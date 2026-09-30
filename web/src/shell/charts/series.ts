// The time-series model behind TimeSeriesChart: from the caller's series (parallel arrays per x field) to the
// aligned columns uPlot draws, the table view and the text summary. Pure, so it is tested without a canvas.
import { align, bucketCentres, ema, envelope, logSafe, sortedPoints, type Num } from "./math";
import { formatNumber, type Table } from "./table";

/** The x fields a time series can be drawn against (R53: step, epoch, wall time or GPU-hours). */
export type XKey = "step" | "epoch" | "wallTime" | "gpuHours";

export const X_LABELS: Record<XKey, string> = { step: "Step", epoch: "Epoch", wallTime: "Wall time", gpuHours: "GPU-hours" };

export type TimeSeries = {
  /** Stable identity (a run id plus metric, say): colour and dash follow it, not its position. */
  id: string;
  label: string;
  /** Categorical slot 0…; default: the position in `series`. Pass it to keep colours when a filter hides series. */
  slot?: number;
  /** Parallel arrays, one per x field the caller has (`wallTime` in Unix seconds). */
  x: Partial<Record<XKey, ArrayLike<number>>>;
  y: ArrayLike<number | null>;
};

export type MarkerKind = "checkpoint" | "best" | "event";

export type ChartMarker = {
  id: string;
  label: string;
  kind?: MarkerKind;
  /** Position per x field; a marker without the current field is not drawn. */
  x: Partial<Record<XKey, number>>;
};

export type EnvelopeMode = "auto" | "on" | "off";

export type FrameOptions = {
  xKey: XKey;
  smoothing: number;
  envelope: EnvelopeMode;
  log: boolean;
  /** Plot width in CSS px: `auto` draws an envelope when a series has more than two points per pixel. */
  widthPx: number;
};

export type FrameLine = {
  id: string;
  label: string;
  slot: number;
  /** Column indexes into `Frame.columns`: envelope min, max, the faint raw line and the main line. */
  min: Num[];
  max: Num[];
  raw: Num[];
  main: Num[];
};

export type Frame = {
  x: number[];
  lines: FrameLine[];
  /** True when the lines are binned into min/max envelopes. */
  dense: boolean;
  smoothed: boolean;
  /** Values a log scale cannot show (≤ 0), per line id. */
  skipped: Record<string, number>;
};

export function slotOf(s: TimeSeries, i: number): number {
  return s.slot ?? i;
}

/**
 * Builds the drawn frame. Four columns per line, always, so live appends never change uPlot's series structure:
 * - sparse, no smoothing: `main` is the raw series; the rest are gaps;
 * - sparse, smoothing: `raw` is the faint raw line, `main` the EMA;
 * - dense: `min`/`max` bound the envelope over equal-width buckets and `main` is the bucket mean (of the EMA
 *   when smoothing), so one line per series survives at any zoom-out.
 */
export function buildFrame(series: TimeSeries[], o: FrameOptions): Frame {
  const smoothed = o.smoothing > 0;
  const skipped: Record<string, number> = {};
  const pts = series.map((s) => {
    const p = sortedPoints(s.x[o.xKey], s.y);
    let y = p.y;
    if (o.log) {
      const safe = logSafe(y);
      y = safe.values;
      if (safe.skipped) skipped[s.id] = safe.skipped;
    }
    return { x: p.x, y, smooth: smoothed ? ema(y, o.smoothing) : null };
  });
  const maxPoints = Math.max(0, ...pts.map((p) => p.x.length));
  const dense = o.envelope === "on" || (o.envelope === "auto" && o.widthPx > 0 && maxPoints > 2 * o.widthPx);
  if (dense) {
    let lo = Infinity;
    let hi = -Infinity;
    for (const p of pts) {
      if (p.x.length) {
        lo = Math.min(lo, p.x[0] as number);
        hi = Math.max(hi, p.x[p.x.length - 1] as number);
      }
    }
    if (!Number.isFinite(lo)) return { x: [], lines: [], dense, smoothed, skipped };
    const n = Math.max(1, Math.round(o.widthPx > 0 ? o.widthPx / 2 : 500));
    const x = bucketCentres(lo, hi, n);
    const lines = series.map((s, i) => {
      const p = pts[i] as (typeof pts)[number];
      const env = envelope(p.x, p.y, lo, hi, x.length);
      const main = p.smooth ? envelope(p.x, p.smooth, lo, hi, x.length).mean : env.mean;
      return { id: s.id, label: s.label, slot: slotOf(s, i), min: env.min, max: env.max, raw: gaps(x.length), main };
    });
    return { x, lines, dense, smoothed, skipped };
  }
  const aligned = align(pts.map((p) => ({ x: p.x, ys: p.smooth ? [p.y, p.smooth] : [p.y] })));
  let c = 0;
  const lines = series.map((s, i) => {
    const p = pts[i] as (typeof pts)[number];
    const raw = aligned.columns[c++] as Num[];
    const smooth = p.smooth ? (aligned.columns[c++] as Num[]) : null;
    const n = aligned.x.length;
    return { id: s.id, label: s.label, slot: slotOf(s, i), min: gaps(n), max: gaps(n), raw: smooth ? raw : gaps(n), main: smooth ?? raw };
  });
  return { x: aligned.x, lines, dense, smoothed, skipped };
}

