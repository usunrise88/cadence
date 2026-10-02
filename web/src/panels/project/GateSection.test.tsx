import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { gatesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Gates } from "@/api/gen/types.gen";
import { PanelContext } from "@/shell/panel/context";
import { GateSection } from "./GateSection";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));

const gates: Gates = {
  path: "gates.yaml",
  exists: false,
  ref: "main",
  head: "abc",
  content: "primaryProfile: 160ms\n",
  config: { primaryProfile: "160ms", replay: { maxRegression: 0.005 }, deletionsInsertions: true },
  departures: [],
};

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(gatesGetQueryKey({ path: { p: "demo" } }), gates);
  runCommand.mockReset();
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <PanelContext.Provider value={{ instanceId: "project:demo", panelId: "project", visible: false }}>
        <GateSection slug="demo" ready />
      </PanelContext.Provider>
    </QueryClientProvider>,
  );
}

describe("Project home: Gate", () => {
  it("shows the effective gate and where it comes from", () => {
    wrap();
    expect(screen.getByText("No gates.yaml: the defaults apply")).toBeTruthy();
    expect(screen.getByText("160ms")).toBeTruthy();
    expect(screen.getByText("Every value is the default.")).toBeTruthy();
  });

  it("checks then commits gates.yaml against the defaults ETag; an agent's edit shows its approval", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Edit gates.yaml" }));
    const text = screen.getByLabelText("gates.yaml content");
    fireEvent.change(text, { target: { value: "primaryProfile: 320ms\n" } });
    runCommand.mockResolvedValueOnce({ ...gates, exists: true, config: { primaryProfile: "320ms" }, departures: [{ param: "primaryProfile", value: "320ms", default: "160ms" }] });
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByText(/primaryProfile: 320ms \(default 160ms\)/)).toBeTruthy();
    expect(runCommand).toHaveBeenLastCalledWith("gates.edit", { project: "demo", expect: "defaults", dryRun: true, body: { content: "primaryProfile: 320ms\n", message: "edit gates.yaml" } });
    runCommand.mockResolvedValueOnce({ approvalId: "apr_7" });
    fireEvent.click(screen.getByRole("button", { name: "Commit to main" }));
    expect(await screen.findByText(/waits for an approval \(apr_7\)/)).toBeTruthy();
  });

  it("lists the problems of a file that does not check out", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Edit gates.yaml" }));
    const { ProblemError } = await import("@/api/client");
    runCommand.mockRejectedValueOnce(
      new ProblemError({ type: "https://cadence.local/help/errors/gate-config-invalid", title: "Invalid", status: 422, detail: "gates.yaml is not valid", errors: [{ path: "/target/goldenSets/0", message: "golden-set/x is not adopted" }] }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("golden-set/x is not adopted"));
  });
});
