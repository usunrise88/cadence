import { useQuery, type QueryClient } from "@tanstack/react-query";
import { BookmarkBook, Language, Medal } from "iconoir-react";
import type { CadenceEvent, GoldenSetVersion, LanguagePack, ModelVersion } from "@/api/gen/types.gen";
import { goldenSetsGetOptions, goldenSetsGetQueryKey, langpacksGetOptions, modelsGetOptions, modelsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";
import { invalidateLangpacks } from "@/shell/evaluation/cache";
import { useShell } from "@/shell/state";

// Evaluation's registry kinds and the language pack (docs/spec/02 "Evaluation entities"; R21–R23): golden sets and
// model versions are immutable registry versions (ver_…) in golden-set/… and model/…; a language pack is the project
// repository's lang/<locale>/ directory, its id the locale.

const n = (v: number | undefined, d = 1) => (typeof v === "number" && Number.isFinite(v) ? String(Math.round(v * 10 ** d) / 10 ** d) : "—");

// ---------------------------------------------------------------- golden set

export function goldenSetToEntity(g: GoldenSetVersion): EntityData {
  return {
    id: g.id,
    name: g.name,
    version: g.version,
    state: g.state,
    updatedAt: g.updatedAt,
    createdAt: g.createdAt,
    actor: g.actor,
    goldenSet: g,
  };
}

const gsOf = (e: EntityData) => (e.goldenSet as GoldenSetVersion | undefined)?.goldenSet;

export const goldenSetEntity: EntityManifest = {
  kind: "golden_set",
  apiEntity: "goldenSets",
  layer: "registry",
  template: "registry",
  // Adopt brings it into the open project (the document's card dry-runs the leakage check first); freeze makes a new
  // golden set version from an eval-only dataset version (the form in the document).
  verbs: [
    { verb: "adopt", command: "projects.adopt", primary: true },
    { verb: "freeze" },
  ],
  facts: [
    { label: "Locale", value: (e) => gsOf(e)?.locale ?? "—" },
    { label: "Utterances", value: (e) => (gsOf(e) ? gsOf(e)!.utterances.toLocaleString() : "—") },
    { label: "Hours", value: (e) => n(gsOf(e)?.hours, 2) },
    { label: "Resampled by", value: (e) => gsOf(e)?.groups ?? "—" },
  ],
  comparable: false,
  draftable: false,
  loopStep: () => "record",
  nextStep: (e) =>
    (e.goldenSet as GoldenSetVersion | undefined)?.usedBy.length
      ? { step: "record", title: "Projects that adopted it evaluate on it; its utterances never reach a training mix" }
      : { step: "record", title: "Adopt it into a project (and name it in gates.yaml) so evals score on it", command: "projects.adopt" },
  icon: Medal,
  live: {
    topics: (id) => [`entity.golden_set.${id}`],
    patch: (qc: QueryClient, batch: CadenceEvent[], id: string) => {
      if (batch.length) void qc.invalidateQueries({ queryKey: goldenSetsGetQueryKey({ path: { id } }) });
    },
  },
  useData(id) {
    const q = useQuery({ ...goldenSetsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? goldenSetToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};

// ---------------------------------------------------------------- model version

export function modelToEntity(m: ModelVersion): EntityData {
  return {
    id: m.id,
    name: m.name,
    version: m.version,
    state: m.state,
    updatedAt: m.updatedAt,
    createdAt: m.createdAt,
    actor: m.actor,
    model: m,
  };
}

const modelOf = (e: EntityData) => (e.model as ModelVersion | undefined)?.model;

export const modelEntity: EntityManifest = {
  kind: "model",
  apiEntity: "models",
  layer: "registry",
  template: "registry",
  // Registering happens from a passed eval (models.register takes a checkpoint); Set as baseline points the project's
  // @baseline at it (aliases.set, always an approval); export and promote arrive in phase 5.
  verbs: [{ verb: "set", command: "aliases.set", primary: true }],
  facts: [
    { label: "Gate", value: (e) => (modelOf(e) ? (modelOf(e)!.gate.verdict === "passed" ? "✓ passed" : "✗ failed") : "—") },
    { label: "Family", value: (e) => modelOf(e)?.familyId ?? "—" },
    { label: "Checkpoint", value: (e) => modelOf(e)?.checkpointId ?? "—" },
    { label: "Used by", value: (e) => String((e.model as ModelVersion | undefined)?.usedBy.length ?? "—") },
  ],
  comparable: false,
  draftable: false,
  loopStep: () => "record",
  nextStep: () => ({ step: "record", title: "Evaluate other checkpoints against it, or set it as the project's baseline (an approval)", command: "aliases.set" }),
  icon: BookmarkBook,
  live: {
    topics: (id) => [`entity.model.${id}`],
    patch: (qc: QueryClient, batch: CadenceEvent[], id: string) => {
      if (batch.length) void qc.invalidateQueries({ queryKey: modelsGetQueryKey({ path: { id } }) });
    },
  },
  useData(id) {
    const q = useQuery({ ...modelsGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? modelToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};

// ---------------------------------------------------------------- language pack

export function languagePackToEntity(p: LanguagePack, project: string): EntityData {
  return {
    id: p.locale,
    name: `${p.locale} language pack`,
    // The branch the pack is read at, like a recipe file; an agent's drafted edit names its branch.
    state: p.branch ? "draft" : "active",
    version: p.sha.slice(0, 7),
    project,
    pack: p,
  };
}

const packOf = (e: EntityData) => e.pack as LanguagePack | undefined;

export const languagePackEntity: EntityManifest = {
  kind: "language_pack",
  apiEntity: "langpacks",
  layer: "project",
  template: "container",
  verbs: [{ verb: "edit", primary: true }],
  facts: [
    { label: "Commit", value: (e) => packOf(e)?.sha.slice(0, 7) ?? "—" },
    { label: "Files", value: (e) => String(packOf(e)?.files.length ?? "—") },
    { label: "Boost lists", value: (e) => String(packOf(e)?.boost.length ?? "—") },
    { label: "Scoring", value: (e) => packOf(e)?.scoring.normalizer ?? "—" },
  ],
  comparable: false,
  draftable: false,
  loopStep: () => "prepare",
  nextStep: (e) =>
    packOf(e)?.issues.length
      ? { step: "prepare", title: "Some files do not check out: fix them in the editor", command: "langpacks.edit" }
      : { step: "prepare", title: "Edit a boost list, then evaluate with it as a decoding variant (evals.new)" },
  icon: Language,
  live: { topics: () => ["recipe.*"], patch: (qc: QueryClient, batch: CadenceEvent[]) => {
      if (batch.length > 0) invalidateLangpacks(qc);
    },
  },
  useData(id) {
    const project = useShell((s) => s.project) ?? "";
    const q = useQuery({ ...langpacksGetOptions({ path: { p: project, locale: id } }), enabled: !!id && !!project });
    return { data: q.data ? languagePackToEntity(q.data, project) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
