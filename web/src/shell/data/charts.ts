import type { DatasetHistogram, DatasetPayload, DatasetPreviewRequest, DatasetStatsGroup } from "@/api/gen/types.gen";
import type { BarSpec, Bin, HistogramSpec } from "@/shell/charts";

// Dataset version statistics (R53, docs/spec/11-ui-panels.md "Dataset version"): the numbers come binned from
// datasets.get (`dataset.stats`, the histograms dataset_freeze computed), as an agent sees them; these builders only
// turn them into chart specs for @/shell/charts.

/**
 * A contract histogram as bins: edges are lower bounds and the last bucket is open, so it is drawn as wide as the
 * one before it (or 1 when there is only one).
 */
export function histogramBins(h: DatasetHistogram | undefined): Bin[] {
  if (!h) return [];
  const n = Math.min(h.edges.length, h.counts.length);
  const out: Bin[] = [];
  for (let i = 0; i < n; i++) {
    const start = h.edges[i]!;
    const next = h.edges[i + 1];
    const width = next !== undefined ? next - start : i > 0 ? start - h.edges[i - 1]! : 1;
    out.push({ start, end: start + width, count: h.counts[i]! });
  }
  return out;
}

const fmt1 = (v: number) => (Math.round(v * 10) / 10).toString();

/** Duration histogram with the p5/p50/p95 marks and, when a preview applied them, the filter bounds. */
export function durationChart(d: DatasetPayload, filter?: Pick<DatasetPreviewRequest, "minDuration" | "maxDuration">): HistogramSpec | undefined {
  const bins = histogramBins(d.stats?.durationHistogram);
  if (!bins.length) return undefined;
  const marks: { label: string; value: number }[] = [];
  const p = d.stats?.durationPercentiles;
  if (p?.p5 !== undefined) marks.push({ label: "p5", value: p.p5 });
  if (p?.p50 !== undefined) marks.push({ label: "p50", value: p.p50 });
  if (p?.p95 !== undefined) marks.push({ label: "p95", value: p.p95 });
  if (filter?.minDuration !== undefined) marks.push({ label: "min filter", value: filter.minDuration });
  if (filter?.maxDuration !== undefined) marks.push({ label: "max filter", value: filter.maxDuration });
  return {
    kind: "histogram",
    title: "Utterance duration",
    xLabel: "Duration",
    yLabel: "Utterances",
    unit: "s",
    format: fmt1,
    series: [{ id: "duration", label: "Utterances", bins }],
    marks,
    note: p ? `p5 ${fmt1(p.p5 ?? 0)} s, median ${fmt1(p.p50 ?? 0)} s, p95 ${fmt1(p.p95 ?? 0)} s` : undefined,
  };
}

/** Characters per second: the outliers sit in the tails (the length_outliers quality check). */
export function charsPerSecondChart(d: DatasetPayload, filter?: Pick<DatasetPreviewRequest, "minCharsPerSecond" | "maxCharsPerSecond">): HistogramSpec | undefined {
  const bins = histogramBins(d.stats?.charsPerSecondHistogram);
  if (!bins.length) return undefined;
  const marks: { label: string; value: number }[] = [];
  if (filter?.minCharsPerSecond !== undefined) marks.push({ label: "min filter", value: filter.minCharsPerSecond });
  if (filter?.maxCharsPerSecond !== undefined) marks.push({ label: "max filter", value: filter.maxCharsPerSecond });
  return {
    kind: "histogram",
    title: "Characters per second",
    xLabel: "Characters per second",
    yLabel: "Utterances",
    format: fmt1,
    series: [{ id: "cps", label: "Utterances", bins }],
    marks,
    note: "Transcript length against duration; the tails are where misaligned or truncated transcripts sit.",
  };
}

/** Speech level (dBFS) per utterance. */
export function levelChart(d: DatasetPayload): HistogramSpec | undefined {
  const bins = histogramBins(d.stats?.levelHistogram);
  if (!bins.length) return undefined;
  return {
    kind: "histogram",
    title: "Level",
    xLabel: "Level",
    yLabel: "Utterances",
    unit: "dBFS",
    format: fmt1,
    series: [{ id: "level", label: "Utterances", bins }],
  };
}

const hours = (v: number) => Math.round(v * 100) / 100;

