import { useQuery, type QueryClient } from "@tanstack/react-query";
import { PercentageCircle } from "iconoir-react";
import type { CadenceEvent, Mix, Presence } from "@/api/gen/types.gen";
import { mixesGetOptions, mixesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { activePresence, presenceLabel } from "@/shell/entity/drafts";
import type { EntityData, EntityManifest } from "@/shell/entity/manifest";

// Mix (docs/spec/08-resolutions.md R13): project work with revisions, draftable. The header's actor is whoever the
// current revision comes from — the agent whose draft a person accepted shows as the agent badge.

export function mixToEntity(m: Mix): EntityData {
  const agent = m.cause?.draftAuthor ?? (m.updatedBy.kind === "agent" ? m.updatedBy : undefined);
  return {
    id: m.id,
    name: m.name,
    state: "active",
    rev: m.rev,
    updatedAt: m.updatedAt,
    createdAt: m.createdAt,
    description: m.description,
    projectId: m.projectId,
    actor: agent ?? m.updatedBy,
    toolCallId: agent ? m.cause?.toolCallId : undefined,
    presence: m.presence,
    mix: m,
  };
}

const pct = (x: number) => `${Math.round(x * 1000) / 10} %`;

/** Patches the open mix from entity.mix.{id}: revisions carry the whole mix, presence.changed the presence list. */
export function patchMix(qc: QueryClient, batch: CadenceEvent[], id: string): void {
  const key = mixesGetQueryKey({ path: { id } });
  let listStale = false;
  for (const e of batch) {
    const payload = e.payload as { mix?: Mix; presence?: Presence[] } | undefined;
    if (payload?.mix) {
      qc.setQueryData<Mix>(key, payload.mix);
      listStale = true;
    } else if (e.type === "presence.changed" && payload?.presence) {
      const presence = payload.presence;
      qc.setQueryData<Mix>(key, (old) => (old ? { ...old, presence } : old));
    }
  }
  if (listStale) void qc.invalidateQueries({ queryKey: [{ _id: "mixesList" }] });
}

export const mixEntity: EntityManifest = {
  kind: "mix",
  apiEntity: "mixes",
  layer: "project",
  template: "container",
  verbs: [
    {
      verb: "edit",
      primary: true,
      enabled: (e) => {
        const editing = activePresence(e.presence);
        return editing.length > 0 ? `${presenceLabel(editing)} is editing this mix — accept or revert its draft first` : true;
      },
    },
  ],
  facts: [
    { label: "Revision", value: (e) => `rev ${e.rev ?? "—"}` },
    { label: "Groups", value: (e) => String((e.mix as Mix | undefined)?.groups.length ?? "—") },
    { label: "Train hours", value: (e) => `${(e.mix as Mix | undefined)?.preview.totalHours ?? "—"} h` },
    {
      label: "Temp · replay",
      value: (e) => {
        const m = e.mix as Mix | undefined;
        return m ? `${m.temperature} · ${pct(m.replayShare)}` : "—";
      },
    },
  ],
  comparable: false,
  draftable: true,
  loopStep: () => "prepare",
  nextStep: (e) =>
    activePresence(e.presence).some((p) => p.draftId)
      ? { step: "decide", title: "An agent drafted a change to this mix — accept or revert it on the draft's outline" }
      : { step: "check", title: "Check the hours per language, then dry-run a training run with this mix (training arrives in phase 2)" },
  icon: PercentageCircle,
  live: { topics: (id) => [`entity.mix.${id}`], patch: patchMix },
  useData(id) {
    const q = useQuery({ ...mixesGetOptions({ path: { id } }), enabled: !!id });
    return { data: q.data ? mixToEntity(q.data) : undefined, error: q.error, isLoading: q.isLoading };
  },
};
