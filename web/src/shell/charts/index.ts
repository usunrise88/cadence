// @/shell/charts — the only module that imports uPlot and ECharts (R53). Panels import charts from here; lint
// rejects `uplot` and `echarts` anywhere else. API: README.md in this directory.
export { TimeSeriesChart, type TimeSeriesChartProps } from "./TimeSeriesChart";
export { AnalyticsChart, type AnalyticsChartProps } from "./AnalyticsChart";
export type { AnalyticsSpec, BarSpec, Bin, ForestSpec, HeatmapSpec, HistogramSpec, ScatterSpec } from "./analytics";
export { X_LABELS, type ChartMarker, type EnvelopeMode, type MarkerKind, type TimeSeries, type XKey } from "./series";
export { ema } from "./math";
export { REDRAW_INTERVAL_MS } from "./throttle";
export { CATEGORICAL_COUNT, categoricalVar, dashFor, readChartTheme, seriesColor, type ChartTheme, type Colormap } from "./tokens";
