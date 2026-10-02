# `@/shell/charts`

The chart primitive (R53 in `docs/spec/08-resolutions.md`). The only module that imports uPlot (MIT) and Apache
ECharts 6 (Apache-2.0; its renderer zrender is BSD-3). Panels import from `@/shell/charts` (the index) and nothing
else: ESLint rejects `uplot`, `echarts`, `zrender` and `@/shell/charts/*` in panels, and the chart libraries anywhere
outside this directory.

Chart data is contract data: histograms, buckets, intervals and aggregates arrive binned from the API. The browser
only zooms, smooths and switches scales.

## `TimeSeriesChart` (uPlot)

For metrics, latency traces and GPU telemetry, including live data.

```tsx
import { TimeSeriesChart, type TimeSeries, type ChartMarker } from "@/shell/charts";

<TimeSeriesChart
  title="Validation WER"            // accessible name, visible title, table caption
  series={runs}                      // TimeSeries[]
  xKey="step"                        // "step" | "epoch" | "wallTime" | "gpuHours"
  yLabel="WER" unit="%"
  yScale="linear"                    // or "log" (values ≤ 0 become gaps and the summary says how many)
  smoothing={0.6}                    // EMA weight in [0, 1); raw line stays faint underneath
  envelope="auto"                    // "auto" | "on" | "off": min/max band when > 2 points per pixel
  markers={checkpoints}              // ChartMarker[]: dashed line + glyph (▼ checkpoint, ◆ best, ● event)
  syncKey="run-123"                  // shared cursor and x zoom across charts with the same key
  height={220}                       // optional; by default fills the parent (give it a height)
  formatY={(v) => `${v.toFixed(1)}`} // optional
  hideTitle                          // optional: the panel already names it (stays the accessible name)
/>
```

```ts
type TimeSeries = {
  id: string;        // stable identity: colour and dash follow it
  label: string;
  slot?: number;     // categorical slot (default: position). Pass it so a filter never repaints survivors
  x: Partial<Record<XKey, ArrayLike<number>>>;  // parallel arrays, wallTime in Unix seconds
  y: ArrayLike<number | null>;
};
type ChartMarker = { id: string; label: string; kind?: "checkpoint" | "best" | "event"; x: Partial<Record<XKey, number>> };
```

Behaviour:

- **Live append**: pass a new `series` array (new identity) whenever points arrive. The chart redraws at most every
  250 ms (≤ 4 per second): the first change right away, a burst once more at the end. The user's zoom is kept while
  live data arrives; "Reset zoom" or a double click restores it.
- **Structure** (series ids and slots, `xKey`, `yScale`, `syncKey`) re-creates the uPlot instance; data, theme,
  smoothing and markers do not.
- **Dense series**: with `envelope="auto"` and more than two points per pixel, every series is binned into
  equal-width buckets (half the plot width in px). A faint band shows min–max; the line shows the bucket mean (of the
  EMA when smoothing).
- **Legend**: buttons that show and hide a series (aria-pressed), each with its dash sample and the value at the
  cursor (last value otherwise).
