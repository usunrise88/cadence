import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { evalsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Eval } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { WORST_N } from "@/shell/evaluation/format";
import { EvalPanel } from "./EvalPanel";
import { ECDF_ROWS } from "./model";
import { EVAL, EVAL_STREAMING, WORST } from "./testdata";

const runCommand = vi.fn();
const openDocument = vi.fn();
const openPanelById = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({
  ...(await orig<object>()),
  useProject: () => "demo",
  openDocument: (d: string) => openDocument(d),
  openPanelById: (id: string) => openPanelById(id),
}));
// ECharts needs a canvas; the report's charts are checked through their specs (model.test.ts).
vi.mock("@/shell/charts", () => ({ AnalyticsChart: ({ spec }: { spec: { kind: string; title: string } }) => <div data-chart={spec.kind}>{spec.title}</div> }));

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(evalsGetQueryKey({ path: { id: "evl_1" }, query: { worst: WORST_N, cell: "evc_s_he_160" } }), { ...EVAL, cells: [{ ...EVAL.cells![0]!, worst: WORST }] });
  // The ECDF reads up to ECDF_ROWS rows of the selected cell and of its baseline.
  for (const c of EVAL.cells!.slice(0, 2)) qc.setQueryData(evalsGetQueryKey({ path: { id: "evl_1" }, query: { worst: ECDF_ROWS, cell: c.id } }), { ...EVAL, cells: [{ ...c, worst: WORST }] });
  useSelection.setState({ activeDoc: "eval:evl_1", selections: {}, pins: {} });
  runCommand.mockReset();
  openDocument.mockReset();
  openPanelById.mockReset();
});
afterEach(() => cleanup());

