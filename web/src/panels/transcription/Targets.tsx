import { useQuery } from "@tanstack/react-query";
import { Plus, Xmark } from "iconoir-react";
import {
  baseModelsListOptions,
  checkpointsListOptions,
  langpacksListOptions,
  modelFamiliesListOptions,
  modelsListOptions,
  projectsGetOptions,
} from "@/api/gen/@tanstack/react-query.gen";
import type { LatencyProfile, TranscriptionTargetIn } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";

// The targets of a transcription test (R47): one to three of a project checkpoint, a model version or a base model,
// each with its latency profile, language and boost list. Empty profile and language mean the server's defaults
// (eval.primary_profile when the family declares it; the project's first locale).

export type TargetKind = "checkpoint" | "model" | "base_model";

export type TargetForm = { kind: TargetKind; id: string; profile: string; language: string; boost: string };

export const MAX_TARGETS = 3;

export function emptyTarget(kind: TargetKind = "base_model"): TargetForm {
  return { kind, id: "", profile: "", language: "", boost: "none" };
}

/** The request body's target of a form row; undefined while it names no model. */
export function targetBody(t: TargetForm): TranscriptionTargetIn | undefined {
  if (!t.id) return undefined;
  const body: TranscriptionTargetIn =
    t.kind === "checkpoint" ? { checkpointId: t.id } : t.kind === "model" ? { modelVersionId: t.id } : { baseModelVersionId: t.id };
  if (t.profile) body.profile = t.profile;
  if (t.language) body.language = t.language;
  if (t.boost && t.boost !== "none") body.boost = t.boost;
  return body;
}

type Option = { id: string; label: string; family?: string };

function useModelOptions(project: string) {
  const ckp = useQuery({ ...checkpointsListOptions({ path: { p: project }, query: { kept: true, limit: 100 } }), enabled: !!project });
  const models = useQuery(modelsListOptions());
  const bases = useQuery(baseModelsListOptions());
  const families = useQuery(modelFamiliesListOptions());
  const options: Record<TargetKind, Option[]> = {
    checkpoint: (ckp.data?.items ?? []).map((c) => ({
      id: c.id,
      label: `${c.runId}${c.step !== undefined ? ` step ${c.step}` : ""}${c.valWer !== undefined ? ` · val WER ${(c.valWer * 100).toFixed(1)} %` : ""}`,
      family: c.family,
    })),
    model: (models.data?.items ?? []).map((m) => ({ id: m.id, label: `${m.name} ${m.version}`, family: m.model.familyId })),
    base_model: (bases.data?.items ?? []).map((b) => ({ id: b.id, label: `${b.name} ${b.version}`, family: b.baseModel.familyId })),
  };
  const profiles = (family: string | undefined): LatencyProfile[] => {
    const all = families.data?.items ?? [];
    const f = all.find((v) => v.modelFamily.name === family) ?? (family ? undefined : all[0]);
    return f?.modelFamily.latencyProfiles ?? [];
  };
  return { options, profiles };
}

export function TargetsForm({ project, targets, onChange, disabled }: { project: string; targets: TargetForm[]; onChange: (t: TargetForm[]) => void; disabled?: boolean }) {
  const { options, profiles } = useModelOptions(project);
  const proj = useQuery({ ...projectsGetOptions({ path: { p: project } }), enabled: !!project });
  const packs = useQuery({ ...langpacksListOptions({ path: { p: project } }), enabled: !!project });
  const locales = proj.data?.locales ?? [];
  const set = (i: number, patch: Partial<TargetForm>) => onChange(targets.map((t, k) => (k === i ? { ...t, ...patch } : t)));

  return (
    <div className="flex flex-col gap-1.5" data-slot="transcription-targets">
      {targets.map((t, i) => {
        const opts = options[t.kind];
        const family = opts.find((o) => o.id === t.id)?.family;
        const profs = profiles(family);
        const lang = t.language || locales[0] || "";
        const pack = (packs.data?.items ?? []).find((p) => p.locale === lang);
        const lane = "ABC"[i] ?? "?";
        return (
          <div key={i} className="flex flex-wrap items-center gap-1.5 text-xs" data-slot="transcription-target">
            <span className="w-4 font-medium text-muted-foreground" aria-hidden>
              {lane}
            </span>
            <NativeSelect
              className="h-7 w-28"
              aria-label={`Target ${i + 1}: kind`}
              value={t.kind}
              disabled={disabled}
              onChange={(e) => set(i, { kind: e.target.value as TargetKind, id: "", profile: "" })}
            >
              <option value="base_model">Base model</option>
              <option value="model">Model version</option>
              <option value="checkpoint">Checkpoint</option>
            </NativeSelect>
            <NativeSelect className="h-7 min-w-48 flex-1" aria-label={`Target ${i + 1}: model`} value={t.id} disabled={disabled} onChange={(e) => set(i, { id: e.target.value, profile: "" })}>
              <option value="">{opts.length ? "Choose…" : "None available"}</option>
              {opts.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </NativeSelect>
            <NativeSelect className="h-7 w-32" aria-label={`Target ${i + 1}: latency profile`} value={t.profile} disabled={disabled} onChange={(e) => set(i, { profile: e.target.value })}>
              <option value="">Primary profile</option>
              {profs
                .filter((p) => p.latencyMs > 0)
                .map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.label ?? p.name}
                  </option>
                ))}
            </NativeSelect>
            <NativeSelect className="h-7 w-24" aria-label={`Target ${i + 1}: language`} value={t.language} disabled={disabled} onChange={(e) => set(i, { language: e.target.value, boost: "none" })}>
              <option value="">{locales[0] ? `${locales[0]} (project)` : "Project's"}</option>
              {locales.map((l) => (
                <option key={l} value={l}>
                  {l}
                </option>
              ))}
            </NativeSelect>
            <NativeSelect className="h-7 w-36" aria-label={`Target ${i + 1}: boost list`} value={t.boost} disabled={disabled} onChange={(e) => set(i, { boost: e.target.value })}>
              <option value="none">No boosting</option>
              {(pack?.boost ?? []).map((d) => (
                <option key={d} value={`lang/${pack!.locale}/boost/${d}.txt`}>
                  Boost: {d}
                </option>
              ))}
            </NativeSelect>
            <Button size="icon-xs" variant="ghost" aria-label={`Remove target ${lane}`} disabled={disabled || targets.length <= 1} onClick={() => onChange(targets.filter((_, k) => k !== i))}>
              <Xmark aria-hidden />
            </Button>
          </div>
        );
      })}
      <div>
        <Button
          size="xs"
          variant="ghost"
          disabled={disabled || targets.length >= MAX_TARGETS}
          onClick={() => onChange([...targets, { ...(targets[targets.length - 1] ?? emptyTarget()), profile: "" }])}
          title="The same model at 1120ms beside its deployment profile shows the gap to the high-latency reference"
        >
          <Plus aria-hidden />
          Add a target
        </Button>
      </div>
    </div>
  );
}
