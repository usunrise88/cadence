import type { Eval, EvalCell, EvalDecoding, EvalEntityScores, EvalGateCheck, EvalGoldenSet, EvalLatencyScores, EvalProfile, EvalSummary, EvalUtterance } from "@/api/gen/types.gen";
import type { BarSpec, ForestSpec, HeatmapSpec, LinePoint, LineSpec } from "@/shell/charts";
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

/** Augmentation 0 is "none": the matrix, the gate and the per-profile charts read it; the rest is the robustness axis. */
const plain = (c: EvalCell) => (c.augmentationIndex ?? 0) === 0;

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
    cells.find((c) => c.role === role && c.goldenSetVersionId === gs && c.profile === profile && c.decodingIndex === decoding && plain(c));
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
  const subjects = (e.cells ?? []).filter((c) => c.role === "subject" && plain(c));
  const gs = e.goldenSets[0]?.versionId;
  return subjects.find((c) => c.goldenSetVersionId === gs && c.profile === e.primaryProfile && c.decodingIndex === 0) ?? subjects[0];
}

/** The baseline cell paired with a subject cell (same golden set, profile, decoding and augmentation). */
export function baselineOf(e: Eval, cell: EvalCell): EvalCell | undefined {
  const aug = cell.augmentationIndex ?? 0;
  return (e.cells ?? []).find(
    (c) => c.role === "baseline" && c.goldenSetVersionId === cell.goldenSetVersionId && c.profile === cell.profile && c.decodingIndex === cell.decodingIndex && (c.augmentationIndex ?? 0) === aug,
  );
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
    .filter((c) => c.role === "subject" && c.decodingIndex === decoding && plain(c) && c.delta && !c.delta.error)
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
      const c = (e.cells ?? []).find((x) => x.role === role && x.goldenSetVersionId === gs.versionId && x.profile === e.primaryProfile && x.decodingIndex === decoding && plain(x));
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

// ---------------------------------------------------------------- per-utterance WER ECDF

/** evals.get answers at most this many worst utterances per cell (the contract's maximum for ?worst=). */
export const ECDF_ROWS = 200;

/** The empirical CDF of per-utterance WER (percent) as step points: (0, 0), then the share (percent) at or below each value. */
export function ecdfPoints(wers: number[]): LinePoint[] {
  const v = wers.map((w) => Math.round(w * 10000) / 100).sort((a, b) => a - b);
  const out: LinePoint[] = [{ x: 0, y: 0 }];
  v.forEach((x, i) => {
    if (v[i + 1] === x) return;
    out.push({ x, y: Math.round(((i + 1) / v.length) * 10000) / 100 });
  });
  return out;
}

function median(sorted: number[]): number {
  const n = sorted.length;
  return n % 2 ? sorted[(n - 1) / 2]! : (sorted[n / 2 - 1]! + sorted[n / 2]!) / 2;
}

/** One side of the ECDF: the rows evals.get returned for a cell and how many utterances the cell scored. */
export type EcdfSide = { rows: EvalUtterance[]; total: number };

/** Whether the rows are every utterance of the cell (the golden set has at most ECDF_ROWS utterances). */
export const ecdfComplete = (s: EcdfSide) => s.rows.length >= s.total;

/**
 * Per-utterance WER ECDF of a cell against its baseline cell. evals.get gives per-utterance rows only as the worst N
 * (N ≤ 200): for a golden set of at most 200 utterances that is every row and the curve is the whole distribution;
 * otherwise it is the tail (the N worst of each model) and the labels and the summary say so.
 */
export function werEcdf(title: string, subject?: EcdfSide, baseline?: EcdfSide): LineSpec {
  const sides = [
    { id: "subject", label: "Subject", slot: 0, side: subject },
    { id: "baseline", label: "Baseline", slot: 1, side: baseline },
  ].filter((x): x is { id: string; label: string; slot: number; side: EcdfSide } => !!x.side && x.side.rows.length > 0);
  const complete = sides.every((x) => ecdfComplete(x.side));
  const pct = (w: number) => `${(Math.round(w * 1000) / 10).toFixed(1)} %`;
  const notes = sides.map(({ label, side }) => {
    const sorted = side.rows.map((r) => r.wer).sort((a, b) => a - b);
    const zero = side.rows.filter((r) => r.errors === 0).length;
    return ecdfComplete(side)
      ? `${label}: median ${pct(median(sorted))}, ${Math.round((zero / side.rows.length) * 100)} % without errors.`
      : `${label}: the ${side.rows.length} worst of ${side.total.toLocaleString()} utterances (evals.get returns at most ${ECDF_ROWS}); the curve is that tail, not the whole set.`;
  });
  return {
    kind: "line",
    title: `Per-utterance WER${complete ? "" : " (worst rows only)"} · ${title}`,
    xLabel: "Utterance WER %",
    yLabel: complete ? "Utterances at or below (%)" : "Of the rows returned (%)",
    unit: "%",
    format: (v: number) => v.toFixed(1),
    yRange: [0, 100],
    note: notes.join(" "),
    series: sides.map(({ id, label, slot, side }) => ({
      id,
      label: ecdfComplete(side) ? `${label} (${side.rows.length})` : `${label} (worst ${side.rows.length} of ${side.total.toLocaleString()})`,
      slot,
      step: true,
      points: ecdfPoints(side.rows.map((r) => r.wer)),
    })),
  };
}

// ---------------------------------------------------------------- streaming: WER against latency, latency, stability

export type ProfileRow = { profile: EvalProfile; primary: boolean; subject?: EvalCell; baseline?: EvalCell };

/** The subject and baseline cells (augmentation none) of one golden set at one decoding, in profile order. */
export function profileCells(e: Eval, goldenSetVersionId: string, decoding = 0): ProfileRow[] {
  const cells = (e.cells ?? []).filter((c) => c.goldenSetVersionId === goldenSetVersionId && c.decodingIndex === decoding && plain(c));
  return e.profiles
    .map((p) => ({
      profile: p,
      primary: p.name === e.primaryProfile && decoding === 0,
      subject: cells.find((c) => c.role === "subject" && c.profile === p.name),
      baseline: cells.find((c) => c.role === "baseline" && c.profile === p.name),
    }))
    .filter((r) => r.subject || r.baseline);
}

const pct2 = (v: number) => Math.round(v * 10000) / 100;
const gsName = (e: Eval, id: string) => {
  const gs = e.goldenSets.find((g) => g.versionId === id);
  return gs ? shortName(gs.name) : id;
};

/**
 * WER against latency (R53): x the profile's latency, y WER, one series per model, the primary profile marked. The
 * subject's bars are the baseline's WER plus the paired bootstrap's 95 % interval of the delta (where it has one).
 */
export function werLatency(e: Eval, goldenSetVersionId: string, decoding = 0): LineSpec {
  const rows = profileCells(e, goldenSetVersionId, decoding).sort((a, b) => a.profile.latencyMs - b.profile.latencyMs);
  const subject: LinePoint[] = [];
  const baseline: LinePoint[] = [];
  for (const r of rows) {
    const at = { x: r.profile.latencyMs, label: r.profile.name, ...(r.primary ? { marked: true } : {}) };
    const s = r.subject?.summary;
    const b = r.baseline?.summary;
    const d = r.subject?.delta;
    if (s) subject.push({ ...at, y: pct2(s.wer), ...(b && d && !d.error ? { low: pct2(b.wer + d.wer.low), high: pct2(b.wer + d.wer.high) } : {}) });
    if (b) baseline.push({ ...at, y: pct2(b.wer) });
  }
  const lat = rows.map((r) => r.profile.latencyMs).filter((v) => v > 0);
  const log = lat.length > 1 && lat.length === rows.length && Math.max(...lat) / Math.min(...lat) >= 8;
  const primary = rows.find((r) => r.primary)?.profile;
  return {
    kind: "line",
    title: `WER against latency · ${gsName(e, goldenSetVersionId)}`,
    xLabel: "Profile latency (ms)",
    yLabel: "WER %",
    unit: "%",
    format: (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(2)),
    xType: log ? "log" : "value",
    xMarks: primary ? [{ label: `primary ${primary.name}`, value: primary.latencyMs }] : [],
    ...(subject.some((p) => p.low != null) ? { note: "The subject's bars are the baseline's WER plus the 95 % interval of the paired delta." } : {}),
    series: [
      { id: "subject", label: "Subject", slot: 0, points: subject },
      { id: "baseline", label: "Baseline", slot: 1, points: baseline },
    ],
  };
}

const latencyOf = (c?: EvalCell): EvalLatencyScores | undefined => (c?.metrics?.latency?.available ? c.metrics.latency : undefined);

/** Latency to final per profile and model (p50, p95 and max in ms; R54); undefined when no cell has it yet. */
export function latencyBars(e: Eval, goldenSetVersionId: string, decoding = 0): BarSpec | undefined {
  const categories: string[] = [];
  const p50: (number | null)[] = [];
  const p95: (number | null)[] = [];
  const max: (number | null)[] = [];
  const paces = new Set<string>();
  for (const r of profileCells(e, goldenSetVersionId, decoding)) {
    for (const role of ["subject", "baseline"] as const) {
      const l = latencyOf(r[role]);
      if (!l) continue;
      if (l.pace) paces.add(l.pace);
      categories.push(`${r.profile.name}${r.primary ? " ★" : ""} · ${role} (n=${l.measured})`);
      p50.push(l.p50Ms ?? null);
      p95.push(l.p95Ms ?? null);
      max.push(l.maxMs ?? null);
    }
  }
  if (!categories.length) return undefined;
  return {
    kind: "bar",
    title: `Latency to final · ${gsName(e, goldenSetVersionId)}`,
    xLabel: "Profile · model (measured utterances)",
    yLabel: "ms after speech end",
    unit: "ms",
    format: (v: number) => String(Math.round(v)),
    horizontal: true,
    categories,
    ...(paces.size ? { note: `Pace: ${[...paces].join(", ")}; speech end from the frame VAD.` } : {}),
    series: [
      { id: "p50", label: "p50", slot: 0, values: p50 },
      { id: "p95", label: "p95", slot: 1, values: p95 },
      { id: "max", label: "max", slot: 2, values: max },
    ],
  };
}

/** Why cells have no value for a metric: one entry per reason with the cells it covers ("fleurs-he · 160ms · subject"). */
export function unavailableReasons(e: Eval, metric: "latency" | "entities", cells: EvalCell[]): { reason: string; cells: string[] }[] {
  const by = new Map<string, string[]>();
  const add = (reason: string, c: EvalCell) => {
    const where = `${cellTitle(e, c)} · ${c.role}`;
    const list = by.get(reason) ?? [];
    if (!list.includes(where)) list.push(where);
    by.set(reason, list);
  };
  for (const c of cells) {
    for (const u of c.metrics?.unavailable ?? []) if (u.metric === metric) add(u.reason, c);
    const m = c.metrics?.[metric];
    if (m && !m.available) add(m.reason ?? "not available", c);
  }
  return [...by].map(([reason, where]) => ({ reason, cells: where }));
}

type Stability = NonNullable<EvalSummary["stability"]>;

/** Partial stability per profile (R54): the unstable partial word ratio (%) and partial edits per second. */
export function stabilityBars(e: Eval, goldenSetVersionId: string, decoding = 0): { ratio: BarSpec; edits: BarSpec } | undefined {
  const rows = profileCells(e, goldenSetVersionId, decoding).filter((r) => r.subject?.summary?.stability || r.baseline?.summary?.stability);
  if (!rows.length) return undefined;
  const name = gsName(e, goldenSetVersionId);
  const categories = rows.map((r) => `${r.profile.name}${r.primary ? " ★" : ""}`);
  const pick = (role: "subject" | "baseline", f: (s: Stability) => number | undefined) =>
    rows.map((r) => {
      const st = r[role]?.summary?.stability;
      return (st ? f(st) : undefined) ?? null;
    });
  const series = (f: (s: Stability) => number | undefined) => [
    { id: "subject", label: "Subject", slot: 0, values: pick("subject", f) },
    { id: "baseline", label: "Baseline", slot: 1, values: pick("baseline", f) },
  ];
  return {
    ratio: {
      kind: "bar",
      title: `Unstable partial words · ${name}`,
      xLabel: "Latency profile",
      yLabel: "% of partial words",
      unit: "%",
      format: (v: number) => v.toFixed(1),
      categories,
      series: series((s) => (s.ratio == null ? undefined : pct2(s.ratio))),
    },
    edits: {
      kind: "bar",
      title: `Partial edits per second · ${name}`,
      xLabel: "Latency profile",
      yLabel: "edits/s",
      format: (v: number) => v.toFixed(2),
      categories,
      series: series((s) => s.editsPerSecond),
    },
  };
}

// ---------------------------------------------------------------- entity accuracy and robustness

/** Entity accuracy per ITN class (% of reference entities read correctly), subject against baseline, "all" first. */
export function entityBars(title: string, subject?: EvalEntityScores, baseline?: EvalEntityScores): BarSpec | undefined {
  const s = subject?.available ? subject : undefined;
  const b = baseline?.available ? baseline : undefined;
  const first = s ?? b;
  if (!first) return undefined;
  const classes: string[] = [];
  const refs = new Map<string, number>();
  for (const x of [s, b]) {
    for (const c of x?.classes ?? []) {
      if (classes.includes(c.class)) continue;
      classes.push(c.class);
      refs.set(c.class, c.refEntities);
    }
  }
  const acc = (x: EvalEntityScores | undefined, cls?: string) => {
    if (!x) return null;
    const v = cls === undefined ? x.accuracy : x.classes?.find((c) => c.class === cls)?.accuracy;
    return v == null ? null : pct2(v);
  };
  return {
    kind: "bar",
    title: `Entity accuracy · ${title}`,
    xLabel: "Entity class (reference entities)",
    yLabel: "% of reference entities correct",
    unit: "%",
    format: (v: number) => v.toFixed(1),
    categories: [`all (${first.refEntities})`, ...classes.map((c) => `${c} (${refs.get(c) ?? 0})`)],
    note: "Reported, not gated.",
    series: [
      { id: "subject", label: "Subject", slot: 0, values: [acc(s), ...classes.map((c) => acc(s, c))] },
      { id: "baseline", label: "Baseline", slot: 1, values: [acc(b), ...classes.map((c) => acc(b, c))] },
    ],
  };
}

/** The augmentation's name ("augment/telephony.yaml@abc…" → "telephony"). */
export function augmentationLabel(e: Eval, index: number): string {
  const a = e.augmentations?.find((x) => x.index === index);
  if (!a) return `augmentation ${index}`;
  return a.name ?? a.profile.replace(/^augment\//, "").replace(/\.ya?ml(@.*)?$/, "");
}

/**
 * The robustness matrix (golden set × augmentation × latency profile): WER degradation under each augmentation
 * against the same cell without it, in percentage points; one row per golden set, augmentation and model.
 */
export function robustnessHeatmap(e: Eval, decoding = 0): HeatmapSpec | undefined {
  const rows = (e.robustness ?? []).filter((r) => r.decodingIndex === decoding);
  if (!rows.length) return undefined;
  const profiles = e.profiles.filter((p) => rows.some((r) => r.profile === p.name));
  const augs = [...new Set(rows.map((r) => r.augmentationIndex))].sort((a, b) => a - b);
  const y: string[] = [];
  const cells: HeatmapSpec["cells"] = [];
  for (const gs of e.goldenSets) {
    for (const aug of augs) {
      for (const role of ["subject", "baseline"] as const) {
        const mine = rows.filter((r) => r.goldenSetVersionId === gs.versionId && r.augmentationIndex === aug && r.role === role);
        if (!mine.length) continue;
        const yi = y.length;
        // A golden set scored on characters (eval.character_error_languages) degrades in CER, not WER.
        const cer = mine.some((r) => r.unit === "char");
        y.push(`${shortName(gs.name)}${cer ? " (CER)" : ""} · ${augmentationLabel(e, aug)} · ${role}`);
        profiles.forEach((p, xi) => {
          const d = mine.find((r) => r.profile === p.name)?.degradation;
          cells.push({ x: xi, y: yi, value: d == null ? null : pct2(d) });
        });
      }
    }
  }
  const allCer = rows.every((r) => r.unit === "char");
  const anyCer = rows.some((r) => r.unit === "char");
  return {
    kind: "heatmap",
    title: `${allCer ? "CER" : anyCer ? "WER / CER" : "WER"} degradation under augmentation`,
    xLabel: "Latency profile",
    yLabel: "Golden set · augmentation · model",
    unit: "pp",
    format: ppFormat,
    x: profiles.map((p) => (p.name === e.primaryProfile && decoding === 0 ? `${p.name} ★` : p.name)),
    y,
    colormap: "diverging",
    center: 0,
    cells,
    note:
      "Degradation is WER under the augmentation minus WER without it (CER for golden sets marked so, languages written without spaces), in percentage points; positive is worse; empty cells are not scored yet.",
  };
}
