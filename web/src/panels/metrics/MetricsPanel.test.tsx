import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { metricsGetQueryKey, runsGetQueryKey, runsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { MetricSeriesSet } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useSelection } from "@/shell/selection/store";
import { MetricsPanel } from "./MetricsPanel";

vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo" }));
// The chart primitive draws on canvas (browser tests cover it); here a stand-in lists what it was given.
vi.mock("@/shell/charts", () => ({
  TimeSeriesChart: (p: { title: string; series: { id: string }[]; markers: { id: string }[]; yScale: string; xKey: string }) => (
    <figure data-title={p.title} data-series={p.series.map((s) => s.id).join(",")} data-markers={p.markers.map((m) => m.id).join(",")} data-scale={p.yScale} data-x={p.xKey} />
  ),
}));

const pt = (step: number, value: number) => ({ x: step, value, min: value, max: value, count: 1, step });
const set = (runId: string): MetricSeriesSet => ({
  runId,
  x: "step",
  maxPoints: 1000,
  lastStep: 20,
  series: [
    { name: "loss", total: 2, binned: false, points: [pt(10, 2.5), pt(20, 2.1)] },
    { name: "lr", total: 2, binned: false, points: [pt(10, 1e-4), pt(20, 9e-5)] },
    { name: "val_wer", total: 1, binned: false, points: [pt(20, 0.31)] },
  ],
  checkpoints: [{ id: "ckp_b", step: 20, valWer: 0.31, kept: true }],
});

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(runsListQueryKey({ path: { p: "demo" }, query: { limit: 1 } }), { items: [] });
  for (const id of ["run_1", "run_2"]) {
    qc.setQueryData(runsGetQueryKey({ path: { id } }), { id, init: "base", mix: { name: id === "run_1" ? "first" : "second", revision: 1 } });
    qc.setQueryData(metricsGetQueryKey({ path: { id }, query: { x: "step", maxPoints: 1000 } }), set(id));
  }
  useSelection.setState({ activeDoc: null, selections: {}, pins: {} });
});
afterEach(() => cleanup());

function wrap() {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "metrics", panelId: "metrics", visible: false }}>
          <MetricsPanel panelId="metrics" instanceId="metrics" />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Metrics", () => {
  it("waits for a run", () => {
    wrap();
    expect(screen.getByText("No run yet")).toBeTruthy();
  });

  it("draws one chart per metric of the active run, with checkpoint marks; lr on a log scale", () => {
    useSelection.setState({ activeDoc: "run:run_1" });
    wrap();
    const charts = [...document.querySelectorAll("figure")];
    expect(charts.map((c) => c.getAttribute("data-title"))).toEqual(["Training loss", "Validation WER", "Learning rate"]);
    expect(charts[0]!.getAttribute("data-markers")).toBe("ckp_b");
    expect(charts[2]!.getAttribute("data-scale")).toBe("log");
    expect(charts[0]!.getAttribute("data-scale")).toBe("linear");
    fireEvent.click(screen.getByRole("button", { name: "Log scale" }));
    expect(document.querySelector("figure")!.getAttribute("data-scale")).toBe("log");
  });

  it("overlays a pinned run when another run becomes active", () => {
    useSelection.setState({ activeDoc: "run:run_1" });
    const { rerender } = wrap();
    fireEvent.click(screen.getByRole("button", { name: "Pin" }));
    useSelection.setState({ activeDoc: "run:run_2" });
    rerender(
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <PanelContext.Provider value={{ instanceId: "metrics", panelId: "metrics", visible: false }}>
            <MetricsPanel panelId="metrics" instanceId="metrics" />
          </PanelContext.Provider>
        </TooltipProvider>
      </QueryClientProvider>,
    );
    expect(document.querySelector("figure")!.getAttribute("data-series")).toBe("run_2:loss,run_1:loss");
    expect(screen.getByLabelText("Pinned runs").textContent).toContain("first r1");
  });
});
