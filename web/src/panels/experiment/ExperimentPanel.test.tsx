import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Experiment } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useEditRequests } from "@/shell/entity/edits";
import { ExperimentPanel } from "./ExperimentPanel";

const runCommand = vi.fn();
const openDocument = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));
vi.mock("@/shell/panel/actions", async (orig) => ({ ...(await orig<object>()), useProject: () => "demo", openDocument: (d: string) => openDocument(d) }));
// The charts load ECharts lazily; the panel test checks what it hands them.
const charts = vi.hoisted(() => ({ specs: [] as { kind: string; title: string }[] }));
vi.mock("@/shell/charts", () => ({
  AnalyticsChart: ({ spec }: { spec: { kind: string; title: string } }) => {
    charts.specs.push(spec);
    return <div data-testid={`chart-${spec.kind}`}>{spec.title}</div>;
  },
}));

const run = (id: string, values: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
  runId: id,
  status: "done",
  values,
  departures: [] as string[],
  gpuHours: 0.5,
  best: false,
  createdAt: "2026-10-02T10:00:00Z",
  ...extra,
});

const experiment = {
  id: "exp_1",
  projectId: "prj_1",
  name: "lr",
  question: "Does a lower peak LR help Hebrew?",
  mix: { id: "mix_1", name: "he-first", revision: 3, replayShare: 0.15 },
  baseModel: { kind: "base_model", id: "ver_base", name: "base-model/x", version: "2026-09-30.abc" },
  runCount: 3,
  parameters: [
    { name: "peak_lr", default: 0.0002, swept: true, departs: true },
    { name: "steps", default: 3000, swept: false, departs: true },
  ],
  runs: [
    run("run_aaaaaaaa1", { peak_lr: 0.0001, steps: 500 }, { departures: ["peak_lr", "steps"], sweepId: "swp_1", point: 0, bestValWer: 0.31, bestCheckpointId: "ckp_a" }),
    run("run_bbbbbbbb2", { peak_lr: 0.0002, steps: 500 }, { departures: ["steps"], sweepId: "swp_1", point: 1, bestValWer: 0.27, bestCheckpointId: "ckp_b", best: true, eval: { evalId: "evl_1", status: "done", verdict: "passed" } }),
    run("run_cccccccc3", { peak_lr: 0.0004, steps: 500 }, { departures: ["peak_lr", "steps"], sweepId: "swp_1", point: 2, status: "running" }),
  ],
  best: { runId: "run_bbbbbbbb2", checkpointId: "ckp_b", valWer: 0.27, registrable: true, eval: { evalId: "evl_1", status: "done", verdict: "passed" } },
  sweeps: [
    {
      id: "swp_1",
      experimentId: "exp_1",
      mode: "grid",
      parameters: [{ name: "peak_lr", values: [0.0001, 0.0002, 0.0004] }],
      gpuHourCap: 4,
      state: "running",
      points: [
        { index: 0, values: { peak_lr: 0.0001 }, state: "done", runId: "run_aaaaaaaa1" },
        { index: 1, values: { peak_lr: 0.0002 }, state: "done", runId: "run_bbbbbbbb2" },
        { index: 2, values: { peak_lr: 0.0004 }, state: "running", runId: "run_cccccccc3" },
      ],
      runsDone: 2,
      currentRunId: "run_cccccccc3",
      gpuHoursSpent: 1,
      estimateGpuHours: 1.5,
      rev: 2,
      actor: { kind: "user", id: "usr_admin" },
      createdAt: "",
      updatedAt: "",
    },
  ],
  rev: 2,
  actor: { kind: "user", id: "usr_admin" },
  createdAt: "2026-10-02T10:00:00Z",
  updatedAt: "2026-10-02T10:00:00Z",
} as unknown as Experiment;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  runCommand.mockReset().mockResolvedValue({});
  openDocument.mockReset();
  charts.specs = [];
});
afterEach(() => cleanup());

function wrap(e: Experiment = experiment) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: `experiment:experiment:${e.id}`, panelId: "experiment", doc: `experiment:${e.id}`, visible: false }}>
          <ExperimentPanel panelId="experiment" instanceId={`experiment:experiment:${e.id}`} doc={`experiment:${e.id}`} tab="overview" entity={{ id: e.id, name: e.name, state: "running", experiment: e }} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

