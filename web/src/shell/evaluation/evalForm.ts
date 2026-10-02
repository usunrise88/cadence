import type { Eval, EvalNew, EvalPlan, EvalSubjectRef, LanguagePack, RunDeparture } from "@/api/gen/types.gen";

// The Run eval form's state (docs/spec/11-ui-panels.md "Commands": Run eval matrix; R20–R24, R43): what a person
// picks on each axis of evals.new. Anything left at its default stays out of the request body, so the server's
// defaults (gates.yaml's golden sets, eval.matrix_profiles, boost none, augmentation none, @baseline) apply and the
// plan says what they resolved to.

export type BoostChoice = { ref: string; weight: string };

export type EvalFormState = {
  /** Golden set versions (ver_…); empty: the default (gates.yaml target and replay, else every adopted set). */
  goldenSets: string[];
  /** Latency profile names; empty: the default. */
  profiles: string[];
  /** Decode without boosting too (always on when no boost list is picked). */
  none: boolean;
  /** Boost lists, lang/<locale>/boost/<file>.txt@<commit>, with a weight ("" = the list's own). */
  boosts: BoostChoice[];
  /** Augmentation profiles, augment/<name>.yaml@<commit>; none is always included. */
  augmentations: string[];
  /** Golden-set locale → the language both models decode it in. */
  languages: [string, string][];
  /** ver_…, @alias, base-model/<name> or model/<name>; empty: @baseline, else the project's base model. */
  baseline: string;
};

export function emptyEvalForm(): EvalFormState {
  return { goldenSets: [], profiles: [], none: true, boosts: [], augmentations: [], languages: [], baseline: "" };
}

/** The subject reference of an eval's subject model (to evaluate it again). */
export function subjectRefOf(e: Pick<Eval, "subject">): EvalSubjectRef {
  const s = e.subject;
  return s.kind === "checkpoint" ? { checkpointId: s.id } : s.kind === "model" ? { modelVersionId: s.id } : { baseModelVersionId: s.id };
}

/**
 * The form an eval's own axes fill, to re-run it: the cells already scored come back cached (their eval records are
 * keyed by model and decoding hash), so only the missing ones compute.
 */
export function formFromEval(e: Eval): EvalFormState {
  const boosts = e.decoding.filter((d) => d.boost !== "none").map((d) => ({ ref: d.boost, weight: d.weight !== undefined ? String(d.weight) : "" }));
  const languages: [string, string][] = [];
  for (const g of e.goldenSets) {
    if (g.decodeAs && g.decodeAs !== g.locale && !languages.some(([l]) => l === g.locale)) languages.push([g.locale, g.decodeAs]);
  }
  return {
    goldenSets: e.goldenSets.map((g) => g.versionId),
    profiles: e.profiles.map((p) => p.name),
    none: e.decoding.some((d) => d.boost === "none") || boosts.length === 0,
    boosts,
    augmentations: (e.augmentations ?? []).filter((a) => a.profile !== "none").map((a) => a.profile),
    languages,
    baseline: e.baseline.id,
  };
}

/**
 * The languages map a run trained under a neighbour's prompt needs (evals.new languages): when the train step ran with
 * target_lang, each of the project's locales that differs from it is decoded as target_lang.
 */
export function languagesForRun(locales: string[], departures: Pick<RunDeparture, "param" | "value">[] | undefined): [string, string][] {
  const t = departures?.find((d) => d.param === "target_lang" && typeof d.value === "string" && d.value)?.value as string | undefined;
  if (!t) return [];
  return locales.filter((l) => l !== t).map((l) => [l, t]);
}

/** A boost list of a language pack as evals.new names it: the file at the last commit that changed the pack. */
export function boostRef(pack: Pick<LanguagePack, "path" | "sha">, listPath: string): string {
  return `${pack.path}/${listPath}@${pack.sha}`;
}

/** An augmentation profile file of the repository at a commit. */
export function augmentRef(path: string, commit: string): string {
  return `${path}@${commit}`;
}

/** The request body; only the axes that depart from the defaults are sent. */
export function evalBody(subject: EvalSubjectRef, f: EvalFormState): EvalNew {
  const body: EvalNew = { subject };
  if (f.goldenSets.length) body.goldenSets = [...f.goldenSets];
  if (f.profiles.length) body.profiles = [...f.profiles];
  if (f.boosts.length) {
    const lists = f.boosts.map((b) => {
      const w = Number(b.weight);
      return b.weight.trim() !== "" && Number.isFinite(w) ? { boost: b.ref, weight: w } : { boost: b.ref };
    });
    body.decoding = f.none ? [{ boost: "none" }, ...lists] : lists;
  }
  if (f.augmentations.length) body.augmentations = [{ profile: "none" }, ...f.augmentations.map((profile) => ({ profile }))];
  const languages = f.languages.filter(([l, d]) => l.trim() && d.trim());
  if (languages.length) body.languages = Object.fromEntries(languages.map(([l, d]) => [l.trim(), d.trim()]));
  if (f.baseline.trim()) body.baseline = f.baseline.trim();
  return body;
}

/** Field errors the form can tell before asking the server. */
export function formProblems(f: EvalFormState): string[] {
  const out: string[] = [];
  for (const b of f.boosts) {
    if (b.weight.trim() === "") continue;
    const w = Number(b.weight);
    if (!Number.isFinite(w) || w <= 0) out.push(`The weight of ${shortBoost(b.ref)} must be a positive number`);
  }
  const seen = new Set<string>();
  for (const [l, d] of f.languages) {
    if (!l.trim() && !d.trim()) continue;
    if (!l.trim() || !d.trim()) out.push("A language row needs both the golden-set locale and the language to decode it in");
    else if (seen.has(l.trim())) out.push(`${l.trim()} is mapped twice`);
    seen.add(l.trim());
  }
  return out;
}

/** lang/he-IL/boost/banking.txt@abc1234… → he-IL banking. */
export function shortBoost(ref: string): string {
  const m = /^lang\/([^/]+)\/boost\/(.+?)\.txt(?:@([0-9a-f]{7}))?/.exec(ref);
  return m ? `${m[1]} ${m[2]}` : ref;
}

/** augment/telephony.yaml@abc… → telephony. */
export function shortAugment(ref: string): string {
  const m = /^augment\/(.+?)\.ya?ml/.exec(ref);
  return m ? m[1]! : ref;
}

/** One line for a plan: the matrix, what is cached and what the rest costs. */
export function planLine(p: EvalPlan): string {
  const s = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  const axes = [s(p.goldenSets.length, "golden set"), s(p.profiles.length, "profile")];
  if (p.decoding.length > 1) axes.push(s(p.decoding.length, "decoding variant"));
  if ((p.augmentations?.length ?? 1) > 1) axes.push(s(p.augmentations!.length, "augmentation"));
  const gpu = p.estimate.gpuHours < 0.01 && p.cellsToCompute > 0 ? "<0.01" : p.estimate.gpuHours.toFixed(2);
  return `${axes.join(" × ")} against ${p.baseline.label}: ${s(p.cells.length, "cell")}, ${p.cellsCached} cached, ${p.cellsToCompute} to compute · ~${gpu} GPU-h (${p.estimate.audioHours.toFixed(2)} h of audio)`;
}
