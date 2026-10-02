// Analytics presets for AnalyticsChart (ECharts): pure builders from binned contract data to an ECharts option, the
// table view, the text summary and the keyboard cursor's items. The API bins; the browser only draws (R53).
import type { EChartsCoreOption } from "echarts/core";
import { formatCell, formatNumber, type Table } from "./table";
import { dashFor, seriesColor, SYMBOLS, type ChartTheme, type Colormap } from "./tokens";

type Base = {
  title: string;
  xLabel?: string;
  yLabel?: string;
  unit?: string;
  format?: (v: number) => string;
};

export type Bin = { start: number; end: number; count: number };

/** Binned counts; several series overlay (two dataset versions). `marks` are filter bounds or percentiles. */
export type HistogramSpec = Base & {
  kind: "histogram";
  series: { id: string; label: string; slot?: number; bins: Bin[] }[];
  marks?: { label: string; value: number }[];
};

export type BarSpec = Base & {
  kind: "bar";
  categories: string[];
  series: { id: string; label: string; slot?: number; values: (number | null)[] }[];
  stacked?: boolean;
  horizontal?: boolean;
};

export type HeatmapSpec = Base & {
  kind: "heatmap";
  x: string[];
  y: string[];
  cells: { x: number; y: number; value: number | null }[];
  /** Sequential ramp, or diverging (blue–slate–orange) around `center` for deltas. */
  colormap?: Colormap | "diverging";
  range?: [number, number];
  center?: number;
};

export type ScatterSpec = Base & {
  kind: "scatter";
  series: { id: string; label: string; slot?: number; points: { x: number; y: number; label?: string }[] }[];
  /** Draws the Pareto front of all points, lower-is-better or higher-is-better per axis ("min" | "max"). */
  front?: { x: "min" | "max"; y: "min" | "max" };
};

/** Forest plot: an estimate with its interval per row (WER deltas with 95 % intervals), reference line at 0. */
export type ForestSpec = Base & {
  kind: "forest";
  rows: { label: string; estimate: number; low: number; high: number }[];
  reference?: number;
};

/** One axis of a parallel-coordinates chart: a numeric (linear or log) or a categorical dimension. */
export type ParallelAxis = { id: string; label: string; type?: "value" | "log" | "category"; categories?: string[] };

/**
 * Parallel coordinates (R53, Experiment: swept parameters and the metric of each run). One line per item; values
 * follow `axes` (a category axis takes the category's string). `highlight` draws a line wider and solid (the best run).
 */
export type ParallelSpec = Base & {
  kind: "parallel";
  axes: ParallelAxis[];
  lines: { id: string; label: string; slot?: number; values: (number | string | null)[]; highlight?: boolean }[];
};

export type AnalyticsSpec = HistogramSpec | BarSpec | HeatmapSpec | ScatterSpec | ForestSpec | ParallelSpec;

/** One keyboard-cursor stop: which ECharts item to highlight and what to announce. */
export type CursorItem = { seriesIndex: number; dataIndex: number; text: string };

const fmtOf = (s: Base) => s.format ?? ((v: number) => formatNumber(v));
const withUnit = (s: Base, v: string) => (s.unit ? `${v} ${s.unit}` : v);
const binLabel = (b: Bin, f: (v: number) => string) => `${f(b.start)}–${f(b.end)}`;

/** Pareto-optimal points (no other point is at least as good on both axes and better on one). */
export function paretoFront(points: { x: number; y: number }[], dir: { x: "min" | "max"; y: "min" | "max" }): { x: number; y: number }[] {
  const sx = dir.x === "min" ? 1 : -1;
  const sy = dir.y === "min" ? 1 : -1;
  const sorted = [...points].sort((a, b) => sx * (a.x - b.x) || sy * (a.y - b.y));
  const out: { x: number; y: number }[] = [];
  let best = Infinity;
  for (const p of sorted) {
    if (sy * p.y < best) {
      out.push(p);
      best = sy * p.y;
    }
  }
  return out;
}

