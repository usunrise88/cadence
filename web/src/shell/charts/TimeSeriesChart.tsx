import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import "./charts.css";
import { Button } from "@/components/ui/button";
import { ChartFrame } from "./ChartFrame";
import { useChartTheme, useElementSize, useLatest, useOwnerDocument } from "./hooks";
import { nearestDefined, nearestIndex, type Num } from "./math";
import { buildFrame, formatX, summarizeTimeSeries, timeSeriesTable, X_LABELS, type ChartMarker, type EnvelopeMode, type Frame, type TimeSeries, type XKey } from "./series";
import { formatNumber } from "./table";
import { createThrottle } from "./throttle";
import { dashFor, seriesColor, withAlpha, type ChartTheme } from "./tokens";

export type TimeSeriesChartProps = {
  /** Accessible name, visible title, table caption. */
  title: string;
  hideTitle?: boolean;
  series: TimeSeries[];
  /** The x field to draw against. */
  xKey: XKey;
  yLabel?: string;
  unit?: string;
  yScale?: "linear" | "log";
  /** EMA weight in [0, 1): 0 draws the raw series; above 0 the raw line stays faint under the smoothed one. */
  smoothing?: number;
  /** Min/max envelope: `auto` when a series has more than two points per pixel. */
  envelope?: EnvelopeMode;
  markers?: ChartMarker[];
  /** Charts with the same key share the cursor and the x zoom (the Metrics panel's stacked charts). */
  syncKey?: string;
  /** Fixed plot height in px; by default the chart fills its parent. */
  height?: number;
  formatY?: (v: number) => string;
  className?: string;
};

const COLS_PER_LINE = 4; // min, max, raw, main (see buildFrame)
const MARKER_GLYPH: Record<string, string> = { checkpoint: "▼", best: "◆", event: "●" };

function toData(frame: Frame): uPlot.AlignedData {
  const cols: Num[][] = [];
  for (const l of frame.lines) cols.push(l.min, l.max, l.raw, l.main);
  return [frame.x, ...cols] as uPlot.AlignedData;
}