function gaps(n: number): Num[] {
  return new Array<Num>(n).fill(null);
}

export function formatX(v: number, xKey: XKey): string {
  if (xKey === "wallTime") return new Date(v * 1000).toISOString().replace(".000Z", "Z");
  if (xKey === "step") return formatNumber(v, 0);
  return formatNumber(v);
}

/** The table view: the raw values on the union of x (never binned), plus the smoothed values when smoothing. */
export function timeSeriesTable(series: TimeSeries[], xKey: XKey, smoothing: number, markers: ChartMarker[] = []): Table {
  const pts = series.map((s) => sortedPoints(s.x[xKey], s.y));
  const smoothed = smoothing > 0;
  const aligned = align(pts.map((p) => ({ x: p.x, ys: smoothed ? [p.y, ema(p.y, smoothing)] : [p.y] })));
  const columns = [X_LABELS[xKey]];
  for (const s of series) {
    columns.push(s.label);
    if (smoothed) columns.push(`${s.label} (smoothed)`);
  }
  const marks = new Map<number, string[]>();
  for (const m of markers) {
    const v = m.x[xKey];
    if (v != null) marks.set(v, [...(marks.get(v) ?? []), m.label]);
  }
  if (marks.size) columns.push("Marker");
  const rows = aligned.x.map((x, i) => {
    const row: (string | number | null)[] = [xKey === "wallTime" ? formatX(x, xKey) : x];
    for (const col of aligned.columns) row.push(col[i] ?? null);
    if (marks.size) row.push(marks.get(x)?.join("; ") ?? null);
    return row;
  });
  return { columns, rows };
}

type Stats = { n: number; first?: [number, number]; last?: [number, number]; min?: [number, number]; max?: [number, number] };

function stats(x: number[], y: Num[]): Stats {
  const s: Stats = { n: 0 };
  for (let i = 0; i < x.length; i++) {
    const v = y[i];
    if (v == null) continue;
    const xi = x[i] as number;
    s.n++;
    s.first ??= [xi, v];
    s.last = [xi, v];
    if (!s.min || v < s.min[1]) s.min = [xi, v];
    if (!s.max || v > s.max[1]) s.max = [xi, v];
  }
  return s;
}

/** Plain-language trend over the smoothed tail: compares the mean of the first and last fifth. */
export function trend(y: Num[]): "rising" | "falling" | "flat" | undefined {
  const vals = y.filter((v): v is number => v != null);
  if (vals.length < 4) return undefined;
  const k = Math.max(1, Math.floor(vals.length / 5));
  const mean = (a: number[]) => a.reduce((p, c) => p + c, 0) / a.length;
  const a = mean(vals.slice(0, k));
  const b = mean(vals.slice(-k));
  const scale = Math.max(Math.abs(a), Math.abs(b), Number.EPSILON);
  const rel = (b - a) / scale;
  if (Math.abs(rel) < 0.02) return "flat";
  return rel > 0 ? "rising" : "falling";
}

/** The text summary (R53 accessibility): one sentence per series plus the markers. */
export function summarizeTimeSeries(
  title: string,
  series: TimeSeries[],
  o: { xKey: XKey; smoothing: number; log: boolean; unit?: string; format?: (v: number) => string },
  markers: ChartMarker[] = [],
): string {
  const fmt = o.format ?? ((v: number) => formatNumber(v));
  const unit = o.unit ? ` ${o.unit}` : "";
  const xl = X_LABELS[o.xKey].toLowerCase();
  const fx = (v: number) => formatX(v, o.xKey);
  const parts: string[] = [];
  const scale = o.log ? "log" : "linear";
  parts.push(`${title}: ${series.length} series by ${xl}, ${scale} scale${o.smoothing > 0 ? `, smoothing ${o.smoothing}` : ""}.`);
  for (const s of series) {
    const p = sortedPoints(s.x[o.xKey], s.y);
    const st = stats(p.x, p.y);
    if (!st.n || !st.first || !st.last || !st.min || !st.max) {
      parts.push(`${s.label}: no data by ${xl}.`);
      continue;
    }
    const t = trend(o.smoothing > 0 ? ema(p.y, o.smoothing) : p.y);
    parts.push(
      `${s.label}: ${formatNumber(st.n, 0)} points, ${xl} ${fx(st.first[0])} to ${fx(st.last[0])}; ` +
        `last ${fmt(st.last[1])}${unit}, minimum ${fmt(st.min[1])}${unit} at ${fx(st.min[0])}, maximum ${fmt(st.max[1])}${unit} at ${fx(st.max[0])}` +
        `${t ? `; ${t}` : ""}.`,
    );
  }
  const shown = markers.filter((m) => m.x[o.xKey] != null);
  if (shown.length) {
    const list = shown.slice(0, 5).map((m) => `${m.label} at ${fx(m.x[o.xKey] as number)}`);
    parts.push(`${shown.length} ${shown.length === 1 ? "marker" : "markers"}: ${list.join(", ")}${shown.length > 5 ? ", …" : ""}.`);
  }
  return parts.join(" ");
}