/** Heatmap bounds: the given range, or the data's (symmetric around `center` when diverging). */
export function heatmapRange(s: HeatmapSpec): [number, number] {
  if (s.range) return s.range;
  const vals = s.cells.map((c) => c.value).filter((v): v is number => v != null && Number.isFinite(v));
  if (!vals.length) return [0, 1];
  const lo = Math.min(...vals);
  const hi = Math.max(...vals);
  if (s.colormap === "diverging") {
    const c = s.center ?? 0;
    const r = Math.max(Math.abs(lo - c), Math.abs(hi - c)) || 1;
    return [c - r, c + r];
  }
  return lo === hi ? [lo, lo + 1] : [lo, hi];
}

/** The ECharts theme object for `setTheme`: palette, text and axes from the chart tokens. */
export function echartsTheme(t: ChartTheme): Record<string, unknown> {
  const axis = {
    axisLine: { lineStyle: { color: t.axis } },
    axisTick: { lineStyle: { color: t.axis } },
    axisLabel: { color: t.axis },
    splitLine: { lineStyle: { color: t.grid } },
    nameTextStyle: { color: t.axis },
  };
  return {
    color: t.categorical,
    backgroundColor: "transparent",
    textStyle: { color: t.text, fontFamily: "inherit" },
    title: { textStyle: { color: t.text } },
    legend: { textStyle: { color: t.textSecondary } },
    categoryAxis: axis,
    valueAxis: axis,
    logAxis: axis,
    tooltip: { backgroundColor: t.surface, borderColor: t.border, textStyle: { color: t.text } },
    visualMap: { textStyle: { color: t.textSecondary } },
  };
}

