import type { Eval, EvalCell, EvalDecoding, EvalGateCheck, EvalGoldenSet, EvalProfile, EvalSummary } from "@/api/gen/types.gen";
import type { BarSpec, ForestSpec, HeatmapSpec } from "@/shell/charts";
import { deltaTone, type DeltaTone, type GateState } from "@/shell/panel";

// The Eval report's pure model (docs/spec/11-ui-panels.md "Eval report"; R53): the matrix of golden sets × latency
// profiles at one decoding variant, and the chart specs built from evals.get. Every number comes from the API
// (summaries, deltas with intervals, duration buckets); nothing is recomputed here except rates from counts.

export type MatrixCell = {
  goldenSet: EvalGoldenSet;
  profile: EvalProfile;
  subject?: EvalCell;
  baseline?: EvalCell;
  /** The cell the gate reads (R20): the primary profile at decoding 0. */
  primary: boolean;
  tone: DeltaTone;
  /** The gate checks about this cell, worst first (failed › inconclusive › passed). */
  gate?: GateState;
  checks: EvalGateCheck[];
};

export type Matrix = {
  rows: EvalGoldenSet[];
  cols: EvalProfile[];
  /** cells[row][col]; undefined where the eval has no subject cell (replay sets run at the primary profile only). */
  cells: (MatrixCell | undefined)[][];
  decoding: number;
};

const STATE_RANK: Record<GateState, number> = { failed: 0, inconclusive: 1, passed: 2 };

function checksFor(e: Eval, gs: EvalGoldenSet, profile: string): EvalGateCheck[] {
  return (e.gate?.checks ?? [])
    .filter((c) => (c.goldenSetVersionId === gs.versionId || (!c.goldenSetVersionId && c.goldenSet === gs.name)) && (c.profile ?? e.primaryProfile) === profile)
    .sort((a, b) => STATE_RANK[a.state] - STATE_RANK[b.state]);
}

/** The golden sets × profiles matrix of one decoding variant. */
export function buildMatrix(e: Eval, decoding = 0): Matrix {
  const cells = e.cells ?? [];
  const find = (role: EvalCell["role"], gs: string, profile: string) =>
    cells.find((c) => c.role === role && c.goldenSetVersionId === gs && c.profile === profile && c.decodingIndex === decoding);
  return {
    rows: e.goldenSets,
    cols: e.profiles,
    decoding,
    cells: e.goldenSets.map((gs) =>
      e.profiles.map((p) => {
        const subject = find("subject", gs.versionId, p.name);
        const baseline = find("baseline", gs.versionId, p.name);
        if (!subject && !baseline) return undefined;
        const checks = decoding === 0 ? checksFor(e, gs, p.name) : [];
        return {
          goldenSet: gs,
          profile: p,
          subject,
          baseline,
          primary: p.name === e.primaryProfile && decoding === 0,
          tone: deltaTone(subject?.delta?.wer),
          gate: checks[0]?.state,
          checks,
        };
      }),
    ),
  };
}

/** The cell a report opens on: the first golden set's primary cell, else any subject cell. */
export function defaultCell(e: Eval): EvalCell | undefined {
  const subjects = (e.cells ?? []).filter((c) => c.role === "subject");
  const gs = e.goldenSets[0]?.versionId;
  return subjects.find((c) => c.goldenSetVersionId === gs && c.profile === e.primaryProfile && c.decodingIndex === 0) ?? subjects[0];
}

/** The baseline cell paired with a subject cell (same golden set, profile and decoding). */
export function baselineOf(e: Eval, cell: EvalCell): EvalCell | undefined {
  return (e.cells ?? []).find((c) => c.role === "baseline" && c.goldenSetVersionId === cell.goldenSetVersionId && c.profile === cell.profile && c.decodingIndex === cell.decodingIndex);
}

export function profileLabel(p: EvalProfile | undefined, name?: string): string {
  if (!p) return name ?? "—";
  return p.label ? `${p.name} (${p.label})` : p.name;
}

