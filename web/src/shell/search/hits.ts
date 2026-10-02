import type { SearchGroup, SearchHit, SearchQualifier } from "@/api/gen/types.gen";
import type { EntityData } from "@/shell/entity/manifest";

// Pure helpers over projects.search results (docs/spec/11-ui-panels.md "Search"): group headings, the entity data a
// hit previews as, and the chips a parsed query renders.

const KIND_LABELS: Record<string, string> = {
  project: "Projects",
  base_model: "Base models",
  dataset_version: "Dataset versions",
  template: "Templates",
  registry_collection: "Registry collections",
  job: "Jobs",
  approval: "Approvals",
  help_article: "Help",
  saved_search: "Saved searches",
  run: "Runs",
  eval: "Evals",
  golden_set: "Golden sets",
  normalizer: "Normalizers",
  model: "Models",
  language_pack: "Language packs",
  mix: "Mixes",
  agent_session: "Agent sessions",
};

/** The heading of a result group: a readable plural for known kinds, the kind itself otherwise. */
export function kindLabel(kind: string): string {
  const known = KIND_LABELS[kind];
  if (known) return known;
  const words = kind.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1) + (words.endsWith("s") ? "" : "s");
}

/** The singular noun of a kind for one row ("dataset version"). */
export function kindNoun(kind: string): string {
  return kind.replace(/_/g, " ");
}

/** What the Inspector shows for a hit whose kind has no entity manifest. */
export function hitToEntity(hit: SearchHit): EntityData {
  const out: EntityData = {
    id: hit.id,
    name: hit.title,
    kind: hit.kind,
    state: hit.status ?? "—",
    updatedAt: hit.updatedAt,
  };
  if (hit.project) out.project = hit.project;
  if (hit.lang) out.lang = hit.lang;
  if (hit.tags.length > 0) out.tags = hit.tags.join(", ");
  if (hit.actor) out.actor = hit.actor;
  if (hit.snippet) out.snippet = hit.snippet;
  for (const [k, v] of Object.entries(hit.numbers ?? {})) out[k] = v;
  return out;
}

/** The hits of every group, in group order (the order the server ranked them). */
export function flattenGroups(groups: SearchGroup[]): SearchHit[] {
  return groups.flatMap((g) => g.items);
}

/** A qualifier as its chip reads: `kind: dataset_version`, `wer < 10`, `updated > 2026-09-01`. */
export function chipLabel(q: SearchQualifier): string {
  return q.op === ":" ? `${q.field}: ${q.value}` : `${q.field} ${q.op} ${q.value}`;
}

/** The `scope:` qualifier value in a query, if any. */
export function scopeOf(query: string): string | undefined {
  const m = /(?:^|\s)scope:(\S+)/.exec(query);
  return m?.[1];
}

/** Sets (or, with undefined, removes) the `scope:` qualifier of a query, keeping everything else. */
export function withScope(query: string, scope: string | undefined): string {
  const rest = query
    .replace(/(?:^|\s)scope:\S+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!scope) return rest;
  return rest ? `${rest} scope:${scope}` : `scope:${scope}`;
}
