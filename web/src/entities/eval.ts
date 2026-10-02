import { useQuery, type QueryClient } from "@tanstack/react-query";
import { Filter } from "iconoir-react";
import type { CadenceEvent, Eval } from "@/api/gen/types.gen";
import { evalsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";
import { invalidateEval } from "@/shell/evaluation/cache";
import { registrable } from "@/shell/evaluation/format";

// An eval (docs/spec/04-blocks.md Block 3; R20–R24, R43, R54): project work comparing a subject (checkpoint, model
// version or base model) with the baseline on golden sets × latency profiles × decoding. Its state is the eval's
// status; the gate verdict shows as a header fact. entity.eval.{id} and eval.{id}.progress carry summaries, so the
// document re-reads evals.get.

export function evalLabel(e: Pick<Eval, "subject" | "baseline">): string {
  return `${e.subject.label} vs ${e.baseline.label}`;
}

export function evalToEntity(e: Eval): EntityData {
  return {
    id: e.id,
    name: evalLabel(e),
    state: e.status,
    rev: e.rev,
    updatedAt: e.updatedAt,
    createdAt: e.createdAt,
    projectId: e.projectId,
    actor: e.actor,
    eval: e,
  };
}

const evalOf = (e: EntityData) => e.eval as Eval | undefined;

export function patchEval(qc: QueryClient, batch: CadenceEvent[], id: string): void {
  if (batch.length > 0) invalidateEval(qc, id);
}

export const evalEntity: EntityManifest = {
  kind: "eval",
  apiEntity: "evals",
  layer: "project",
  template: "work",
  verbs: [
    {
      verb: "gate",
      primary: true,
      enabled: (e) => (e.state === "done" ? true : e.state === "failed" ? "The eval failed; its cells are incomplete" : "The gate reads a finished eval"),
    },
    // Run eval…: the report's form filled from this eval's axes (scored cells come back cached).
    { verb: "new", command: "evals.new" },
  ],
  facts: [
    { label: "Subject", value: (e) => evalOf(e)?.subject.label ?? "—" },
    { label: "Baseline", value: (e) => evalOf(e)?.baseline.label ?? "—" },
    {
      label: "Cells",
      value: (e) => {
        const p = evalOf(e)?.progress;
        return p ? `${p.cellsDone} of ${p.cellsTotal}${p.cellsCached ? ` (${p.cellsCached} cached)` : ""}` : "—";
      },
    },
    { label: "Gate", value: (e) => (evalOf(e)?.gate ? `${evalOf(e)!.gate!.verdict === "passed" ? "✓ passed" : "✗ failed"}` : "not run") },
  ],
  comparable: false,
  draftable: false,
  loopStep: (e) => {
    const ev = evalOf(e);
    if (e.state === "queued" || e.state === "running") return "run";
    if (e.state === "failed") return "decide";
    if (!ev?.gate) return "review";
    return ev.gate.verdict === "passed" ? "record" : "decide";
  },
  nextStep: (e) => {
    const ev = evalOf(e);
    if (e.state === "queued" || e.state === "running") return { step: "run", title: "The matrix fills as cells are scored; cached cells are ready now" };
    if (e.state === "failed") return { step: "decide", title: "Open the pipeline run to see the failed step, or re-run the missing cells", command: "evals.new" };
    if (!ev?.gate) return { step: "review", title: "Read the deltas and the worst utterances, then run the project's gate", command: "evals.gate" };
    if (ev.gate.verdict === "passed" && registrable(ev) === true) return { step: "record", title: "The gate passed: register the checkpoint as a model version", command: "models.register" };
    if (ev.gate.verdict === "passed") return { step: "record", title: "The gate passed" };
    return { step: "decide", title: "The gate failed: read the failed checks, then train further or change the mix" };
  },
  icon: Filter,
  live: { topics: (id) => [`entity.eval.${id}`, `eval.${id}.progress`], patch: patchEval },
  useData(id) {
    const q = useQuery({ ...evalsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? evalToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
