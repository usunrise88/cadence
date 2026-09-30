// The ECharts half of @/shell/charts, loaded lazily (AnalyticsChart.tsx) so ECharts stays out of the main bundle.
// Tree-shaken: only the chart types and components the presets use are registered.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { BarChart, CustomChart, HeatmapChart, LineChart, ScatterChart } from "echarts/charts";
import { AriaComponent, GridComponent, LegendComponent, MarkLineComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { init, use as registerEcharts, type ECharts } from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import { analyticsTable, buildOption, cursorItems, echartsTheme, summarizeAnalytics, type AnalyticsSpec } from "./analytics";
import { ChartFrame } from "./ChartFrame";
import { useChartTheme, useElementSize, useOwnerDocument } from "./hooks";

registerEcharts([BarChart, HeatmapChart, ScatterChart, LineChart, CustomChart, GridComponent, TooltipComponent, LegendComponent, VisualMapComponent, MarkLineComponent, AriaComponent, CanvasRenderer]);

export type AnalyticsChartProps = {
  spec: AnalyticsSpec;
  hideTitle?: boolean;
  /** Fixed plot height in px; by default the chart fills its parent. */
  height?: number;
  className?: string;
};

export function AnalyticsChartImpl({ spec, hideTitle, height, className }: AnalyticsChartProps) {
  const [el, setEl] = useState<HTMLDivElement | null>(null);
  // ECharts draws into an inner element: its ARIA component writes aria-label there, so the focusable outer element
  // keeps its own name and the keyboard hint.
  const [canvasEl, setCanvasEl] = useState<HTMLDivElement | null>(null);
  const doc = useOwnerDocument(el);
  const theme = useChartTheme(el, doc);
  const size = useElementSize(el, doc);
  const chartRef = useRef<ECharts | null>(null);
  const [cursor, setCursor] = useState(-1);
  const [announce, setAnnounce] = useState("");
  const summary = useMemo(() => summarizeAnalytics(spec), [spec]);
  const items = useMemo(() => cursorItems(spec), [spec]);

  // One instance per element (and per document: a popout moves the element, the canvas follows it).
  useLayoutEffect(() => {
    if (!canvasEl) return;
    const chart = init(canvasEl, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    return () => {
      chart.dispose();
      chartRef.current = null;
    };
  }, [canvasEl, doc]);

  // Theme first, then the option; a theme switch calls setTheme on the live instance (never re-created).
  const themed = useRef<unknown>(null);
  useLayoutEffect(() => {
    const chart = chartRef.current;
    if (!chart || !theme) return;
    if (themed.current !== theme) {
      chart.setTheme(echartsTheme(theme));
      themed.current = theme;
    }
    chart.setOption(buildOption(spec, theme, summary), { notMerge: true, lazyUpdate: false });
  }, [spec, theme, summary, doc]);

  useEffect(() => {
    if (size.width > 0 && size.height > 0) chartRef.current?.resize({ width: size.width, height: size.height });
  }, [size]);

  const move = useCallback(
    (next: number) => {
      const chart = chartRef.current;
      const it = items.items[next];
      if (!chart || !it) return;
      const prev = items.items[cursor];
      if (prev) chart.dispatchAction({ type: "downplay", seriesIndex: prev.seriesIndex, dataIndex: prev.dataIndex });
      chart.dispatchAction({ type: "highlight", seriesIndex: it.seriesIndex, dataIndex: it.dataIndex });
      chart.dispatchAction({ type: "showTip", seriesIndex: it.seriesIndex, dataIndex: it.dataIndex });
      setCursor(next);
      setAnnounce(it.text);
    },
    [items, cursor],
  );

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const n = items.items.length;
    if (!n) return;
    const row = items.rowLength;
    const cur = cursor;
    let next: number;
    switch (e.key) {
      case "ArrowRight":
        next = cur < 0 ? 0 : cur + 1;
        break;
      case "ArrowLeft":
        next = cur < 0 ? n - 1 : cur - 1;
        break;
      case "ArrowDown":
        next = cur < 0 ? 0 : cur + row;
        break;
      case "ArrowUp":
        next = cur < 0 ? 0 : cur - row;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = n - 1;
        break;
      case "Escape": {
        const prev = items.items[cur];
        if (prev) chartRef.current?.dispatchAction({ type: "downplay", seriesIndex: prev.seriesIndex, dataIndex: prev.dataIndex });
        chartRef.current?.dispatchAction({ type: "hideTip" });
        setCursor(-1);
        setAnnounce("");
        e.preventDefault();
        return;
      }
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
    move(Math.min(n - 1, Math.max(0, next)));
  };

  const table = useCallback(() => analyticsTable(spec), [spec]);

  return (
    <ChartFrame title={spec.title} hideTitle={hideTitle} className={className} summary={summary} table={table} announce={announce} plotHeight={height}>
      {(describedBy) => (
        <div
          ref={setEl}
          className="absolute inset-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          tabIndex={0}
          role="application"
          aria-roledescription="chart"
          aria-label={`${spec.title}. Arrow keys move between values, Home and End jump, Escape clears.`}
          aria-describedby={describedBy}
          data-testid="analytics-plot"
          data-chart-kind={spec.kind}
          onKeyDown={onKeyDown}
        >
          <div ref={setCanvasEl} className="absolute inset-0" data-testid="analytics-canvas" />
        </div>
      )}
    </ChartFrame>
  );
}
