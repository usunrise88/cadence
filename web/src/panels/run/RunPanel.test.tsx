import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { checkpointsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Run } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { useEditRequests } from "@/shell/entity/edits";
import { useSelection } from "@/shell/selection/store";
import { useTrainingFocus } from "@/shell/training/focus";
import { RunPanel } from "./RunPanel";

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

const run = {
  id: "run_0192ab34cdef",
  projectId: "prj_1",
  status: "running",
  init: "checkpoint",
  checkpointId: "ckp_parent",
  parentRunId: "run_parent",
  baseModel: { kind: "base_model", id: "ver_base", name: "base-model/x", version: "2026-09-30.abc" },
  family: { name: "fixture-family", versionId: "ver_fam" },
  mix: { id: "mix_1", name: "he-first", revision: 3, hash: `b3:${"a".repeat(64)}` },
  recipe: { pipeline: "train-stage", source: "repository", version: "abc", commit: "abcdef123" },
  pipelineRunId: "plr_1",
  trainStep: "train",
  steps: 500,
  gpus: 1,
  precision: "bf16",
  timeline: [
    { step: "calibrate", kind: "fx_calibrate", kindVersion: "1", role: "calibrate", state: "done", attempts: 1 },
    { step: "train", kind: "fx_train", kindVersion: "1", role: "train", state: "running", attempts: 2, oomRetries: 1, batchScale: 0.75, jobId: "job_train", startedAt: "2026-09-30T10:00:00Z" },
  ],
  currentJobId: "job_train",
  departures: [{ step: "train", param: "steps", value: 500, default: 3000 }],
  parentDiff: [{ param: "peak_lr", value: 0.0001, parentValue: 0.001 }],
  finalMetrics: {},
  checkpointCount: 2,
  bestCheckpointId: "ckp_b",
  gpuHours: 0.42,
  estimate: { basis: "table", gpuHours: { value: 1.2, low: 0.6, high: 1.8 } },
  rev: 4,
  actor: { kind: "user", id: "usr_admin" },
  createdAt: "2026-09-30T10:00:00Z",
  updatedAt: "2026-09-30T10:05:00Z",
} as unknown as Run;

let qc: QueryClient;
beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(checkpointsListQueryKey({ path: { p: "demo" }, query: { run: run.id } }), {
    keepTopK: 3,
    items: [
      { id: "ckp_a", runId: run.id, projectId: "prj_1", artifact: "b3:x", kind: "trained", step: 100, valWer: 0.35, kept: true, rank: 2, createdAt: "" },
      { id: "ckp_b", runId: run.id, projectId: "prj_1", artifact: "b3:y", kind: "trained", step: 200, valWer: 0.31, kept: true, rank: 1, createdAt: "" },
    ],
  });
  runCommand.mockReset().mockResolvedValue({});
  openDocument.mockReset();
  openPanelById.mockReset();
  useSelection.setState({ selections: {} });
  useTrainingFocus.setState({ job: null, pipelineRun: null });
});
afterEach(() => cleanup());

function wrap(r: Run = run) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: `run:run:${r.id}`, panelId: "run", doc: `run:${r.id}`, visible: false }}>
          <RunPanel panelId="run" instanceId={`run:run:${r.id}`} doc={`run:${r.id}`} tab="overview" entity={{ id: r.id, name: "x", state: r.status, run: r }} />
        </PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("Run document", () => {
  it("shows the stage timeline with OOM retries, the parent diff, departures and spend", () => {
    wrap();
    const train = document.querySelector('[data-stage="train"]') as HTMLElement;
    expect(train.getAttribute("data-state")).toBe("running");
    expect(train.textContent).toContain("1 OOM retry");
    expect(train.textContent).toContain("batch 0.75×");
    expect(screen.getByRole("table", { name: "Config diff against the parent run" }).textContent).toContain("peak_lr");
    expect(screen.getByRole("table", { name: "Departures from defaults" }).textContent).toContain("3000");
    expect(document.querySelector('[data-slot="gpu-hours"]')!.textContent).toContain("0.42 used of ~1.2 estimated (0.6–1.8, table)");
    fireEvent.click(within(train).getByRole("button", { name: "Logs" }));
    expect(useTrainingFocus.getState().job?.id).toBe("job_train");
    expect(openPanelById).toHaveBeenCalledWith("logs");
  });

  it("pauses and stops the current step job", async () => {
    wrap();
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.pause", { jobId: "job_train" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm stop" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("jobs.cancel", { jobId: "job_train" }));
    cleanup();
    // An ended run has no job actions (the header offers Resume from checkpoint).
    wrap({ ...run, status: "failed", currentJobId: undefined });
    expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("starts a new stage with an explicit peak LR, estimate first", async () => {
    wrap();
    // The header's "New stage from checkpoint" (runs.stage without a body) asks this document to open the form.
    act(() => useEditRequests.getState().request(`run:${run.id}`));
    const form = screen.getByRole("form", { name: "New stage from checkpoint" });
    expect(within(form).getByRole("button", { name: "Estimate" })).toHaveProperty("disabled", true);
    fireEvent.change(within(form).getByLabelText("Peak LR"), { target: { value: "0.0001" } });
    runCommand.mockResolvedValueOnce({ basis: "measured", gpuHours: { value: 1, low: 0.9, high: 1.1 }, durationSeconds: { value: 3600, low: 3000, high: 4000 }, steps: 500, budget: { withinDailyBudget: true } });
    fireEvent.click(within(form).getByRole("button", { name: "Estimate" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("runs.stage", { run, dryRun: true, body: { peakLr: 0.0001, checkpoint: "ckp_b" } }));
    expect(await within(form).findByText(/~1 GPU-h/)).toBeTruthy();
    runCommand.mockResolvedValueOnce({ id: "run_new" });
    fireEvent.click(within(form).getByRole("button", { name: "Start stage" }));
    await waitFor(() => expect(openDocument).toHaveBeenCalledWith("run:run_new"));
  });

  it("opens the stage form from Checkpoints with that checkpoint", () => {
    wrap();
    act(() => useSelection.getState().select(`run:${run.id}`, "stage:ckp_a"));
    const form = screen.getByRole("form", { name: "New stage from checkpoint" });
    expect((within(form).getByLabelText("Checkpoint") as HTMLSelectElement).value).toBe("ckp_a");
  });
});
