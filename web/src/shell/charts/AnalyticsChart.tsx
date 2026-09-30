import { lazy, Suspense } from "react";
import type { AnalyticsChartProps } from "./AnalyticsChartImpl";

// ECharts (≈ 300 KB minified for the presets) loads on first use, not with the shell.
const Impl = lazy(() => import("./AnalyticsChartImpl").then((m) => ({ default: m.AnalyticsChartImpl })));

/** Apache ECharts analytics presets (R53): histogram, bar, heatmap, scatter, forest plot — from binned API data. */
export function AnalyticsChart(props: AnalyticsChartProps) {
  return (
    <Suspense fallback={<div className="flex h-full min-h-24 items-center justify-center text-xs text-muted-foreground">Loading chart…</div>}>
      <Impl {...props} />
    </Suspense>
  );
}

export type { AnalyticsChartProps };