/** uPlot time series (R53): synced cursors, EMA over a faint raw line, min/max envelopes, markers, log/linear y. */
export function TimeSeriesChart(props: TimeSeriesChartProps) {
  const { title, hideTitle, series, xKey, yLabel, unit, yScale = "linear", smoothing = 0, envelope = "auto", markers = [], syncKey, height, formatY, className } = props;
  const [plotEl, setPlotEl] = useState<HTMLDivElement | null>(null);
  const doc = useOwnerDocument(plotEl);
  const theme = useChartTheme(plotEl, doc);
  const size = useElementSize(plotEl, doc);
  const plotRef = useRef<uPlot | null>(null);
  const log = yScale === "log";

  // Live data: redraw at most 4 times per second. `shown` is what the chart, legend and summary use.
  const [shown, setShown] = useState(series);
  const latest = useLatest(series);
  const throttle = useMemo(() => createThrottle(() => setShown(latest.current)), [latest]);
  useEffect(() => {
    if (series !== shown) throttle.schedule();
  }, [series, shown, throttle]);
  useEffect(() => () => throttle.cancel(), [throttle]);

  const frame = useMemo(() => buildFrame(shown, { xKey, smoothing, envelope, log, widthPx: size.width }), [shown, xKey, smoothing, envelope, log, size.width]);
  const frameRef = useLatest(frame);
  const themeRef = useLatest(theme);
  const markersRef = useLatest(markers);
  const fmtY = useCallback((v: number) => (formatY ? formatY(v) : formatNumber(v)), [formatY]);
  const fmtRef = useLatest(fmtY);

  const [cursorIdx, setCursorIdx] = useState<number | null>(null);
  const [hidden, setHidden] = useState<Record<string, boolean>>({});
  const [zoomed, setZoomed] = useState(false);
  const zoomedRef = useLatest(zoomed);
  const [announce, setAnnounce] = useState("");

  // Structure: uPlot is re-created only when the set of lines, the x field, the y scale or the sync key changes.
  const structure = `${shown.map((s, i) => `${s.id}:${s.slot ?? i}`).join("|")}#${xKey}#${yScale}#${syncKey ?? ""}#${height ?? ""}`;

  useLayoutEffect(() => {
    if (!plotEl) return;
    const lines = frameRef.current.lines;
    const color = (slot: number) => (themeRef.current ? seriesColor(themeRef.current, slot) : "currentColor");
    const s: uPlot.Series[] = [{ label: X_LABELS[xKey] }];
    const bands: uPlot.Band[] = [];
    lines.forEach((l, i) => {
      const base = 1 + i * COLS_PER_LINE;
      s.push(
        { label: `${l.label} min`, stroke: () => "transparent", width: 0, points: { show: false } },
        { label: `${l.label} max`, stroke: () => "transparent", width: 0, points: { show: false } },
        { label: `${l.label} raw`, stroke: () => withAlpha(color(l.slot), 0.3), width: 1, points: { show: false } },
        { label: l.label, stroke: () => color(l.slot), width: 2, dash: [...dashFor(l.slot)], points: { show: false }, spanGaps: true },
      );
      bands.push({ series: [base + 1, base], fill: () => withAlpha(color(l.slot), 0.18) });
    });
    const axis = () => themeRef.current?.axis ?? "currentColor";
    const grid = () => themeRef.current?.grid ?? "transparent";
    const axisBase: uPlot.Axis = {
      stroke: axis,
      grid: { stroke: grid, width: 1 },
      ticks: { stroke: grid, width: 1, size: 4 },
      font: "11px system-ui, sans-serif",
      labelFont: "11px system-ui, sans-serif",
    };
    const opts: uPlot.Options = {
      width: Math.max(1, plotEl.clientWidth),
      height: Math.max(1, height ?? plotEl.clientHeight),
      legend: { show: false },
      pxAlign: 0,
      scales: {
        x: { time: xKey === "wallTime" },
        y: log ? { distr: 3, log: 10 } : { auto: true },
      },
      axes: [
        { ...axisBase, values: xKey === "wallTime" ? undefined : (_u, ticks) => ticks.map((t) => (t == null ? "" : formatX(t, xKey))) },
        { ...axisBase, label: yLabel ? `${yLabel}${unit ? ` (${unit})` : ""}` : undefined, size: 56, values: (_u, ticks) => ticks.map((t) => (t == null ? "" : fmtRef.current(t))) },
      ],
      series: s,
      bands,
      cursor: {
        sync: syncKey ? { key: syncKey, scales: ["x", null] } : undefined,
        drag: { x: true, y: false, setScale: true },
        points: { show: false },
        focus: { prox: 16 },
      },
      hooks: {
        draw: [(u) => drawMarkers(u, markersRef.current, xKey, themeRef.current)],
        setCursor: [(u) => setCursorIdx(u.cursor.idx ?? null)],
        setSelect: [
          (u) => {
            if (u.select.width > 0) setZoomed(true);
          },
        ],
      },
    };
    // The callback form: uPlot tests `instanceof HTMLElement`, which fails for elements of another window (popouts).
    const u = new uPlot(opts, toData(frameRef.current), (self, init) => {
      plotEl.appendChild(self.root);
      init();
    });
    plotRef.current = u;
    const ownerDoc = plotEl.ownerDocument;
    const view = ownerDoc.defaultView;
    // Popouts: uPlot listens on the opener's window for resize/scroll and on its document for mouseup while
    // dragging a zoom. Re-sync the cached rect from the popout's own window and forward its mouseup.
    const resync = () => u.syncRect(true);
    const forwardUp = (e: MouseEvent) => {
      document.dispatchEvent(new MouseEvent("mouseup", { clientX: e.clientX, clientY: e.clientY, button: e.button, buttons: e.buttons, bubbles: true }));
    };
    const onDbl = () => setZoomed(false);
    u.over.addEventListener("dblclick", onDbl);
    if (view && ownerDoc !== document) {
      view.addEventListener("resize", resync);
      view.addEventListener("scroll", resync, true);
      ownerDoc.addEventListener("mouseup", forwardUp);
    }
    return () => {
      u.over.removeEventListener("dblclick", onDbl);
      if (view && ownerDoc !== document) {
        view.removeEventListener("resize", resync);
        view.removeEventListener("scroll", resync, true);
        ownerDoc.removeEventListener("mouseup", forwardUp);
      }
      u.destroy();
      plotRef.current = null;
    };
    // `structure` stands for the series set; data, theme and markers flow in without re-creating the plot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [plotEl, structure, doc]);

  // Data (throttled upstream): keep the user's zoom while live points arrive.
  useLayoutEffect(() => {
    plotRef.current?.setData(toData(frame), !zoomedRef.current);
  }, [frame, zoomedRef]);

  // Theme: series and axis colours are functions of the current tokens; a redraw applies them.
  useLayoutEffect(() => {
    const u = plotRef.current;
    if (!u || !theme) return;
    u.redraw(false, true);
  }, [theme, markers]);

  // Size: follow the panel (and popout window) size.
  useLayoutEffect(() => {
    const u = plotRef.current;
    if (!u || size.width <= 0) return;
    const h = height ?? size.height;
    if (h > 0 && (u.width !== size.width || u.height !== h)) u.setSize({ width: size.width, height: h });
  }, [size, height]);

  // Legend visibility toggles every column of a line.
  useLayoutEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    frame.lines.forEach((l, i) => {
      const show = !hidden[l.id];
      for (let k = 0; k < COLS_PER_LINE; k++) {
        const idx = 1 + i * COLS_PER_LINE + k;
        if (u.series[idx] && u.series[idx].show !== show) u.setSeries(idx, { show });
      }
    });
  }, [hidden, frame.lines, structure]);

  const resetZoom = () => {
    const u = plotRef.current;
    if (!u) return;
    setZoomed(false);
    u.setData(toData(frameRef.current), true);
  };

  const readout = useCallback(
    (idx: number): string => {
      const f = frameRef.current;
      const x = f.x[idx];
      if (x == null) return "";
      const parts = f.lines
        .filter((l) => !hidden[l.id])
        .map((l) => {
          const v = l.main[idx];
          const range = f.dense && l.min[idx] != null && l.max[idx] != null ? ` (range ${fmtY(l.min[idx] as number)} to ${fmtY(l.max[idx] as number)})` : "";
          const raw = !f.dense && f.smoothed && l.raw[idx] != null ? ` (raw ${fmtY(l.raw[idx] as number)})` : "";
          return `${l.label} ${v == null ? "no value" : fmtY(v)}${f.smoothed ? " smoothed" : ""}${raw}${range}`;
        });
      const near = markersRef.current.filter((m) => {
        const mx = m.x[xKey];
        return mx != null && nearestIndex(f.x, mx) === idx;
      });
      const mk = near.length ? `; ${near.map((m) => m.label).join(", ")}` : "";
      return `${X_LABELS[xKey]} ${formatX(x, xKey)}${f.dense ? " (bucket centre)" : ""}: ${parts.join("; ")}${mk}`;
    },
    [frameRef, markersRef, hidden, fmtY, xKey],
  );

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const u = plotRef.current;
    const f = frameRef.current;
    if (!u || !f.x.length) return;
    const n = f.x.length;
    const cur = u.cursor.idx ?? cursorIdx ?? -1;
    const page = Math.max(1, Math.round(n / 10));
    let next: number;
    switch (e.key) {
      case "ArrowRight":
        next = cur < 0 ? 0 : cur + (e.shiftKey ? 10 : 1);
        break;
      case "ArrowLeft":
        next = cur < 0 ? n - 1 : cur - (e.shiftKey ? 10 : 1);
        break;
      case "PageDown":
        next = (cur < 0 ? 0 : cur) + page;
        break;
      case "PageUp":
        next = (cur < 0 ? n - 1 : cur) - page;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = n - 1;
        break;
      case "Escape":
        u.setCursor({ left: -10, top: -10 });
        setAnnounce("");
        e.preventDefault();
        return;
      default:
        return;
    }
    e.preventDefault();
    e.stopPropagation();
    next = Math.min(n - 1, Math.max(0, next));
    const main = f.lines.find((l) => !hidden[l.id])?.main;
    const yi = main ? nearestDefined(main, next) : -1;
    const yv = yi >= 0 && main ? main[yi] : null;
    const left = u.valToPos(f.x[next] as number, "x");
    const top = yv != null ? u.valToPos(yv, "y") : u.bbox.height / (2 * uPlot.pxRatio);
    u.setCursor({ left, top });
    setCursorIdx(next);
    setAnnounce(readout(next));
  };

  const summary = useMemo(
    () => summarizeTimeSeries(title, shown, { xKey, smoothing, log, unit, format: formatY }, markers),
    [title, shown, xKey, smoothing, log, unit, formatY, markers],
  );
  const table = useCallback(() => timeSeriesTable(shown, xKey, smoothing, markers), [shown, xKey, smoothing, markers]);
  const skipped = Object.values(frame.skipped).reduce((a, b) => a + b, 0);

  return (
    <ChartFrame
      title={title}
      hideTitle={hideTitle}
      className={className}
      summary={skipped ? `${summary} ${skipped} values at or below zero are not shown on the log scale.` : summary}
      table={table}
      announce={announce}
      plotHeight={height}
      toolbar={
        zoomed ? (
          <Button size="xs" variant="ghost" onClick={resetZoom}>
            Reset zoom
          </Button>
        ) : null
      }
      legend={<Legend theme={theme} frame={frame} idx={cursorIdx} hidden={hidden} setHidden={setHidden} fmt={fmtY} markers={markers} xKey={xKey} />}
    >
      {(describedBy) => (
        <div
          ref={setPlotEl}
          className="cadence-chart-plot absolute inset-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          tabIndex={0}
          role="application"
          aria-roledescription="chart"
          aria-label={`${title}. Arrow keys move the cursor, Shift for 10 points, Home and End jump, Escape clears.`}
          aria-describedby={describedBy}
          data-testid="timeseries-plot"
          onKeyDown={onKeyDown}
        />
      )}
    </ChartFrame>
  );
}

