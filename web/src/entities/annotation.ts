import { useQuery, type QueryClient } from "@tanstack/react-query";
import { Label } from "iconoir-react";
import type { Batch, CadenceEvent } from "@/api/gen/types.gen";
import { batchesGetOptions, batchesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

// An annotation batch (docs/spec/04-blocks.md "Annotation workflow"; phase 4 · stream A): project work — a sample of
// segments people transcribe by hand, frozen into a golden set or training data. Its work state: running while people
// annotate, queued while its freeze runs, done when frozen, failed when the freeze failed. Events on
// entity.annotation_batch.{id} carry a summary, so the document re-reads batches.get and its item queues.

export function batchState(b: Batch): string {
  switch (b.state) {
    case "frozen":
      return "done";
    case "freezing":
      return "queued";
    case "failed":
      return "failed";
  }
  return b.progress.annotations === 0 ? "planned" : "running";
}

export function batchToEntity(b: Batch): EntityData {
  return {
    id: b.id,
    name: b.name,
    state: batchState(b),
    rev: b.rev,
    updatedAt: b.updatedAt,
    createdAt: b.createdAt,
    projectId: b.projectId,
    actor: b.createdBy,
    batch: b,
  };
}

const batch = (e: EntityData) => e.batch as Batch | undefined;
const pct = (v: number | undefined) => (v === undefined ? "—" : `${(v * 100).toFixed(1)} %`);

/** entity.annotation_batch.{id}: an item was annotated or adjudicated, a reviewer invited, the freeze moved. */
export function patchBatch(qc: QueryClient, batchEvents: CadenceEvent[], id: string): void {
  if (batchEvents.length === 0) return;
  void qc.invalidateQueries({ queryKey: batchesGetQueryKey({ path: { id } }) });
  void qc.invalidateQueries({ queryKey: [{ _id: "batchItemsList" }] });
  void qc.invalidateQueries({ queryKey: [{ _id: "batchesList" }] });
}

export const annotationBatchEntity: EntityManifest = {
  kind: "annotation_batch",
  apiEntity: "batches",
  layer: "project",
  template: "work",
  verbs: [
    {
      verb: "freeze",
      primary: true,
      enabled: (e) => {
        const b = batch(e);
        if (!b) return "Loading";
        return b.canFreeze.ok ? true : (b.canFreeze.reasons[0] ?? "The batch cannot freeze yet");
      },
    },
  ],
  facts: [
    {
      label: "Progress",
      value: (e) => {
        const b = batch(e);
        if (!b) return "—";
        const done = b.progress.agreed + b.progress.adjudicated + b.progress.excluded;
        return `${done} of ${b.progress.items} resolved`;
      },
    },
    { label: "Inter-annotator WER", value: (e) => pct(batch(e)?.agreement.iaaWer) },
    { label: "Adjudication", value: (e) => String(batch(e)?.adjudication.queue ?? "—") },
    { label: "Purpose", value: (e) => batch(e)?.purpose ?? "—" },
  ],
  comparable: false,
  draftable: false,
  loopStep: (e) => {
    const b = batch(e);
    if (!b) return "prepare";
    if (b.state === "frozen") return "record";
    if (b.canFreeze.ok) return "decide";
    return b.progress.pending > 0 ? "run" : "review";
  },
  nextStep: (e) => {
    const b = batch(e);
    if (!b) return { step: "prepare", title: "Loading the batch" };
    if (b.state === "frozen") return { step: "record", title: b.purpose === "golden-set" ? "Frozen as a golden set: evaluate against it (evals.new)" : "Frozen as a dataset version: mix it" };
    if (b.state === "freezing") return { step: "run", title: "The accepted items are being cut into the content store" };
    if (b.canFreeze.ok) return { step: "decide", title: "Every item is resolved and the agreement meets its target: freeze (approval)", command: "batches.freeze" };
    if (b.progress.pending > 0) return { step: "run", title: `${b.progress.pending} item(s) need annotations: annotate in the Triage panel, or invite a reviewer`, command: "view.openTriage" };
    if (b.progress.disputed > 0) return { step: "review", title: `${b.progress.disputed} item(s) wait for adjudication below` };
    return { step: "review", title: b.canFreeze.reasons[0] ?? "Review the batch" };
  },
  icon: Label,
  live: { topics: (id) => [`entity.annotation_batch.${id}`], patch: patchBatch },
  useData(id) {
    const q = useQuery({ ...batchesGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? batchToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