/** Builds the ECharts option for a preset. `summary` becomes the ARIA description; decals are always on. */
export function buildOption(spec: AnalyticsSpec, theme: ChartTheme, summary: string): EChartsCoreOption {
  const f = fmtOf(spec);
  const common = {
    animation: false,
    aria: { enabled: true, label: { enabled: true, description: summary }, decal: { show: true } },
    grid: { left: 28, right: 16, top: 16, bottom: spec.xLabel ? 24 : 8, containLabel: true },
    tooltip: { trigger: spec.kind === "bar" || spec.kind === "histogram" ? "axis" : "item", confine: true, axisPointer: { type: "shadow" } },
  };
  const legend = (n: number) => (n > 1 ? { legend: { top: 0, type: "scroll", itemHeight: 10 } } : {});
  const nameOf = (s?: string) => (s ? { name: s, nameLocation: "middle" as const, nameGap: 28 } : {});
  switch (spec.kind) {
    case "histogram": {
      const bins = spec.series[0]?.bins ?? [];
      const cats = bins.map((b) => binLabel(b, f));
      const marks = (spec.marks ?? [])
        .map((m) => ({ m, i: bins.findIndex((b) => m.value >= b.start && m.value <= b.end) }))
        .filter((x) => x.i >= 0)
        .map(({ m, i }) => ({ xAxis: i, name: m.label, label: { formatter: m.label, color: theme.axis }, lineStyle: { color: theme.marker, type: "dashed" } }));
      return {
        ...common,
        ...legend(spec.series.length),
        grid: { ...common.grid, top: spec.series.length > 1 ? 32 : 20 },
        xAxis: { type: "category", data: cats, ...nameOf(spec.xLabel), axisLabel: { hideOverlap: true } },
        yAxis: { type: "value", ...nameOf(spec.yLabel ?? "Count"), nameGap: 40 },
        series: spec.series.map((s, i) => ({
          type: "bar",
          name: s.label,
          id: s.id,
          data: s.bins.map((b) => b.count),
          barGap: "0%",
          barCategoryGap: "8%",
          itemStyle: { color: seriesColor(theme, s.slot ?? i) },
          ...(i === 0 && marks.length ? { markLine: { symbol: "none", silent: true, data: marks } } : {}),
        })),
      };
    }
    case "bar": {
      const cat = { type: "category", data: spec.categories, ...nameOf(spec.xLabel), axisLabel: { hideOverlap: true } };
      const val = { type: "value", ...nameOf(spec.yLabel), nameGap: 40 };
      return {
        ...common,
        ...legend(spec.series.length),
        grid: { ...common.grid, top: spec.series.length > 1 ? 32 : 16 },
        xAxis: spec.horizontal ? val : cat,
        yAxis: spec.horizontal ? { ...cat, inverse: true } : val,
        series: spec.series.map((s, i) => ({
          type: "bar",
          name: s.label,
          id: s.id,
          data: s.values,
          stack: spec.stacked ? "total" : undefined,
          itemStyle: { color: seriesColor(theme, s.slot ?? i), borderColor: theme.surface, borderWidth: spec.stacked ? 1 : 0 },
        })),
      };
    }
    case "heatmap": {
      const [min, max] = heatmapRange(spec);
      const colors = spec.colormap === "diverging" ? theme.diverging : theme.sequential[spec.colormap ?? "magma"];
      const labelled = spec.cells.length <= 144;
      return {
        ...common,
        grid: { ...common.grid, right: 72 },
        tooltip: { trigger: "item", confine: true },
        xAxis: { type: "category", data: spec.x, ...nameOf(spec.xLabel), splitArea: { show: false } },
        yAxis: { type: "category", data: spec.y, ...nameOf(spec.yLabel), nameGap: 48 },
        visualMap: { type: "continuous", min, max, calculable: false, orient: "vertical", right: 0, top: "middle", itemHeight: 120, inRange: { color: colors }, formatter: (v: number) => f(v) },
        series: [
          {
            type: "heatmap",
            name: spec.title,
            data: spec.cells.map((c) => [c.x, c.y, c.value ?? "-"]),
            // Values in the cells: colour is never the only channel (when they fit).
            label: { show: labelled, formatter: (p: { value: [number, number, number | string] }) => (typeof p.value[2] === "number" ? f(p.value[2]) : ""), fontSize: 10 },
            itemStyle: { borderColor: theme.surface, borderWidth: 1 },
          },
        ],
      };
    }
    case "scatter": {
      const all = spec.series.flatMap((s) => s.points);
      const front = spec.front ? paretoFront(all, spec.front) : [];
      return {
        ...common,
        ...legend(spec.series.length + (front.length ? 1 : 0)),
        grid: { ...common.grid, top: spec.series.length > 1 ? 32 : 16 },
        xAxis: { type: "value", scale: true, ...nameOf(spec.xLabel) },
        yAxis: { type: "value", scale: true, ...nameOf(spec.yLabel), nameGap: 40 },
        series: [
          ...spec.series.map((s, i) => ({
            type: "scatter",
            name: s.label,
            id: s.id,
            symbol: SYMBOLS[(s.slot ?? i) % SYMBOLS.length],
            symbolSize: 9,
            data: s.points.map((p) => ({ value: [p.x, p.y], name: p.label })),
            itemStyle: { color: seriesColor(theme, s.slot ?? i), borderColor: theme.surface, borderWidth: 1 },
          })),
          ...(front.length
            ? [
                {
                  type: "line",
                  name: "Pareto front",
                  data: front.map((p) => [p.x, p.y]),
                  symbol: "none",
                  silent: true,
                  lineStyle: { color: theme.axis, type: [...dashFor(1)], width: 1 },
                  step: false,
                },
              ]
            : []),
        ],
      };
    }
    case "forest": {
      const ref = spec.reference ?? 0;
      const rows = spec.rows;
      return {
        ...common,
        tooltip: { trigger: "item", confine: true },
        xAxis: { type: "value", scale: true, ...nameOf(spec.xLabel) },
        yAxis: { type: "category", data: rows.map((r) => r.label), inverse: true, axisTick: { show: false } },
        series: [
          {
            type: "custom",
            name: "95 % interval",
            silent: true,
            data: rows.map((r, i) => [i, r.low, r.high]),
            encode: { x: [1, 2], y: 0 },
            renderItem: (_params: unknown, api: CustomApi) => {
              const i = api.value(0);
              const lo = api.coord([api.value(1), i]);
              const hi = api.coord([api.value(2), i]);
              const cap = 5;
              const style = { stroke: theme.axis, lineWidth: 1.5 };
              return {
                type: "group",
                children: [
                  { type: "line", shape: { x1: lo[0], y1: lo[1], x2: hi[0], y2: hi[1] }, style },
                  { type: "line", shape: { x1: lo[0], y1: lo[1] - cap, x2: lo[0], y2: lo[1] + cap }, style },
                  { type: "line", shape: { x1: hi[0], y1: hi[1] - cap, x2: hi[0], y2: hi[1] + cap }, style },
                ],
              };
            },
          },
          {
            type: "scatter",
            name: "Estimate",
            symbol: "diamond",
            symbolSize: 11,
            data: rows.map((r) => r.estimate),
            itemStyle: { color: seriesColor(theme, 0), borderColor: theme.surface, borderWidth: 1 },
            markLine: { symbol: "none", silent: true, data: [{ xAxis: ref }], lineStyle: { color: theme.marker, type: "dashed" }, label: { show: false } },
          },
        ],
      };
    }
    case "parallel": {
      const tooltipValue = (v: number | string | null) => (v == null ? "—" : typeof v === "number" ? f(v) : v);
      return {
        ...common,
        ...legend(spec.lines.length),
        tooltip: {
          trigger: "item",
          confine: true,
          formatter: (p: { seriesName: string; value: (number | string | null)[] }) =>
            `${p.seriesName}<br/>${spec.axes.map((a, i) => `${a.label}: ${tooltipValue(p.value[i] ?? null)}`).join("<br/>")}`,
        },
        parallel: { left: 48, right: 56, top: spec.lines.length > 1 ? 48 : 32, bottom: 24 },
        parallelAxis: spec.axes.map((a, i) => ({
          dim: i,
          name: a.label,
          type: a.type ?? "value",
          ...(a.type === "category" ? { data: a.categories ?? [] } : { scale: true }),
          nameTextStyle: { color: theme.axis },
          axisLine: { lineStyle: { color: theme.axis } },
          axisLabel: { color: theme.axis, ...(a.type === "category" ? {} : { formatter: (v: number) => f(v) }) },
        })),
        series: spec.lines.map((l, i) => {
          const slot = l.slot ?? i;
          return {
            type: "parallel",
            name: l.label,
            id: l.id,
            data: [l.values.map((v) => v ?? "-")],
            // The highlighted line differs by width and a solid stroke, never by colour alone.
            lineStyle: { color: seriesColor(theme, slot), width: l.highlight ? 3.5 : 1.5, opacity: l.highlight ? 1 : 0.8, type: l.highlight ? "solid" : [...dashFor(slot)] },
            emphasis: { lineStyle: { width: 4 } },
            z: l.highlight ? 3 : 2,
          };
        }),
      };
    }
  }
}

