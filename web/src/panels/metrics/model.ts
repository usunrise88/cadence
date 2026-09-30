import type { MetricAxis, MetricSeriesSet } from "@/api/gen/types.gen";
import type { ChartMarker, TimeSeries, XKey } from "@/shell/charts";

// Pure logic of the Metrics panel: which charts to draw and in what order, series from the binned API answer (R53:
// the server bins, the browser only smooths and zooms), live append after a step, and checkpoint marks.

/** Chart order and presentation for the metrics a train step reports; others follow alphabetically. */
export const KNOWN: { names: string[]; title: string; unit?: string; log?: boolean }[] = [
  { names: ["loss", "train_loss"], title: "Training loss" },
  { names: ["val_loss"], title: "Validation loss" },
  { names: ["val_wer", "wer"], title: "Validation WER" },
  { names: ["lr", "learning_rate"], title: "Learning rate", log: true },
  { names: ["grad_norm"], title: "Gradient norm" },
  { names: ["throughput", "audio_seconds_per_second"], title: "Throughput", unit: "audio s/s" },
  { names: ["gpu_memory_gb", "gpu_memory"], title: "GPU memory", unit: "GB" },
];

export const AXES: { id: MetricAxis; label: string; key: XKey }[] = [
  { id: "step", label: "Step", key: "step" },
  { id: "epoch", label: "Epoch", key: "epoch" },
  { id: "wall", label: "Wall time", key: "wallTime" },
  { id: "gpuHours", label: "GPU-hours", key: "gpuHours" },
];

export const xKeyOf = (axis: MetricAxis): XKey => AXES.find((a) => a.id === axis)?.key ?? "step";

export type ChartGroup = { key: string; title: string; unit?: string; log?: boolean; names: string[] };

/** One chart per known metric present (in the known order), then one per other name. */
export function chartGroups(names: Iterable<string>): ChartGroup[] {
  const present = new Set(names);
  const out: ChartGroup[] = [];
  const used = new Set<string>();
  for (const k of KNOWN) {
    const hit = k.names.filter((n) => present.has(n));
    if (hit.length === 0) continue;
    hit.forEach((n) => used.add(n));
    out.push({ key: k.names[0]!, title: k.title, unit: k.unit, log: k.log, names: hit });
  }
  for (const n of [...present].filter((n) => !used.has(n)).sort()) out.push({ key: n, title: n, names: [n] });
  return out;
}

/** The x value of each point for the chart: wall time as Unix seconds (the chart formats it as a time). */
function xs(set: MetricSeriesSet, name: string): number[] {
  const s = set.series.find((x) => x.name === name);
  if (!s) return [];
  return set.x === "wall" ? s.points.map((p) => (p.wallTime ? Date.parse(p.wallTime) / 1000 : p.x)) : s.points.map((p) => p.x);
}

/** A chart's series: each run's series of the group's names; pinned runs keep their slot so colours stay put. */
export function seriesFor(group: ChartGroup, runs: { runId: string; label: string; slot: number; set: MetricSeriesSet | undefined }[]): TimeSeries[] {
  const out: TimeSeries[] = [];
  for (const r of runs) {
    if (!r.set) continue;
    const key = xKeyOf(r.set.x);
    for (const name of group.names) {
      const s = r.set.series.find((x) => x.name === name);
      if (!s || s.points.length === 0) continue;
      out.push({
        id: `${r.runId}:${name}`,
        label: runs.length > 1 || group.names.length > 1 ? `${r.label}${group.names.length > 1 ? ` · ${name}` : ""}` : name,
        slot: r.slot * group.names.length + group.names.indexOf(name),
        x: { [key]: xs(r.set, name) },
        y: s.points.map((p) => p.value),
      });
    }
  }
  return out;
}

/** Checkpoint marks at the x of the step they were saved at (the first point at or after it). */
export function checkpointMarks(set: MetricSeriesSet | undefined): ChartMarker[] {
  if (!set) return [];
  const key = xKeyOf(set.x);
  const ref = set.series.find((s) => s.points.length && s.points.every((p) => p.step !== undefined));
  const best = set.checkpoints.filter((c) => c.valWer !== undefined).sort((a, b) => a.valWer! - b.valWer!)[0];
  const out: ChartMarker[] = [];
  for (const c of set.checkpoints) {
    if (c.step === undefined) continue;
    let x: number | undefined = set.x === "step" ? c.step : undefined;
    if (x === undefined && ref) {
      const i = ref.points.findIndex((p) => (p.step ?? 0) >= c.step!);
      const p = ref.points[i >= 0 ? i : ref.points.length - 1];
      if (p) x = set.x === "wall" && p.wallTime ? Date.parse(p.wallTime) / 1000 : p.x;
    }
    if (x === undefined) continue;
    out.push({
      id: c.id,
      label: `${c.kind === "averaged" ? "averaged" : `step ${c.step}`}${c.valWer !== undefined ? ` · val WER ${Math.round(c.valWer * 10000) / 10000}` : ""}${c.kept ? " · kept" : ""}`,
      kind: best && c.id === best.id ? "best" : "checkpoint",
      x: { [key]: x },
    });
  }
  return out;
}

/**
 * Appends a live delta (metrics.get with afterStep) to the set. Undefined when the set must be read again instead:
 * either side is binned, or the axis changed.
 */
export function appendDelta(set: MetricSeriesSet, delta: MetricSeriesSet): MetricSeriesSet | undefined {
  if (delta.x !== set.x) return undefined;
  if (set.series.some((s) => s.binned) || delta.series.some((s) => s.binned)) return undefined;
  const byName = new Map(set.series.map((s) => [s.name, s]));
  for (const d of delta.series) {
    const cur = byName.get(d.name);
    const last = cur?.points[cur.points.length - 1]?.step ?? -1;
    const fresh = d.points.filter((p) => (p.step ?? Infinity) > last);
    byName.set(d.name, cur ? { ...cur, total: cur.total + fresh.length, points: [...cur.points, ...fresh] } : d);
  }
  const series = [...byName.values()];
  if (series.some((s) => s.points.length > set.maxPoints)) return undefined; // time to bin: read it again
  const known = new Set(set.checkpoints.map((c) => c.id));
  return {
    ...set,
    lastStep: Math.max(set.lastStep ?? 0, delta.lastStep ?? 0),
    series,
    checkpoints: [...set.checkpoints, ...delta.checkpoints.filter((c) => !known.has(c.id))],
  };
}

/** The lowest optimiser step among run.{id}.metrics event payloads (and what was already pending). */
export function minEventStep(payloads: unknown[], cur: number | undefined): number | undefined {
  let m = cur;
  for (const p of payloads) {
    for (const pt of (p as { points?: { step?: number }[] } | undefined)?.points ?? []) {
      if (pt.step !== undefined && (m === undefined || pt.step < m)) m = pt.step;
    }
  }
  return m;
}

/**
 * afterStep for the live read: after the set's last step, or before the earliest step the events carried (a metric
 * reported at a step the set already reached — validation WER at the step of the last loss point — is not lost).
 */
export function afterStepFor(set: MetricSeriesSet, earliest: number | undefined): number {
  const last = set.lastStep ?? 0;
  return earliest === undefined ? last : Math.max(0, Math.min(last, earliest - 1));
}
