import type { PipelineRun, PipelineRunSummary, PipelineStep } from "@/api/gen/types.gen";

// Pure logic of the Pipeline run panel: applying live events to the cached run, step order and durations.

/** pipeline_run.{id} events: started / state_changed carry the run summary, step_changed one step. */
export function applyRunEvent(run: PipelineRun | undefined, type: string, payload: unknown): PipelineRun | undefined {
  if (!run || !payload || typeof payload !== "object") return run;
  const p = payload as { pipelineRun?: PipelineRunSummary; step?: PipelineStep; runState?: PipelineRun["state"] };
  if (type === "pipeline_run.step_changed" && p.step) {
    const step = p.step;
    const has = run.steps.some((s) => s.id === step.id);
    const steps = has ? run.steps.map((s) => (s.id === step.id ? step : s)) : [...run.steps, step];
    return { ...run, ...(p.runState ? { state: p.runState } : {}), steps: sortSteps(steps) };
  }
  if (p.pipelineRun && p.pipelineRun.id === run.id) {
    if (p.pipelineRun.rev < run.rev) return run;
    return { ...run, ...p.pipelineRun, steps: run.steps };
  }
  return run;
}

export function sortSteps(steps: PipelineStep[]): PipelineStep[] {
  return [...steps].sort((a, b) => a.position - b.position || a.step.localeCompare(b.step));
}

/** Wall time of a step or run: finished − started, or now − started while it runs. */
export function elapsedSeconds(startedAt: string | undefined, finishedAt: string | undefined, now = Date.now()): number | undefined {
  if (!startedAt) return undefined;
  const end = finishedAt ? Date.parse(finishedAt) : now;
  return Math.max(0, (end - Date.parse(startedAt)) / 1000);
}

export function retryable(s: PipelineStep): boolean {
  return s.state === "failed" || s.state === "cancelled";
}

/** The pipeline file the run was read from, when it came from the repository. */
export function pipelineFile(run: PipelineRunSummary): string | undefined {
  return run.source === "repository" ? `pipelines/${run.pipeline}.yaml` : undefined;
}

export function shortHash(hash: string): string {
  const h = hash.startsWith("b3:") ? hash.slice(3) : hash;
  return `b3:${h.slice(0, 10)}…`;
}

export function formatBytes(n: number | undefined): string {
  if (n === undefined) return "";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}