function unreachable(x: never): never {
  throw new Error(`unknown chart preset ${JSON.stringify(x)}`);
}

type CustomApi ={ value(dim: number): number; coord(v: [number, number]): [number, number] };

/** The table view of a preset (the same numbers the API returned). */
export function analyticsTable(spec: AnalyticsSpec): Table {
  switch (spec.kind) {
    case "histogram": {
      const bins = spec.series[0]?.bins ?? [];
      return {
        columns: ["Bin start", "Bin end", ...spec.series.map((s) => s.label)],
        rows: bins.map((b, i) => [b.start, b.end, ...spec.series.map((s) => s.bins[i]?.count ?? null)]),
      };
    }
    case "bar":
      return { columns: [spec.xLabel ?? "Category", ...spec.series.map((s) => s.label)], rows: spec.categories.map((c, i) => [c, ...spec.series.map((s) => s.values[i] ?? null)]) };
    case "heatmap":
      return {
        columns: [spec.yLabel ?? "Row", ...spec.x],
        rows: spec.y.map((yl, yi) => [yl, ...spec.x.map((_, xi) => spec.cells.find((c) => c.x === xi && c.y === yi)?.value ?? null)]),
      };
    case "scatter":
      return {
        columns: ["Series", "Point", spec.xLabel ?? "x", spec.yLabel ?? "y"],
        rows: spec.series.flatMap((s) => s.points.map((p) => [s.label, p.label ?? null, p.x, p.y])),
      };
    case "forest":
      return { columns: [spec.yLabel ?? "Row", "Estimate", "Low", "High"], rows: spec.rows.map((r) => [r.label, r.estimate, r.low, r.high]) };
    case "parallel":
      return {
        columns: [spec.yLabel ?? "Line", ...spec.axes.map((a) => a.label)],
        rows: spec.lines.map((l) => [l.highlight ? `${l.label} (highlighted)` : l.label, ...spec.axes.map((_, i) => l.values[i] ?? null)]),
      };
  }
  return unreachable(spec);
}