- **Keyboard**: the plot is focusable; ←/→ move the cursor (Shift: 10 points), PageUp/PageDown a tenth, Home/End the
  ends, Escape hides it. Each move is read out in a polite live region (x, each series' value, raw or range, markers).
- **Popouts and panels**: size follows the element with its own window's `ResizeObserver`; the element may live in a
  popout document (Dockview moves the DOM). uPlot's rect is re-synced on the popout window's resize and scroll, and a
  zoom drag that ends in the popout is forwarded to uPlot.

## `AnalyticsChart` (ECharts, lazy)

For histograms, bars, heatmaps, scatter/Pareto, forest plots, parallel coordinates and lines (ECDFs, WER against
latency). ECharts loads on first render (its own chunk);
a small placeholder shows meanwhile.

```tsx
import { AnalyticsChart, type AnalyticsSpec } from "@/shell/charts";

<AnalyticsChart spec={spec} height={240} hideTitle={false} />
```

| `kind` | Fields |
| --- | --- |
| `histogram` | `series: { id, label, slot?, bins: { start, end, count }[] }[]` (overlay versions: same bin edges), `marks?: { label, value }[]` (filter bounds, percentiles; drawn at the bin containing the value) |
| `bar` | `categories: string[]`, `series: { id, label, slot?, values: (number \| null)[] }[]`, `stacked?`, `horizontal?` |
| `heatmap` | `x: string[]`, `y: string[]`, `cells: { x, y, value \| null }[]` (indexes), `colormap?: "magma" \| "viridis" \| "diverging"`, `range?`, `center?` (diverging: symmetric around it, default 0) |
| `scatter` | `series: { id, label, slot?, points: { x, y, label? }[] }[]`, `front?: { x: "min" \| "max", y: "min" \| "max" }` (Pareto front line) |
| `forest` | `rows: { label, estimate, low, high }[]`, `reference?` (default 0) |
| `line` | `series: { id, label, slot?, step?, points: { x, y, low?, high?, label?, marked? }[] }[]`, `xMarks?: { label, value }[]` (vertical reference lines), `xType?: "value" \| "log"`, `yRange?` (a step series is an ECDF, drawn without symbols; `low`/`high` draw vertical interval bars in the series' colour; a marked point is larger and labelled with ★) |
| `parallel` | `axes: { id, label, type?: "value" \| "log" \| "category", categories? }[]`, `lines: { id, label, slot?, values: (number \| string \| null)[], highlight? }[]` (one line per item, values in axis order; a highlighted line is wider and solid — the best run of an Experiment) |

All kinds take `title`, `xLabel?`, `yLabel?`, `unit?`, `format?` and `note?` (appended to the text summary: what the
data covers, how an interval was derived). ARIA is on (the text summary is its
description) and decal patterns are on for every series; scatter and line series also differ by symbol (lines by dash too), heatmap cells print
their values when there are at most 144. A theme switch calls `setTheme` on the live instance; nothing is
re-created. Keyboard: ←/→ step through values, ↑/↓ move a row in heatmaps, Home/End, Escape; each stop is read out.

## Shared by every chart

- **Table** / **Copy CSV** in the chart's toolbar: the table shows the raw numbers (never binned, first 500 rows);
  CSV copies every row (RFC 4180) with the clipboard of the window the chart is in.
- **Text summary**: linked with `aria-describedby` and shown as the table caption (series, ranges, extremes, trend,
  markers; or per-preset highlights).
- **Colour is never the only channel**: dashes per slot, symbols, decals, legend labels, values in cells.

## Tokens

`theme.css` holds the colours; `tokens.ts` names them and resolves them (display-p3 → sRGB) for canvas and ECharts.

| Token | Use |
| --- | --- |
| `--cadence-chart-1` … `-8` | Categorical series in fixed order: crimson, violet, bronze, plum, lime, sky, orange, teal (step 9, dark 10; lime/sky 11 and orange/teal 10 in light, where step 9 is under 3:1) |
| `--cadence-chart-axis`, `-grid`, `-cursor`, `-marker` | Axes and labels (slate-11), grid (slate-a4, decorative), crosshair (slate-9), checkpoint marks (slate-11) |
| `--cadence-chart-magma-0…8`, `--cadence-chart-viridis-0…8` | Sequential ramps, theme-independent |
| `--cadence-chart-div-0…8` | Diverging blue → slate → orange (Radix steps, so dark mode has its own) |

TS: `readChartTheme(el)` returns every token as the element sees it; `seriesColor(theme, slot)`, `dashFor(slot)`,
`categoricalVar(slot)`, `CATEGORICAL_COUNT`. A ninth series reuses slot 1's colour with another dash.

`make contrast` (and `make test`) checks each categorical colour at ≥ 3:1 on slate-1 and slate-2 in both modes, and
runs a Machado (2009) colour-vision-deficiency simulation: neighbouring series ≥ 8 ΔE (OKLab ×100) under protanopia
and deuteranopia, ≥ 6 under tritanopia, ≥ 15 in normal vision, any two series ≥ 8; the diverging poles ≥ 15; the
sequential ramps strictly increase in lightness. The list in `scripts/contrast.mjs` must equal `theme.css`.

## Tests

- `charts.test.ts` (jsdom): EMA, envelope binning, alignment, frame building, table/CSV, summaries, throttle,
  presets.
- `charts.browser.test.tsx` (headless Chromium): both chart kinds render; theme switch keeps the canvas and changes
  the pixels; keyboard readout; table view; resize; both kinds inside a second document (iframe) like a popout.
