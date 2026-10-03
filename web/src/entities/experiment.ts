import { useQuery, type QueryClient } from "@tanstack/react-query";
import { Flask } from "iconoir-react";
import type { CadenceEvent, Experiment } from "@/api/gen/types.gen";
import { experimentsGetOptions, experimentsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

// An experiment (docs/spec/04-blocks.md "Experiments and sweeps"): project work that groups the runs answering one
// question on a fixed mix revision and base model. Its state follows its sweeps: running while one runs, planned
// before any run, else done. Events on entity.experiment.{id} carry a summary only, so the document re-reads
// experiments.get (and the runs list it filters).

/** The work state an experiment shows: a running sweep makes it running; no run yet, planned; else done. */
export function experimentState(e: Experiment): string {
  if (e.sweeps.some((s) => s.state === "running")) return "running";
  return e.runCount === 0 ? "planned" : "done";
}

export function experimentToEntity(e: Experiment): EntityData {
  return {
    id: e.id,
    name: e.name,
    state: experimentState(e),
    rev: e.rev,
    updatedAt: e.updatedAt,
    createdAt: e.createdAt,
    projectId: e.projectId,
    actor: e.actor,
    experiment: e,
  };
}

const num = (v: number | undefined, digits = 2) => (v === undefined ? "—" : String(Math.round(v * 10 ** digits) / 10 ** digits));
const exp = (e: EntityData) => e.experiment as Experiment | undefined;

/** entity.experiment.{id}: a run, a sweep or the experiment changed; read it again. */
export function patchExperiment(qc: QueryClient, batch: CadenceEvent[], id: string): void {
  if (batch.length === 0) return;
  void qc.invalidateQueries({ queryKey: experimentsGetQueryKey({ path: { id } }) });
  void qc.invalidateQueries({ queryKey: [{ _id: "experimentsList" }] });
  void qc.invalidateQueries({ queryKey: [{ _id: "runsList" }] });
}

export const experimentEntity: EntityManifest = {
  kind: "experiment",
  apiEntity: "experiments",
  layer: "project",
  template: "work",
  verbs: [
    {
      verb: "run",
      command: "sweeps.run",
      primary: true,
      enabled: (e) => (exp(e)?.sweeps.some((s) => s.state === "running") ? "A sweep of this experiment is running; its runs queue one after another" : true),
    },
    {
      verb: "register",
      command: "models.register",
      enabled: (e) => {
        const best = exp(e)?.best;
        if (!best) return "No run has a checkpoint with a validation WER yet";
        return best.registrable ? true : (best.reason ?? "The best checkpoint has no passed gate yet");
      },
    },
  ],
  facts: [
    { label: "Runs", value: (e) => String(exp(e)?.runCount ?? "—") },
    { label: "Best val WER", value: (e) => num(exp(e)?.best?.valWer, 4) },
    {
      label: "Sweep",
      value: (e) => {
        const s = exp(e)?.sweeps[0];
        return s ? `${s.state} · ${num(s.gpuHoursSpent)} of ${num(s.gpuHourCap)} GPU-h` : "none";
      },
    },
    {
      label: "Mix",
      value: (e) => {
        const m = exp(e)?.mix;
        return m ? `${m.name} rev ${m.revision}` : "—";
      },
    },
  ],
  comparable: false,
  draftable: false,
  loopStep: (e) => (e.state === "running" ? "run" : e.state === "planned" ? "prepare" : exp(e)?.best?.registrable ? "decide" : "review"),
  nextStep: (e) => {
    const x = exp(e);
    if (!x || x.runCount === 0) return { step: "prepare", title: "Run a sweep over the parameters in question (estimate first), or start a run with this experiment", command: "sweeps.run" };
    if (e.state === "running") return { step: "run", title: "Runs queue one after another; compare them here as checkpoints arrive" };
    if (x.best?.registrable) return { step: "decide", title: "The best run's checkpoint passed its gate: register it as a model version", command: "models.register" };
    return { step: "review", title: x.best?.reason ?? "Compare the runs, then evaluate the best checkpoint (evals.new, then evals.gate)" };
  },
  icon: Flask,
  live: { topics: (id) => [`entity.experiment.${id}`], patch: patchExperiment },
  useData(id) {
    const q = useQuery({ ...experimentsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? experimentToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