/** The text summary of a preset (R53 accessibility). */
export function summarizeAnalytics(spec: AnalyticsSpec): string {
  const f = fmtOf(spec);
  const u = (v: number) => withUnit(spec, f(v));
  switch (spec.kind) {
    case "histogram": {
      const parts = spec.series.map((s) => {
        const total = s.bins.reduce((a, b) => a + b.count, 0);
        const peak = s.bins.reduce<Bin | undefined>((p, b) => (!p || b.count > p.count ? b : p), undefined);
        return `${s.label}: ${formatNumber(total, 0)} items in ${s.bins.length} bins${peak ? `, most in ${binLabel(peak, u)} (${formatNumber(peak.count, 0)})` : ""}`;
      });
      const marks = spec.marks?.length ? ` Marks: ${spec.marks.map((m) => `${m.label} at ${u(m.value)}`).join(", ")}.` : "";
      return `${spec.title}: histogram${spec.xLabel ? ` of ${spec.xLabel}` : ""}. ${parts.join("; ")}.${marks}`;
    }
    case "bar": {
      const parts = spec.series.map((s) => {
        let hi = -1;
        let lo = -1;
        s.values.forEach((v, i) => {
          if (v == null) return;
          if (hi < 0 || v > (s.values[hi] as number)) hi = i;
          if (lo < 0 || v < (s.values[lo] as number)) lo = i;
        });
        return hi < 0 ? `${s.label}: no values` : `${s.label}: highest ${spec.categories[hi]} ${u(s.values[hi] as number)}, lowest ${spec.categories[lo]} ${u(s.values[lo] as number)}`;
      });
      return `${spec.title}: ${spec.stacked ? "stacked " : ""}bars over ${spec.categories.length} categories. ${parts.join("; ")}.`;
    }
    case "heatmap": {
      const vals = spec.cells.filter((c) => c.value != null) as { x: number; y: number; value: number }[];
      if (!vals.length) return `${spec.title}: heatmap, ${spec.y.length} by ${spec.x.length}, no values.`;
      const hi = vals.reduce((p, c) => (c.value > p.value ? c : p));
      const lo = vals.reduce((p, c) => (c.value < p.value ? c : p));
      const at = (c: { x: number; y: number }) => `${spec.y[c.y]} × ${spec.x[c.x]}`;
      return `${spec.title}: heatmap, ${spec.y.length} rows by ${spec.x.length} columns. Highest ${u(hi.value)} at ${at(hi)}; lowest ${u(lo.value)} at ${at(lo)}.`;
    }
    case "scatter": {
      const n = spec.series.reduce((a, s) => a + s.points.length, 0);
      const front = spec.front ? paretoFront(spec.series.flatMap((s) => s.points), spec.front) : [];
      return `${spec.title}: scatter of ${n} points in ${spec.series.length} series${spec.xLabel && spec.yLabel ? `, ${spec.yLabel} against ${spec.xLabel}` : ""}.${front.length ? ` ${front.length} points on the Pareto front.` : ""}`;
    }
    case "forest": {
      const ref = spec.reference ?? 0;
      const above = spec.rows.filter((r) => r.low > ref).length;
      const below = spec.rows.filter((r) => r.high < ref).length;
      const across = spec.rows.length - above - below;
      return `${spec.title}: ${spec.rows.length} estimates with intervals against ${formatCell(ref)}. ${above} entirely above, ${below} entirely below, ${across} include it.`;
    }
    case "parallel": {
      const ranges = spec.axes.map((a, i) => {
        if (a.type === "category") {
          const seen = new Set(spec.lines.map((l) => l.values[i]).filter((v): v is string | number => v != null).map(String));
          return `${a.label} takes ${seen.size} value${seen.size === 1 ? "" : "s"}`;
        }
        const vals = spec.lines.map((l) => l.values[i]).filter((v): v is number => typeof v === "number" && Number.isFinite(v));
        return vals.length ? `${a.label} ${f(Math.min(...vals))} to ${f(Math.max(...vals))}` : `${a.label} has no values`;
      });
      const hi = spec.lines.filter((l) => l.highlight).map((l) => l.label);
      return `${spec.title}: parallel coordinates of ${spec.lines.length} lines over ${spec.axes.length} axes. ${ranges.join("; ")}.${hi.length ? ` Highlighted: ${hi.join(", ")}.` : ""}`;
    }
  }
  return unreachable(spec);
}

