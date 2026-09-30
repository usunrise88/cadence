import { useQuery, type QueryClient } from "@tanstack/react-query";
import { Play } from "iconoir-react";
import type { CadenceEvent, Run } from "@/api/gen/types.gen";
import { runsGetOptions, runsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";
import { runLabel } from "@/shell/training/runs";

// A training run (docs/spec/04-blocks.md Block 2; R12, R13, R44): project work that mirrors one pipeline run. Its
// state is the run status; run.{id}.status carries only the status fields, so the document re-reads runs.get.

export function runToEntity(r: Run): EntityData {
  return {
    id: r.id,
    name: runLabel(r),
    state: r.status,
    rev: r.rev,
    updatedAt: r.updatedAt,
    createdAt: r.createdAt,
    projectId: r.projectId,
    actor: r.actor,
    run: r,
  };
}

const num = (v: number | undefined, digits = 2) => (v === undefined ? "—" : String(Math.round(v * 10 ** digits) / 10 ** digits));

/** run.{id}.status and run.{id}.checkpoints: the run changed; read it again (the events carry a summary only). */
export function patchRun(qc: QueryClient, batch: CadenceEvent[], id: string): void {
  if (batch.length === 0) return;
  void qc.invalidateQueries({ queryKey: runsGetQueryKey({ path: { id } }) });
  void qc.invalidateQueries({ queryKey: [{ _id: "runsList" }] });
}

const ended = (e: EntityData) => ["done", "failed", "cancelled"].includes(e.state);

export const runEntity: EntityManifest = {
  kind: "run",
  apiEntity: "runs",
  layer: "project",
  template: "work",
  verbs: [
    { verb: "stage", primary: true, enabled: (e) => ((e.run as Run | undefined)?.checkpointCount ? true : "The run has no checkpoint yet") },
    { verb: "resume", enabled: (e) => (e.state === "failed" || e.state === "cancelled" ? true : "Only a failed or cancelled run resumes from its training state") },
  ],
  facts: [
    { label: "Steps", value: (e) => String((e.run as Run | undefined)?.steps ?? "—") },
    {
      label: "GPU-hours",
      value: (e) => {
        const r = e.run as Run | undefined;
        return r ? `${num(r.gpuHours)} of ~${num(r.estimate?.gpuHours.value)}` : "—";
      },
    },
    { label: "Val WER", value: (e) => num((e.run as Run | undefined)?.finalMetrics.val_wer, 4) },
    { label: "Checkpoints", value: (e) => String((e.run as Run | undefined)?.checkpointCount ?? "—") },
  ],
  comparable: false,
  draftable: false,
  loopStep: (e) => (e.state === "done" ? "review" : ended(e) ? "decide" : "run"),
  nextStep: (e) =>
    e.state === "done"
      ? { step: "review", title: "Compare the checkpoints' validation WER, then evaluate the best one (phase 3) or start a new stage from it", command: "runs.stage" }
      : e.state === "failed" || e.state === "cancelled"
        ? { step: "decide", title: "Read the failed step's log, then resume from the training state or retry the step", command: "runs.resume" }
        : { step: "run", title: "Watch loss and validation WER in Metrics; checkpoints appear as they are saved" },
  icon: Play,
  live: { topics: (id) => [`run.${id}.status`, `run.${id}.checkpoints`], patch: patchRun },
  useData(id) {
    const q = useQuery({ ...runsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? runToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