const rowsOf = () => within(screen.getByRole("table", { name: "Runs of the experiment" })).getAllByRole("row").slice(1);

describe("Experiment document", () => {
  it("compares the runs: departures highlighted, the best run marked, the sweep's spend against its cap", () => {
    wrap();
    expect(screen.getByText("Does a lower peak LR help Hebrew?")).toBeTruthy();
    const rows = rowsOf();
    expect(rows).toHaveLength(3);
    const best = rows[1]!;
    expect(best.getAttribute("data-best")).toBe("true");
    expect(best.textContent).toContain("◆ best");
    expect(best.textContent).toContain("passed");
    // peak_lr departs in runs 1 and 3, not in the best run; steps departs everywhere.
    expect(rows[0]!.querySelectorAll("[data-departs]")).toHaveLength(2);
    expect(best.querySelectorAll("[data-departs]")).toHaveLength(1);
    expect(screen.getByRole("columnheader", { name: /peak_lr\s*swept/ })).toBeTruthy();
    const sweep = document.querySelector('[data-sweep="swp_1"]')!;
    expect(sweep.querySelector('[data-slot="sweep-progress"]')!.textContent).toBe("2 of 3 runs");
    expect(sweep.querySelector('[data-slot="sweep-spend"]')!.textContent).toBe("1 of 4 GPU-h cap");
    fireEvent.click(within(sweep as HTMLElement).getByRole("button", { name: /current/ }));
    expect(openDocument).toHaveBeenCalledWith("run:run_cccccccc3");
    // Charts: the scatter of a swept parameter against WER and parallel coordinates for the sweep.
    expect(screen.getByTestId("chart-scatter")).toBeTruthy();
    expect(screen.getByTestId("chart-parallel")).toBeTruthy();
  });

  it("Compare N narrows the table and the charts to the chosen runs", () => {
    wrap();
    const compare = () => screen.getByRole("button", { name: /^Compare/ });
    expect((compare() as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: "Select run aaaaaaaa" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select run bbbbbbbb" }));
    expect(compare().textContent).toBe("Compare 2");
    charts.specs = [];
    fireEvent.click(compare());
    expect(rowsOf()).toHaveLength(2);
    const parallel = charts.specs.find((s) => s.kind === "parallel") as unknown as { lines: unknown[] };
    expect(parallel.lines).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Show all 3" }));
    expect(rowsOf()).toHaveLength(3);
  });

  it("starts a sweep only with a fresh estimate that fits the cap", async () => {
    // A sweep runs: the form opens from the header's Run sweep (an edit request) once it ended.
    wrap({ ...experiment, sweeps: [{ ...experiment.sweeps[0]!, state: "done", currentRunId: undefined }] });
    act(() => useEditRequests.getState().request("experiment:exp_1"));
    const form = await screen.findByRole("form", { name: "New sweep" });
    const start = () => within(form).getByRole("button", { name: "Start sweep" }) as HTMLButtonElement;
    fireEvent.change(within(form).getByLabelText("Parameter 1 name"), { target: { value: "peak_lr" } });
    fireEvent.change(within(form).getByLabelText("Parameter 1 values"), { target: { value: "0.0001, 0.0003" } });
    fireEvent.change(within(form).getByLabelText("GPU-hour cap"), { target: { value: "2" } });
    expect(start().disabled).toBe(true);
    const plan = {
      experimentId: "exp_1",
      mode: "grid",
      points: [
        { index: 0, values: { peak_lr: 0.0001 }, state: "pending", estimateGpuHours: 0.5 },
        { index: 1, values: { peak_lr: 0.0003 }, state: "pending", estimateGpuHours: 0.5 },
      ],
      estimateGpuHours: { value: 1, low: 0.5, high: 1.5 },
      gpuHourCap: 2,
      withinCap: true,
      fits: 2,
      basis: "table",
      budget: { remainingGpuHours: 8, withinDailyBudget: true },
    };
    runCommand.mockResolvedValueOnce(plan);
    fireEvent.click(within(form).getByRole("button", { name: "Estimate" }));
    await waitFor(() => expect(form.querySelector('[data-slot="sweep-plan"]')?.textContent).toContain("2 runs, ~1 GPU-h"));
    const body = { mode: "grid", parameters: [{ name: "peak_lr", values: [0.0001, 0.0003] }], gpuHourCap: 2 };
    expect(runCommand).toHaveBeenLastCalledWith("sweeps.run", { experiment: { id: "exp_1", rev: 2 }, body, dryRun: true });
    expect(start().disabled).toBe(false);
    // An edit makes the estimate stale.
    fireEvent.change(within(form).getByLabelText("GPU-hour cap"), { target: { value: "3" } });
    expect(start().disabled).toBe(true);
    fireEvent.change(within(form).getByLabelText("GPU-hour cap"), { target: { value: "2" } });
    expect(start().disabled).toBe(false);
    runCommand.mockResolvedValueOnce({ id: "swp_2", state: "running" });
    fireEvent.click(start());
    await waitFor(() => expect(runCommand).toHaveBeenLastCalledWith("sweeps.run", { experiment: { id: "exp_1", rev: 2 }, body, dryRun: false }));
    await waitFor(() => expect(screen.queryByRole("form", { name: "New sweep" })).toBeNull());
  });

  it("an estimate over the cap cannot start", async () => {
    wrap({ ...experiment, sweeps: [] });
    fireEvent.click(screen.getByRole("button", { name: "New sweep" }));
    const form = screen.getByRole("form", { name: "New sweep" });
    fireEvent.change(within(form).getByLabelText("Parameter 1 name"), { target: { value: "warmup_steps" } });
    fireEvent.change(within(form).getByLabelText("Parameter 1 values"), { target: { value: "50, 100, 200" } });
    runCommand.mockResolvedValueOnce({
      experimentId: "exp_1",
      mode: "grid",
      points: [],
      estimateGpuHours: { value: 12, low: 6, high: 18 },
      gpuHourCap: 8,
      withinCap: false,
      fits: 2,
      basis: "table",
      budget: { remainingGpuHours: 8, withinDailyBudget: false },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Estimate" }));
    await waitFor(() => expect(form.querySelector('[data-slot="sweep-plan"]')?.textContent).toContain("over the cap: the first 2 fit"));
    expect((within(form).getByRole("button", { name: "Start sweep" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("registers the best checkpoint after showing the registration", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Register best" }));
    const box = screen.getByRole("group", { name: "Register the best checkpoint" });
    runCommand.mockResolvedValueOnce({ name: "model/demo", model: { evalId: "evl_1", gate: { verdict: "passed" } } });
    fireEvent.click(within(box).getByRole("button", { name: "Show registration" }));
    await waitFor(() => expect(box.textContent).toContain("Registers as model/demo"));
    expect(runCommand).toHaveBeenLastCalledWith("models.register", { project: "demo", body: { checkpointId: "ckp_b" }, dryRun: true });
    runCommand.mockResolvedValueOnce({ approvalId: "apr_1" });
    fireEvent.click(within(box).getByRole("button", { name: "Confirm register" }));
    await waitFor(() => expect(box.textContent).toContain("waits for an approval (apr_1)"));
  });

  it("Register best is disabled with the reason when the gate has not passed", () => {
    wrap({ ...experiment, best: { ...experiment.best!, registrable: false, reason: "eval evl_1 is not gated yet: run evals.gate on it" } });
    const btn = screen.getByRole("button", { name: "Register best" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.title).toContain("not gated yet");
  });

  it("evaluates the best checkpoint: the plan first", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Evaluate best" }));
    const box = screen.getByRole("group", { name: "Evaluate the best checkpoint" });
    runCommand.mockResolvedValueOnce({ cellsToCompute: 4, cellsCached: 2, estimate: { gpuHours: 0.3, audioHours: 3 } });
    fireEvent.click(within(box).getByRole("button", { name: "Plan" }));
    await waitFor(() => expect(box.textContent).toContain("4 cells to compute, 2 cached"));
    expect(runCommand).toHaveBeenLastCalledWith("evals.new", { project: "demo", body: { subject: { checkpointId: "ckp_b" } }, dryRun: true });
    runCommand.mockResolvedValueOnce({ id: "evl_2", status: "queued" });
    fireEvent.click(within(box).getByRole("button", { name: "Start eval" }));
    await waitFor(() => expect(box.textContent).toContain("Eval evl_2 is queued"));
  });
});
