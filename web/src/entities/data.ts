import { useQuery, type QueryClient } from "@tanstack/react-query";
import { Database, DatabaseScript } from "iconoir-react";
import type { CadenceEvent, DatasetVersion, Source } from "@/api/gen/types.gen";
import { datasetsGetOptions, datasetsGetQueryKey, sourcesGetOptions, sourcesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

// Data entities (phase 4 · stream R; docs/spec/02 "Data entities", the phase-4 plan's decisions 3–4): a dataset version
// is a registry version (ver_… in dataset/…) that starts as a draft when ingested from a mount and is frozen by
// datasets.freeze; a source (src_…) is a corpus with its licence, training clearance, clearing and ingest history.

const n = (v: number | undefined, d = 1) => (typeof v === "number" && Number.isFinite(v) ? String(Math.round(v * 10 ** d) / 10 ** d) : "—");

// ---------------------------------------------------------------- dataset version

export function datasetVersionToEntity(v: DatasetVersion): EntityData {
  return {
    id: v.id,
    name: v.name,
    version: v.version,
    state: v.state,
    updatedAt: v.updatedAt,
    createdAt: v.createdAt,
    actor: v.actor,
    datasetVersion: v,
  };
}

const dsOf = (e: EntityData) => e.datasetVersion as DatasetVersion | undefined;

/** A draft that datasets.freeze is cutting now (its freeze names a pipeline run, frozen is still false). */
export function freezing(v: DatasetVersion | undefined): boolean {
  return !!v && v.state === "draft" && !!v.dataset.freeze?.pipelineRunId && !v.dataset.freeze.frozenAt;
}

export const datasetVersionEntity: EntityManifest = {
  kind: "dataset_version",
  apiEntity: "datasets",
  layer: "registry",
  template: "registry",
  // A draft is previewed and frozen; a frozen version is adopted into the project. Archive is the admin's soft delete.
  verbs: [
    {
      verb: "freeze",
      primary: true,
      enabled: (e) => (dsOf(e)?.state !== "draft" ? "Only a draft is frozen; this version is frozen already" : freezing(dsOf(e)) ? "Freezing now" : true),
    },
    { verb: "preview" },
    { verb: "adopt", command: "projects.adopt", enabled: (e) => (dsOf(e)?.state === "frozen" ? true : "Only frozen versions are adopted") },
    { verb: "archive", command: "versions.archive", enabled: (e) => (dsOf(e)?.state === "archived" ? "Archived already" : true) },
  ],
  facts: [
    { label: "Hours", value: (e) => n(dsOf(e)?.dataset.hours, 2) },
    { label: "Utterances", value: (e) => (dsOf(e) ? dsOf(e)!.dataset.utterances.toLocaleString() : "—") },
    { label: "Languages", value: (e) => dsOf(e)?.dataset.locales.join(", ") || "—" },
    { label: "Licence", value: (e) => dsOf(e)?.licence || "—" },
  ],
  comparable: true,
  draftable: false,
  loopStep: (e) => (dsOf(e)?.state === "draft" ? (freezing(dsOf(e)) ? "run" : "check") : "record"),
  nextStep: (e) => {
    const v = dsOf(e);
    if (v?.state === "draft") {
      return freezing(v)
        ? { step: "run", title: "The freeze is cutting the segments into the content store; the version turns frozen when it ends" }
        : { step: "check", title: "Preview hours per language and split, then freeze (leakage check, quality checks, card)", command: "datasets.freeze" };
    }
    if (v?.dataset.evalOnly) return { step: "record", title: "Eval-only: freeze it as a golden set (goldenSets.freeze) or adopt it to evaluate on" };
    return v?.usedBy.length
      ? { step: "record", title: "Mix it into a training run (Mix document), or compare it with another version" }
      : { step: "record", title: "Adopt it into the project so mixes and pipelines may use it (data.lock lists it)", command: "projects.adopt" };
  },
  icon: Database,
  live: {
    topics: (id) => [`entity.dataset_version.${id}`],
    patch: (qc: QueryClient, batch: CadenceEvent[], id: string) => {
      if (batch.length) void qc.invalidateQueries({ queryKey: datasetsGetQueryKey({ path: { id } }) });
    },
  },
  useData(id) {
    const q = useQuery({ ...datasetsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? datasetVersionToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};

// ---------------------------------------------------------------- source

export function sourceToEntity(s: Source): EntityData {
  return {
    id: s.id,
    name: s.name,
    version: s.kind,
    state: s.archived ? "archived" : "active",
    rev: s.rev,
    updatedAt: s.updatedAt,
    createdAt: s.createdAt,
    actor: s.clearedBy ?? s.createdBy,
    source: s,
  };
}

const srcOf = (e: EntityData) => e.source as Source | undefined;

export const sourceEntity: EntityManifest = {
  kind: "source",
  apiEntity: "sources",
  layer: "registry",
  template: "container",
  // Clearing for training is a person's decision (sources.edit; an agent's call waits for an approval); archiving is
  // the admin's. Ingests run from the project's pipelines/data-ingest.yaml.
  verbs: [
    { verb: "edit", primary: true, enabled: (e) => (srcOf(e)?.archived ? "An archived source cannot be edited" : true) },
    { verb: "archive", enabled: (e) => (srcOf(e)?.archived ? "Archived already" : true) },
  ],
  facts: [
    { label: "Licence", value: (e) => srcOf(e)?.licence || "—" },
    { label: "Training", value: (e) => (srcOf(e) ? (srcOf(e)!.trainingCleared ? "✓ cleared" : "eval-only") : "—") },
    { label: "Utterances", value: (e) => (srcOf(e) ? srcOf(e)!.utterances.toLocaleString() : "—") },
    { label: "Hours", value: (e) => n(srcOf(e)?.hours, 2) },
  ],
  comparable: false,
  draftable: false,
  loopStep: (e) => (srcOf(e)?.ingests?.length ? "record" : "prepare"),
  nextStep: (e) => {
    const s = srcOf(e);
    if (s?.archived) return { step: "record", title: "Archived: it takes no new ingests; its utterances and dataset versions stay" };
    if (!s?.ingests?.length) return { step: "prepare", title: "Ingest it: set source in pipelines/data-ingest.yaml and run the pipeline" };
    return s.trainingCleared
      ? { step: "record", title: "Cleared for training: freeze its drafts and mix them" }
      : { step: "decide", title: "Eval-only until a person clears it for training (check the licence first)", command: "sources.edit" };
  },
  icon: DatabaseScript,
  live: {
    topics: (id) => [`entity.source.${id}`],
    patch: (qc: QueryClient, batch: CadenceEvent[], id: string) => {
      if (batch.length) void qc.invalidateQueries({ queryKey: sourcesGetQueryKey({ path: { id } }) });
    },
  },
  useData(id) {
    const q = useQuery({ ...sourcesGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? sourceToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