export function decodingLabel(d: EvalDecoding | undefined): string {
  if (!d || d.boost === "none") return "no boosting";
  const file = d.boost.replace(/^lang\/[^/]+\/boost\//, "").replace(/@([0-9a-f]{7})[0-9a-f]*$/, " @$1");
  return `boost ${file}${d.weight !== undefined ? ` × ${d.weight}` : ""}${d.terms !== undefined ? ` (${d.terms} terms)` : ""}`;
}

/** "fleurs-he · 160ms" (+ the decoding when there are several). */
export function cellTitle(e: Eval, c: Pick<EvalCell, "goldenSetVersionId" | "profile" | "decodingIndex">): string {
  const gs = e.goldenSets.find((g) => g.versionId === c.goldenSetVersionId);
  const name = gs ? shortName(gs.name) : c.goldenSetVersionId;
  const dec = e.decoding.length > 1 ? ` · ${decodingLabel(e.decoding.find((d) => d.index === c.decodingIndex))}` : "";
  return `${name} · ${c.profile}${dec}`;
}

/** golden-set/fleurs-he → fleurs-he. */
export function shortName(collection: string): string {
  return collection.replace(/^golden-set\//, "");
}

const pp = (v: number) => Math.round(v * 10000) / 100; // fraction → percentage points, 2 decimals
const ppFormat = (v: number) => `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v).toFixed(2)}`;
const pctFormat = (v: number) => v.toFixed(1);

/** Heatmap of WER deltas (subject − baseline, percentage points): diverging around 0, values printed in cells. */
export function deltaHeatmap(m: Matrix): HeatmapSpec {
  return {
    kind: "heatmap",
    title: "WER delta against the baseline",
    xLabel: "Latency profile",
    yLabel: "Golden set",
    unit: "pp",
    format: ppFormat,
    x: m.cols.map((p) => (m.cells.some((r) => r[m.cols.indexOf(p)]?.primary) ? `${p.name} ★` : p.name)),
    y: m.rows.map((g) => shortName(g.name)),
    colormap: "diverging",
    center: 0,
    cells: m.cells.flatMap((row, y) => row.map((c, x) => ({ x, y, value: c?.subject?.delta?.wer ? pp(c.subject.delta.wer.value) : null }))),
  };
}

/** Forest plot: each subject cell's WER delta with its interval (percentage points), reference line at 0. */
export function deltaForest(e: Eval, decoding = 0): ForestSpec {
  const order = new Map(e.goldenSets.map((g, i) => [g.versionId, i]));
  const profiles = new Map(e.profiles.map((p, i) => [p.name, i]));
  const rows = (e.cells ?? [])
    .filter((c) => c.role === "subject" && c.decodingIndex === decoding && c.delta && !c.delta.error)
    .sort((a, b) => (order.get(a.goldenSetVersionId) ?? 0) - (order.get(b.goldenSetVersionId) ?? 0) || (profiles.get(a.profile) ?? 0) - (profiles.get(b.profile) ?? 0))
    .map((c) => ({
      label: `${cellTitle(e, { ...c, decodingIndex: 0 })}${c.profile === e.primaryProfile ? " ★" : ""}`,
      estimate: pp(c.delta!.wer.value),
      low: pp(c.delta!.wer.low),
      high: pp(c.delta!.wer.high),
    }));
  return { kind: "forest", title: "WER delta with 95 % interval", xLabel: "Subject − baseline (pp); left of 0 is better", unit: "pp", format: ppFormat, rows, reference: 0 };
}

const rate = (count: number, s: EvalSummary) => (s.refWords > 0 ? Math.round((count / s.refWords) * 10000) / 100 : null);

/** Substitution, deletion and insertion rates (% of reference words) of subject and baseline at the primary profile. */
export function sdiBars(e: Eval, decoding = 0): BarSpec {
  const categories: string[] = [];
  const s: (number | null)[] = [];
  const d: (number | null)[] = [];
  const ins: (number | null)[] = [];
  for (const gs of e.goldenSets) {
    for (const role of ["subject", "baseline"] as const) {
      const c = (e.cells ?? []).find((x) => x.role === role && x.goldenSetVersionId === gs.versionId && x.profile === e.primaryProfile && x.decodingIndex === decoding);
      if (!c?.summary) continue;
      categories.push(`${shortName(gs.name)} · ${role}`);
      s.push(rate(c.summary.sub, c.summary));
      d.push(rate(c.summary.del, c.summary));
      ins.push(rate(c.summary.ins, c.summary));
    }
  }
  return {
    kind: "bar",
    title: `Substitutions, deletions and insertions at ${e.primaryProfile}`,
    yLabel: "% of reference words",
    unit: "%",
    format: pctFormat,
    stacked: true,
    horizontal: true,
    categories,
    series: [
      { id: "sub", label: "Substitutions", slot: 0, values: s },
      { id: "del", label: "Deletions", slot: 1, values: d },
      { id: "ins", label: "Insertions", slot: 2, values: ins },
    ],
  };
}

export function bucketLabel(b: { lo: number; hi?: number }): string {
  return b.hi === undefined ? `${b.lo}+ s` : `${b.lo}–${b.hi} s`;
}

/** WER by utterance duration for one cell, subject against baseline (the scores' buckets). */
export function bucketBars(title: string, subject?: EvalSummary, baseline?: EvalSummary): BarSpec {
  const buckets = subject?.buckets ?? baseline?.buckets ?? [];
  const key = (b: { lo: number; hi?: number }) => `${b.lo}:${b.hi ?? ""}`;
  const pick = (s: EvalSummary | undefined) => {
    const by = new Map((s?.buckets ?? []).map((b) => [key(b), b]));
    return buckets.map((b) => {
      const x = by.get(key(b));
      return x && x.utterances > 0 ? Math.round(x.wer * 10000) / 100 : null;
    });
  };
  return {
    kind: "bar",
    title: `WER by duration · ${title}`,
    xLabel: "Utterance duration",
    yLabel: "WER %",
    unit: "%",
    format: pctFormat,
    categories: buckets.map((b) => `${bucketLabel(b)} (${b.utterances})`),
    series: [
      { id: "subject", label: "Subject", slot: 0, values: pick(subject) },
      { id: "baseline", label: "Baseline", slot: 1, values: pick(baseline) },
    ],
  };
}

/** Progress as a fraction (0–1) for the progress bar. */
export function progressOf(e: Pick<Eval, "progress">): number {
  return e.progress.cellsTotal > 0 ? e.progress.cellsDone / e.progress.cellsTotal : 0;
}