type LegendProps = {
  theme: ChartTheme | null;
  frame: Frame;
  idx: number | null;
  hidden: Record<string, boolean>;
  setHidden: (f: (h: Record<string, boolean>) => Record<string, boolean>) => void;
  fmt: (v: number) => string;
  markers: ChartMarker[];
  xKey: XKey;
};

function Legend({ theme, frame, idx, hidden, setHidden, fmt, markers, xKey }: LegendProps) {
  const kinds = [...new Set(markers.filter((m) => m.x[xKey] != null).map((m) => m.kind ?? "checkpoint"))];
  return (
    <ul className="m-0 flex list-none flex-wrap items-center gap-x-1 p-0 text-xs text-muted-foreground" aria-label="Series">
      {frame.lines.map((l) => {
        const color = theme ? seriesColor(theme, l.slot) : "currentColor";
        const at = idx != null ? l.main[idx] : lastDefined(l.main);
        const off = !!hidden[l.id];
        return (
          <li key={l.id}>
            <button
              type="button"
              aria-pressed={!off}
              onClick={() => setHidden((h) => ({ ...h, [l.id]: !off }))}
              className={`inline-flex min-h-6 items-center gap-1.5 rounded-sm px-1 hover:bg-muted ${off ? "opacity-50 line-through" : ""}`}
            >
              <svg width="18" height="8" aria-hidden="true">
                <line x1="0" y1="4" x2="18" y2="4" stroke={color} strokeWidth="2" strokeDasharray={dashFor(l.slot).join(" ") || undefined} />
              </svg>
              <span className="text-foreground">{l.label}</span>
              {at != null && <span className="tabular-nums">{fmt(at)}</span>}
            </button>
          </li>
        );
      })}
      {kinds.map((k) => (
        <li key={k} className="inline-flex min-h-6 items-center gap-1 px-1">
          <span aria-hidden="true">{MARKER_GLYPH[k]}</span>
          <span>{k === "checkpoint" ? "Checkpoints" : k === "best" ? "Best" : "Events"}</span>
        </li>
      ))}
    </ul>
  );
}