/** Keyboard-cursor stops in reading order; `rowLength` lets Up/Down move by a row (heatmap). */
export function cursorItems(spec: AnalyticsSpec): { items: CursorItem[]; rowLength: number } {
  const f = fmtOf(spec);
  const u = (v: number | null | undefined) => (v == null ? "no value" : withUnit(spec, f(v)));
  switch (spec.kind) {
    case "histogram": {
      const bins = spec.series[0]?.bins ?? [];
      const items = bins.map((b, i) => ({ seriesIndex: 0, dataIndex: i, text: `${binLabel(b, f)}: ${spec.series.map((s) => `${s.label} ${formatNumber(s.bins[i]?.count ?? 0, 0)}`).join(", ")}` }));
      return { items, rowLength: 1 };
    }
    case "bar":
      return { items: spec.categories.map((c, i) => ({ seriesIndex: 0, dataIndex: i, text: `${c}: ${spec.series.map((s) => `${s.label} ${u(s.values[i])}`).join(", ")}` })), rowLength: 1 };
    case "heatmap":
      return {
        items: spec.cells.map((c, i) => ({ seriesIndex: 0, dataIndex: i, text: `${spec.y[c.y]} × ${spec.x[c.x]}: ${u(c.value)}` })),
        rowLength: spec.x.length || 1,
      };
    case "scatter":
      return {
        items: spec.series.flatMap((s, si) => s.points.map((p, i) => ({ seriesIndex: si, dataIndex: i, text: `${s.label}${p.label ? ` ${p.label}` : ""}: ${spec.xLabel ?? "x"} ${f(p.x)}, ${spec.yLabel ?? "y"} ${f(p.y)}` }))),
        rowLength: 1,
      };
    case "forest":
      return { items: spec.rows.map((r, i) => ({ seriesIndex: 1, dataIndex: i, text: `${r.label}: ${u(r.estimate)}, interval ${f(r.low)} to ${f(r.high)}` })), rowLength: 1 };
    case "parallel":
      return {
        items: spec.lines.map((l, si) => ({
          seriesIndex: si,
          dataIndex: 0,
          text: `${l.label}${l.highlight ? " (highlighted)" : ""}: ${spec.axes.map((a, i) => { const v = l.values[i]; return `${a.label} ${v == null ? "no value" : typeof v === "number" ? f(v) : v}`; }).join(", ")}`,
        })),
        rowLength: 1,
      };
  }
  return unreachable(spec);
}