function wrap(ev: Eval = EVAL, tab?: "details") {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "eval:eval:evl_1", panelId: "eval", doc: "eval:evl_1", visible: false }}>
          <EvalPanel panelId="eval" instanceId="eval:eval:evl_1" doc="eval:evl_1" tab={tab} entity={{ id: ev.id, name: "x", state: ev.status, rev: ev.rev, eval: ev }} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Eval report", () => {
  it("shows the matrix with the primary cell, tones by glyph, the gate and its checks", () => {
    wrap();
    const matrix = screen.getByRole("table", { name: "Golden sets by latency profile" });
    const primary = matrix.querySelector('[data-cell="evc_s_he_160"]') as HTMLElement;
    expect(primary.getAttribute("data-primary")).toBe("true");
    expect(primary.getAttribute("data-tone")).toBe("better");
    expect(primary.textContent).toContain("10.00 %");
    expect(primary.textContent).toContain("▼");
    expect(primary.textContent).toContain("Δ −2.0 pp [−3.0, −1.0]");
    expect(primary.getAttribute("aria-label")).toContain("better than the baseline");
    expect(primary.getAttribute("aria-pressed")).toBe("true");
    expect(matrix.querySelector('[data-cell="evc_s_sr_160"]')!.textContent).toContain("▲");
    expect(within(matrix).getByLabelText("Not evaluated at this profile")).toBeTruthy();
    expect(document.querySelector('[data-slot="gate-verdict"]')!.getAttribute("data-verdict")).toBe("passed");
    expect(screen.getByRole("list", { name: "Gate checks" }).querySelectorAll("li")).toHaveLength(2);
    expect([...document.querySelectorAll("[data-chart]")].map((c) => c.getAttribute("data-chart"))).toEqual(["heatmap", "forest", "bar", "bar", "line", "line"]);
  });

  it("selects a cell into the selection bus and opens an utterance in Diff", async () => {
    wrap();
    fireEvent.click(document.querySelector('[data-cell="evc_s_he_80"]')!);
    expect(useSelection.getState().selections["eval:evl_1"]).toBe("cell:evc_s_he_80");
    act(() => useSelection.setState({ selections: {} }));
    const table = await screen.findByRole("table", { name: /Worst utterances/ });
    // Rows with errors only, by default; the RTL text keeps its direction.
    expect(table.querySelectorAll("tbody tr")).toHaveLength(2);
    expect(table.querySelector("tbody td[dir]")!.getAttribute("dir")).toBe("rtl");
    fireEvent.keyDown(table.querySelector('[data-utterance="4"]')!, { key: "Enter" });
    expect(useSelection.getState().selections["eval:evl_1"]).toBe("cell:evc_s_he_160/utt:4");
    expect(openPanelById).toHaveBeenCalledWith("diff");
  });

  it("groups streaming and robustness charts; an empty section starts folded and still opens", () => {
    wrap(EVAL);
    const robustness = document.querySelector('[data-slot="robustness"]') as HTMLElement;
    expect(robustness.getAttribute("data-empty")).toBe("true");
    const toggle = within(robustness).getByRole("button", { name: "Robustness" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(robustness.textContent).toContain("no augmentation axis");
    const streaming = document.querySelector('[data-slot="streaming"]') as HTMLElement;
    expect(within(streaming).getByRole("button", { name: "Streaming" }).getAttribute("aria-expanded")).toBe("true");
    expect(within(streaming).getByText("WER against latency · fleurs-he")).toBeTruthy();
  });

  it("draws latency, stability, entity accuracy and the robustness matrix when the eval has them", () => {
    wrap(EVAL_STREAMING);
    const kinds = [...document.querySelectorAll("[data-chart]")].map((c) => c.textContent);
    expect(kinds).toEqual(
      expect.arrayContaining([
        "Entity accuracy · fleurs-he · 160ms",
        "WER against latency · fleurs-he",
        "Latency to final · fleurs-he",
        "Unstable partial words · fleurs-he",
        "Partial edits per second · fleurs-he",
        "WER degradation under augmentation",
      ]),
    );
    expect(document.querySelector('[data-slot="robustness"]')!.textContent).toContain("telephony");
    // The replay set has no latency: its reason shows instead of a chart.
    fireEvent.click(document.querySelector('[data-cell="evc_s_sr_160"]')!);
    const streaming = document.querySelector('[data-slot="streaming"]') as HTMLElement;
    expect(within(streaming).getByRole("list", { name: "Why latency to final is unavailable" }).textContent).toContain("no VAD for sr");
  });

  it("runs the gate on this revision", async () => {
    runCommand.mockResolvedValue(EVAL);
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Run the gate again" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("evals.gate", { eval: { id: "evl_1", rev: 3 } }));
  });

  it("registers the model: check, then an inline confirm", async () => {
    runCommand.mockImplementation((_id: string, a: { dryRun?: boolean }) =>
      Promise.resolve(a.dryRun ? { name: "model/demo", model: { gate: { verdict: "passed" }, weightsHash: "b3:0123456789abcdef0123" } } : { id: "ver_m", model: {} }),
    );
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Register model…" }));
    const form = screen.getByRole("form", { name: "Register model version" });
    expect(within(form).getByRole("button", { name: "Register" })).toHaveProperty("disabled", true);
    fireEvent.click(within(form).getByRole("button", { name: "Check" }));
    expect(await within(form).findByText("model/demo")).toBeTruthy();
    expect(runCommand).toHaveBeenCalledWith("models.register", { project: "demo", body: { checkpointId: "ckp_b", evalId: "evl_1" }, dryRun: true });
    fireEvent.click(within(form).getByRole("button", { name: "Register" }));
    fireEvent.click(within(form).getByRole("button", { name: /Confirm register/ }));
    await waitFor(() => expect(openDocument).toHaveBeenCalledWith("model:ver_m"));
  });

  it("shows why a model cannot be registered and the gate problem", async () => {
    const failed = { ...EVAL, gate: { ...EVAL.gate!, verdict: "failed" as const } };
    wrap(failed);
    expect(screen.getByRole("button", { name: "Register model…" })).toHaveProperty("disabled", true);
    expect(screen.getByRole("button", { name: "Register model…" }).getAttribute("title")).toBe("The gate did not pass");
  });

  it("fills live: a running eval shows its progress", () => {
    wrap({ ...EVAL, status: "running", gate: undefined, progress: { cellsTotal: 6, cellsDone: 3, cellsCached: 3 } });
    const bar = screen.getByRole("progressbar", { name: "Cells scored" });
    expect(bar.getAttribute("aria-valuenow")).toBe("3");
    expect(screen.getByRole("button", { name: "Run the gate" })).toHaveProperty("disabled", true);
  });

  it("lists the definition in Details", () => {
    wrap(EVAL, "details");
    expect(screen.getByRole("table", { name: "Golden sets" }).textContent).toContain("replay");
    expect(screen.getByText(/paired blockwise bootstrap, 1000 samples, 95 % level/)).toBeTruthy();
  });
});