function lastDefined(col: Num[]): Num {
  for (let i = col.length - 1; i >= 0; i--) if (col[i] != null) return col[i] as number;
  return null;
}

/** Checkpoint marks: a dashed vertical line and a glyph at the top (shape carries the kind, not colour). */
function drawMarkers(u: uPlot, markers: ChartMarker[], xKey: XKey, theme: ChartTheme | null) {
  if (!markers.length) return;
  const ctx = u.ctx;
  const { left, top, width, height } = u.bbox;
  const px = uPlot.pxRatio;
  ctx.save();
  ctx.beginPath();
  ctx.rect(left, top, width, height);
  ctx.clip();
  ctx.strokeStyle = theme?.marker ?? "currentColor";
  ctx.fillStyle = theme?.marker ?? "currentColor";
  ctx.lineWidth = px;
  ctx.setLineDash([3 * px, 3 * px]);
  for (const m of markers) {
    const v = m.x[xKey];
    if (v == null) continue;
    const x = Math.round(u.valToPos(v, "x", true));
    if (x < left || x > left + width) continue;
    ctx.beginPath();
    ctx.moveTo(x, top);
    ctx.lineTo(x, top + height);
    ctx.stroke();
    const s = 4 * px;
    ctx.beginPath();
    const kind = m.kind ?? "checkpoint";
    if (kind === "checkpoint") {
      ctx.moveTo(x - s, top);
      ctx.lineTo(x + s, top);
      ctx.lineTo(x, top + s * 1.5);
    } else if (kind === "best") {
      ctx.moveTo(x, top);
      ctx.lineTo(x + s, top + s);
      ctx.lineTo(x, top + 2 * s);
      ctx.lineTo(x - s, top + s);
    } else {
      ctx.arc(x, top + s, s * 0.8, 0, Math.PI * 2);
    }
    ctx.closePath();
    ctx.fill();
  }
  ctx.restore();
}
