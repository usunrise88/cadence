import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { checkpointsListQueryKey, runsGetQueryKey, runsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Checkpoint } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { CheckpointsPanel, orderCheckpoints } from "./CheckpointsPanel";

const runCommand = vi.fn();
const openDocument = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: (d: string) => openDocument(d) }));

const ck = (id: string, over: Partial<Checkpoint>): Checkpoint => ({ id, runId: "run_1", projectId: "prj_1", artifact: "b3:x", kind: "trained", kept: false, createdAt: "2026-09-30T10:00:00Z", ...over });
const items = [ck("ckp_a", { step: 100, valWer: 0.35, kept: true, rank: 2 }), ck("ckp_c", { step: 50, valWer: 0.5, rank: 4 }), ck("ckp_b", { step: 200, valWer: 0.31, kept: true, rank: 1 })];

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(runsListQueryKey({ path: { p: "demo" }, query: { limit: 1 } }), { items: [{ id: "run_1" }] });
  qc.setQueryData(runsGetQueryKey({ path: { id: "run_1" } }), { id: "run_1", init: "base", mix: { name: "he-first", revision: 2 } });
  qc.setQueryData(checkpointsListQueryKey({ path: { p: "demo" }, query: { run: "run_1" } }), { keepTopK: 3, items });
  runCommand.mockReset();
  openDocument.mockReset();
  useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "checkpoints", panelId: "checkpoints", visible: false }}>
          <CheckpointsPanel panelId="checkpoints" instanceId="checkpoints" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Checkpoints", () => {
  it("lists the newest run's checkpoints best first, kept marked", () => {
    expect(orderCheckpoints(items).map((c) => c.id)).toEqual(["ckp_b", "ckp_a", "ckp_c"]);
    wrap();
    expect(screen.getByText("he-first r2 · from base · 1")).toBeTruthy();
    const rows = [...document.querySelectorAll("[data-checkpoint]")];
    expect(rows.map((r) => r.getAttribute("data-checkpoint"))).toEqual(["ckp_b", "ckp_a", "ckp_c"]);
    expect(rows[0]!.textContent).toContain("best");
    expect(rows[0]!.textContent).toContain("val WER 31 %");
    expect(rows[2]!.textContent).toContain("not kept");
    expect(within(rows[0] as HTMLElement).getByRole("button", { name: /Evaluate/ })).toHaveProperty("disabled", true);
  });

  it("averages the selected checkpoints", async () => {
    runCommand.mockResolvedValue({ runId: "run_1", step: "fx_average@1", checkpoints: ["ckp_b", "ckp_a"] });
    wrap();
    const average = screen.getByRole("button", { name: /Average selected/ });
    expect(average).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByLabelText("Select checkpoint at step 200"));
    fireEvent.click(screen.getByLabelText("Select checkpoint at step 100"));
    fireEvent.click(screen.getByRole("button", { name: "Average selected (2)" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("checkpoints.average", { runId: "run_1", body: { checkpoints: ["ckp_b", "ckp_a"] } }));
    expect(await screen.findByText(/Averaging 2 checkpoints/)).toBeTruthy();
  });

  it("starts a new stage from a checkpoint in the Run document", () => {
    wrap();
    const b = document.querySelector('[data-checkpoint="ckp_b"]') as HTMLElement;
    fireEvent.click(within(b).getByRole("button", { name: "New stage from here" }));
    expect(useSelection.getState().selections["run:run_1"]).toBe("stage:ckp_b");
    expect(openDocument).toHaveBeenCalledWith("run:run_1");
  });
});