/** Hours per language (dataset.languages), else per locale of the version. */
export function languageChart(d: DatasetPayload): BarSpec | undefined {
  const rows = d.languages ?? [];
  if (!rows.length) return undefined;
  return {
    kind: "bar",
    title: "Hours by language",
    categories: rows.map((r) => r.language),
    yLabel: "Hours",
    unit: "h",
    series: [{ id: "hours", label: "Hours", values: rows.map((r) => hours(r.hours)) }],
  };
}

/** Hours and utterances per split. */
export function splitChart(d: DatasetPayload): BarSpec | undefined {
  if (!d.splits.length) return undefined;
  return {
    kind: "bar",
    title: "Hours by split",
    categories: d.splits.map((s) => s.name),
    yLabel: "Hours",
    unit: "h",
    series: [{ id: "hours", label: "Hours", values: d.splits.map((s) => hours(s.hours)) }],
  };
}

/** Hours per transcript origin (human, pseudo-label, …) or per channel role (caller, bot, mono). */
export function groupChart(title: string, groups: DatasetStatsGroup[] | undefined, key: "origin" | "role"): BarSpec | undefined {
  const rows = (groups ?? []).filter((g) => g[key]);
  if (!rows.length) return undefined;
  return {
    kind: "bar",
    title,
    categories: rows.map((g) => g[key]!),
    yLabel: "Hours",
    unit: "h",
    series: [{ id: "hours", label: "Hours", values: rows.map((g) => hours(g.hours)) }],
  };
}

/** Utterances per source sample rate (8 kHz-origin audio is telephone audio, R52). */
export function sampleRateChart(d: DatasetPayload): BarSpec | undefined {
  const rows = d.stats?.sourceRates ?? [];
  if (!rows.length) return undefined;
  return {
    kind: "bar",
    title: "Source sample rates",
    categories: rows.map((r) => `${r.rate / 1000} kHz`),
    yLabel: "Utterances",
    series: [{ id: "utterances", label: "Utterances", values: rows.map((r) => r.utterances) }],
  };
}

const fmt2 = (v: number) => (Math.round(v * 100) / 100).toString();

/**
 * End-of-utterance gaps (`stats.eou`, dataset_freeze from sdp_ingest's per-channel VAD): from a segment's last speech
 * to the other party's next speech, for segments of multi-channel recordings. Below zero the other party started
 * before the speech ended (barge-in). Only when the version has gaps measured.
 */
export function eouChart(d: DatasetPayload): HistogramSpec | undefined {
  const e = d.stats?.eou;
  const bins = histogramBins(e?.gapHistogram);
  if (!e || !bins.length) return undefined;
  const marks: { label: string; value: number }[] = [];
  if (e.p50GapS !== undefined) marks.push({ label: "p50", value: e.p50GapS });
  if (e.p90GapS !== undefined) marks.push({ label: "p90", value: e.p90GapS });
  const parts = [`The other party spoke next within 10 s after ${e.withGap} of ${e.utterances} segments of multi-channel recordings`];
  if (e.p50GapS !== undefined && e.p90GapS !== undefined) parts.push(`gap p50 ${fmt2(e.p50GapS)} s, p90 ${fmt2(e.p90GapS)} s${e.meanGapS !== undefined ? `, mean ${fmt2(e.meanGapS)} s` : ""}`);
  if (e.overlapping) parts.push(`${e.overlapping} overlapping (the other party started before the speech ended)`);
  return {
    kind: "histogram",
    title: "End-of-utterance gap",
    xLabel: "Gap to the other party's next speech",
    yLabel: "Segments",
    unit: "s",
    format: fmt2,
    series: [{ id: "eou", label: "Segments", bins }],
    marks,
    note: `${parts.join("; ")}.`,
  };
}

/** Every chart the version has data for, in reading order. */
export function datasetCharts(d: DatasetPayload, filter?: DatasetPreviewRequest): (HistogramSpec | BarSpec)[] {
  return [
    languageChart(d),
    splitChart(d),
    durationChart(d, filter),
    charsPerSecondChart(d, filter),
    levelChart(d),
    sampleRateChart(d),
    groupChart("Hours by transcript origin", d.stats?.origins, "origin"),
    groupChart("Hours by channel role", d.stats?.roles, "role"),
    eouChart(d),
  ].filter((c): c is HistogramSpec | BarSpec => !!c);
}
