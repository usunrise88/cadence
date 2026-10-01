import { describe, expect, it } from "vitest";
import type { PipelineRun, PipelineStep } from "@/api/gen/types.gen";
import { applyRunEvent, elapsedSeconds, formatBytes, pipelineFile, retryable, shortHash } from "./model";

const step = (id: string, position: number, state: PipelineStep["state"] = "waiting"): PipelineStep => ({
  id,
  step: `s${position}`,
  position,
  kind: "echo",
  kindVersion: "1",
  state,
  params: {},
  departures: [],
  in: {},
  produces: {},
  attempts: 0,
  attemptLog: [],
});

const run: PipelineRun = {
  id: "plr_1",
  projectId: "prj_1",
  pipeline: "echo",
  source: "template",
  version: "template-abc",
  state: "running",
  rev: 2,
  actor: { kind: "user", id: "usr_admin" },
  createdAt: "2026-09-30T10:00:00Z",
  updatedAt: "2026-09-30T10:00:00Z",
  steps: [step("pls_1", 0, "running"), step("pls_2", 1)],
};

describe("pipeline run events", () => {
  it("replaces a changed step and takes the run's state", () => {
    const next = applyRunEvent(run, "pipeline_run.step_changed", { pipelineRunId: "plr_1", runState: "failed", step: { ...step("pls_1", 0, "failed"), attempts: 1 } })!;
    expect(next.state).toBe("failed");
    expect(next.steps.map((s) => [s.id, s.state])).toEqual([
      ["pls_1", "failed"],
      ["pls_2", "waiting"],
    ]);
  });

  it("merges a newer run summary and ignores an older one", () => {
    const { steps: _steps, ...summary } = run;
    expect(applyRunEvent(run, "pipeline_run.state_changed", { pipelineRun: { ...summary, state: "done", rev: 3 } })!.state).toBe("done");
    expect(applyRunEvent(run, "pipeline_run.state_changed", { pipelineRun: { ...summary, state: "done", rev: 1 } })).toBe(run);
    expect(applyRunEvent(run, "pipeline_run.state_changed", { pipelineRun: { ...summary, id: "plr_2", rev: 9 } })).toBe(run);
    expect(applyRunEvent(undefined, "pipeline_run.state_changed", {})).toBeUndefined();
  });

  it("names the pipeline file only for repository runs", () => {
    expect(pipelineFile(run)).toBeUndefined();
    expect(pipelineFile({ ...run, source: "repository", pipeline: "train-stage" })).toBe("pipelines/train-stage.yaml");
  });

  it("formats hashes, sizes, durations and what can be retried", () => {
    expect(shortHash(`b3:${"ab".repeat(32)}`)).toBe("b3:ababababab…");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(3 * 1024 ** 3)).toBe("3.0 GB");
    expect(elapsedSeconds("2026-09-30T10:00:00Z", "2026-09-30T10:01:30Z")).toBe(90);
    expect(elapsedSeconds(undefined, undefined)).toBeUndefined();
    expect(retryable(step("x", 0, "failed"))).toBe(true);
    expect(retryable(step("x", 0, "done"))).toBe(false);
  });
});
