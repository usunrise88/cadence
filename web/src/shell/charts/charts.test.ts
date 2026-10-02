import { describe, expect, it, vi } from "vitest";
import { analyticsTable, buildOption, cursorItems, heatmapRange, paretoFront, summarizeAnalytics, type AnalyticsSpec } from "./analytics";
import { align, bucketCentres, ema, envelope, logSafe, nearestDefined, nearestIndex, sortedPoints } from "./math";
import { buildFrame, summarizeTimeSeries, timeSeriesTable, trend, type TimeSeries } from "./series";
import { formatNumber, toCsv } from "./table";
import { createThrottle, type Clock } from "./throttle";
import { categoricalVar, dashFor, parseColor, withAlpha, type ChartTheme } from "./tokens";

const theme: ChartTheme = {
  dark: false,
  categorical: ["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"],
  axis: "axis",
  grid: "grid",
  cursor: "cursor",
  marker: "marker",
  text: "text",
  textSecondary: "text2",
  surface: "surface",
  border: "border",
  sequential: { magma: ["m0", "m8"], viridis: ["v0", "v8"] },
  diverging: ["d0", "d4", "d8"],
};

describe("ema", () => {
  it("is the identity at weight 0", () => {
    expect(ema([1, 2, 3], 0)).toEqual([1, 2, 3]);
  });
  it("debiases the start (TensorBoard smoothing)", () => {
    const out = ema([10, 10, 10], 0.9);
    for (const v of out) expect(v).toBeCloseTo(10, 10);
  });
  it("smooths a step and keeps gaps as gaps without resetting", () => {
    const out = ema([0, null, 10, NaN, 10], 0.5);
    expect(out[1]).toBeNull();
    expect(out[3]).toBeNull();
    // 0 → (0*.5 + 10*.5)/(1-.25) = 6.667 → (5*.5 + 10*.5)/(1-.125) = 8.571
    expect(out[2]).toBeCloseTo(6.6667, 3);
    expect(out[4]).toBeCloseTo(8.5714, 3);
  });
});

describe("envelope binning", () => {
  it("keeps min, max, mean and last per equal-width bucket", () => {
    const x = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9];
    const y = [5, 1, 9, 3, 2, 8, 4, 4, 7, 0];
    const e = envelope(x, y, 0, 9, 2);
    expect(e.min).toEqual([1, 0]);
    expect(e.max).toEqual([9, 8]);
    expect(e.mean[0]).toBeCloseTo(4, 10); // 5 1 9 3 2 → 20 / 5
    expect(e.last).toEqual([2, 0]);
  });
  it("leaves empty buckets as gaps and ignores points outside the range", () => {
    const e = envelope([0, 10, 99], [1, 2, 3], 0, 10, 5);
    expect(e.min).toEqual([1, null, null, null, 2]);
  });
  it("bucket centres span the range", () => {
    expect(bucketCentres(0, 10, 5)).toEqual([1, 3, 5, 7, 9]);
  });
});

describe("alignment and lookups", () => {
  it("sorts points, keeps the last value of a repeated x and drops non-finite x", () => {
    expect(sortedPoints([3, 1, 1, NaN, 2], [30, 10, 11, 99, 20])).toEqual({ x: [1, 2, 3], y: [11, 20, 30] });
  });
  it("aligns series on the union of x", () => {
    const a = align([
      { x: [1, 3], ys: [[10, 30]] },
      { x: [2, 3], ys: [[20, 31]] },
    ]);
    expect(a.x).toEqual([1, 2, 3]);
    expect(a.columns).toEqual([
      [10, null, 30],
      [null, 20, 31],
    ]);
  });
  it("finds the nearest x and the nearest defined value", () => {
    expect(nearestIndex([0, 10, 20], 14)).toBe(1);
    expect(nearestIndex([0, 10, 20], 16)).toBe(2);
    expect(nearestDefined([null, null, 3, null], 0)).toBe(2);
  });
  it("log-safe values drop zero and negatives", () => {
    expect(logSafe([1, 0, -2, 4])).toEqual({ values: [1, null, null, 4], skipped: 2 });
  });
});

function run(id: string, n: number, f: (i: number) => number): TimeSeries {
  const step = Array.from({ length: n }, (_, i) => i * 10);
  return { id, label: id, x: { step, epoch: step.map((s) => s / 100) }, y: step.map((_, i) => f(i)) };
}

describe("buildFrame", () => {
  it("sparse without smoothing draws the raw series as the main line", () => {
    const f = buildFrame([run("a", 5, (i) => i)], { xKey: "step", smoothing: 0, envelope: "auto", log: false, widthPx: 600 });
    expect(f.dense).toBe(false);
    expect(f.lines[0]?.main).toEqual([0, 1, 2, 3, 4]);
    expect(f.lines[0]?.raw.every((v) => v == null)).toBe(true);
  });
  it("with smoothing keeps the raw line (faint) under the EMA", () => {
    const f = buildFrame([run("a", 5, (i) => i)], { xKey: "step", smoothing: 0.6, envelope: "auto", log: false, widthPx: 600 });
    expect(f.lines[0]?.raw).toEqual([0, 1, 2, 3, 4]);
    expect(f.lines[0]?.main[4]).toBeLessThan(4);
  });
  it("switches to an envelope above two points per pixel and keeps one x axis for all series", () => {
    const f = buildFrame([run("a", 1000, (i) => Math.sin(i)), run("b", 400, (i) => i)], { xKey: "step", smoothing: 0, envelope: "auto", log: false, widthPx: 100 });
    expect(f.dense).toBe(true);
    expect(f.x).toHaveLength(50);
    for (const l of f.lines) {
      expect(l.min).toHaveLength(50);
      expect(l.max).toHaveLength(50);
    }
    const a = f.lines[0]!;
    expect(a.min.some((v, i) => v != null && a.max[i] != null && (a.max[i] as number) > v)).toBe(true);
  });
  it("draws against the chosen x field and skips non-positive values on a log scale", () => {
    const f = buildFrame([run("a", 3, (i) => i)], { xKey: "epoch", smoothing: 0, envelope: "off", log: true, widthPx: 600 });
    expect(f.x).toEqual([0, 0.1, 0.2]);
    expect(f.lines[0]?.main).toEqual([null, 1, 2]);
    expect(f.skipped).toEqual({ a: 1 });
  });
  it("colour slots follow the series' own slot, not its position", () => {
    const s = { ...run("b", 2, (i) => i), slot: 5 };
    expect(buildFrame([s], { xKey: "step", smoothing: 0, envelope: "off", log: false, widthPx: 100 }).lines[0]?.slot).toBe(5);
  });
});

describe("table, CSV and summaries", () => {
  it("the table holds raw values on the union of x, smoothed columns and markers", () => {
    const t = timeSeriesTable([run("loss", 3, (i) => 3 - i)], "step", 0.5, [{ id: "c1", label: "ckpt-20", kind: "checkpoint", x: { step: 20 } }]);
    expect(t.columns).toEqual(["Step", "loss", "loss (smoothed)", "Marker"]);
    expect(t.rows[0]?.[1]).toBe(3);
    expect(t.rows[2]?.[3]).toBe("ckpt-20");
  });
  it("CSV quotes separators and leaves gaps empty", () => {
    expect(toCsv({ columns: ["a", "b,c"], rows: [[1, null], ['say "hi"', 2.5]] })).toBe('a,"b,c"\r\n1,\r\n"say ""hi""",2.5\r\n');
  });
  it("summarises each series with range, extremes and trend, and lists markers", () => {
    const s = summarizeTimeSeries("Loss", [run("train", 50, (i) => 10 / (i + 1))], { xKey: "step", smoothing: 0, log: false }, [
      { id: "m", label: "ckpt-100", x: { step: 100 } },
    ]);
    expect(s).toContain("Loss: 1 series by step, linear scale.");
    expect(s).toContain("train: 50 points, step 0 to 490");
    expect(s).toContain("falling");
    expect(s).toContain("1 marker: ckpt-100 at 100");
  });
  it("trend reads rising, falling and flat", () => {
    expect(trend([1, 2, 3, 4, 5, 6])).toBe("rising");
    expect(trend([6, 5, 4, 3, 2, 1])).toBe("falling");
    expect(trend([1, 1, 1, 1, 1])).toBe("flat");
  });
  it("formats numbers compactly", () => {
    expect(formatNumber(0.000012)).toMatch(/E/);
    expect(formatNumber(1234.5678, 0)).toMatch(/1.?235/);
  });
});

describe("throttle (≤ 4 redraws per second)", () => {
  it("runs the first call at once and a burst once at the end of the interval", () => {
    let now = 0;
    const timers: { at: number; fn: () => void }[] = [];
    const clock: Clock = { now: () => now, setTimeout: (fn, ms) => timers.push({ at: now + ms, fn }), clearTimeout: () => {} };
    const fn = vi.fn();
    const t = createThrottle(fn, 250, clock);
    t.schedule();
    expect(fn).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 20; i++) {
      now += 10;
      t.schedule();
    }
    expect(fn).toHaveBeenCalledTimes(1);
    expect(timers).toHaveLength(1);
    now = timers[0]!.at;
    timers[0]!.fn();
    expect(fn).toHaveBeenCalledTimes(2);
  });
  it("draws at most 4 times in a second of continuous appends", () => {
    let now = 0;
    const pending: { at: number; fn: () => void }[] = [];
    const clock: Clock = { now: () => now, setTimeout: (fn, ms) => pending.push({ at: now + ms, fn }), clearTimeout: () => {} };
    const fn = vi.fn();
    const t = createThrottle(fn, 250, clock);
    for (now = 0; now < 1000; now += 5) {
      for (const p of pending.splice(0).filter((p) => p.at <= now)) p.fn();
      t.schedule();
    }
    expect(fn.mock.calls.length).toBeLessThanOrEqual(4);
  });
});

describe("tokens", () => {
  it("names eight categorical slots and wraps with a new dash", () => {
    expect(categoricalVar(0)).toBe("--cadence-chart-1");
    expect(categoricalVar(9)).toBe("--cadence-chart-2");
    expect(dashFor(0)).toEqual([]);
    expect(dashFor(8)).not.toEqual(dashFor(0));
  });
  it("parses colours and applies alpha", () => {
    expect(parseColor(["#", "ff000080"].join(""))).toEqual({ r: 255, g: 0, b: 0, a: 128 / 255 });
    expect(withAlpha("rgb(10, 20, 30)", 0.5)).toBe("rgba(10, 20, 30, 0.5)");
    expect(withAlpha("color(display-p3 1 0 0)", 0.5)).toBe("color(display-p3 1 0 0)");
  });
});

describe("analytics presets", () => {
  const hist: AnalyticsSpec = {
    kind: "histogram",
    title: "Durations",
    xLabel: "Duration",
    unit: "s",
    series: [
      { id: "v1", label: "v1", bins: [{ start: 0, end: 5, count: 3 }, { start: 5, end: 10, count: 9 }] },
      { id: "v2", label: "v2", bins: [{ start: 0, end: 5, count: 4 }, { start: 5, end: 10, count: 2 }] },
    ],
    marks: [{ label: "p95", value: 9 }],
  };
  const forest: AnalyticsSpec = {
    kind: "forest",
    title: "WER delta",
    rows: [
      { label: "a", estimate: -1, low: -2, high: -0.5 },
      { label: "b", estimate: 0.2, low: -0.3, high: 0.8 },
    ],
  };

  it("builds an option with ARIA description and decals on, and palette colours per slot", () => {
    const o = buildOption(hist, theme, "summary text") as { aria: { enabled: boolean; label: { description: string }; decal: { show: boolean } }; series: { itemStyle: { color: string }; markLine?: { data: { xAxis: number }[] } }[] };
    expect(o.aria.enabled).toBe(true);
    expect(o.aria.decal.show).toBe(true);
    expect(o.aria.label.description).toBe("summary text");
    expect(o.series.map((s) => s.itemStyle.color)).toEqual(["c1", "c2"]);
    expect(o.series[0]?.markLine?.data[0]?.xAxis).toBe(1);
  });
  it("heatmaps take the sequential ramp or a diverging one symmetric around the centre", () => {
    const h: AnalyticsSpec = { kind: "heatmap", title: "Matrix", x: ["a", "b"], y: ["r"], cells: [{ x: 0, y: 0, value: -1 }, { x: 1, y: 0, value: 3 }], colormap: "diverging" };
    expect(heatmapRange(h)).toEqual([-3, 3]);
    const o = buildOption(h, theme, "") as { visualMap: { inRange: { color: string[] } } };
    expect(o.visualMap.inRange.color).toEqual(theme.diverging);
    expect(analyticsTable(h)).toEqual({ columns: ["Row", "a", "b"], rows: [["r", -1, 3]] });
    expect(cursorItems(h).rowLength).toBe(2);
  });
  it("finds the Pareto front", () => {
    const pts = [{ x: 1, y: 5 }, { x: 2, y: 3 }, { x: 3, y: 4 }, { x: 4, y: 1 }];
    expect(paretoFront(pts, { x: "min", y: "min" })).toEqual([{ x: 1, y: 5 }, { x: 2, y: 3 }, { x: 4, y: 1 }]);
  });
  it("summarises, tabulates and walks each preset", () => {
    expect(summarizeAnalytics(hist)).toContain("v1: 12 items in 2 bins, most in 5 s–10 s (9)");
    expect(summarizeAnalytics(hist)).toContain("p95 at 9 s");
    expect(summarizeAnalytics(forest)).toContain("1 entirely below, 1 include it");
    expect(analyticsTable(forest).rows[0]).toEqual(["a", -1, -2, -0.5]);
    expect(cursorItems(forest).items[1]?.text).toBe("b: 0.2, interval -0.3 to 0.8");
    const bar: AnalyticsSpec = { kind: "bar", title: "SDI", categories: ["x", "y"], series: [{ id: "s", label: "S", values: [2, 5] }], stacked: true };
    expect(summarizeAnalytics(bar)).toContain("highest y 5");
  });
  it("parallel coordinates: one series per line, axes by type, the highlighted line wider and solid", () => {
    const par: AnalyticsSpec = {
      kind: "parallel",
      title: "Sweep",
      axes: [
        { id: "lr", label: "peak_lr", type: "log" },
        { id: "aug", label: "augmentation", type: "category", categories: ["clean", "telephony"] },
        { id: "wer", label: "val WER" },
      ],
      lines: [
        { id: "r1", label: "run 1", values: [0.0001, "clean", 0.3] },
        { id: "r2", label: "run 2", values: [0.0003, "telephony", 0.25], highlight: true },
        { id: "r3", label: "run 3", values: [0.001, "clean", null] },
      ],
    };
    const o = buildOption(par, theme, "s") as {
      parallelAxis: { dim: number; type: string; data?: string[] }[];
      series: { type: string; data: unknown[][]; lineStyle: { color: string; width: number; type: unknown } }[];
    };
    expect(o.parallelAxis.map((a) => a.type)).toEqual(["log", "category", "value"]);
    expect(o.parallelAxis[1]?.data).toEqual(["clean", "telephony"]);
    expect(o.series.map((s) => s.type)).toEqual(["parallel", "parallel", "parallel"]);
    expect(o.series[2]?.data[0]).toEqual([0.001, "clean", "-"]);
    expect(o.series[1]?.lineStyle).toMatchObject({ color: "c2", type: "solid" });
    expect(o.series[1]!.lineStyle.width).toBeGreaterThan(o.series[0]!.lineStyle.width);
    expect(analyticsTable(par)).toEqual({
      columns: ["Line", "peak_lr", "augmentation", "val WER"],
      rows: [
        ["run 1", 0.0001, "clean", 0.3],
        ["run 2 (highlighted)", 0.0003, "telephony", 0.25],
        ["run 3", 0.001, "clean", null],
      ],
    });
    const sum = summarizeAnalytics(par);
    expect(sum).toContain("3 lines over 3 axes");
    expect(sum).toContain("augmentation takes 2 values");
    expect(sum).toContain("val WER 0.25 to 0.3");
    expect(sum).toContain("Highlighted: run 2.");
    expect(cursorItems(par).items[2]).toEqual({ seriesIndex: 2, dataIndex: 0, text: "run 3: peak_lr 0.001, augmentation clean, val WER no value" });
  });
  it("lines: step ECDFs without symbols, interval bars, a marked point and x marks; table, summary and cursor", () => {
    const line: AnalyticsSpec = {
      kind: "line",
      title: "WER against latency",
      xLabel: "Latency (ms)",
      yLabel: "WER",
      unit: "%",
      note: "Intervals: baseline + delta.",
      xMarks: [{ label: "primary", value: 160 }],
      series: [
        { id: "s", label: "Subject", points: [{ x: 80, y: 15, low: 13.5, high: 16, label: "80ms" }, { x: 160, y: 10, low: 9, high: 11, label: "160ms", marked: true }] },
        { id: "b", label: "Baseline", points: [{ x: 80, y: 14, label: "80ms" }, { x: 160, y: 12, label: "160ms", marked: true }] },
        { id: "e", label: "ECDF", slot: 2, step: true, points: [{ x: 0, y: 0 }, { x: 50, y: 1 }] },
      ],
    };
    const o = buildOption(line, theme, "s") as {
      series: { type: string; step?: string; showSymbol?: boolean; data: unknown[]; lineStyle?: { type: unknown }; markLine?: { data: { xAxis: number }[] } }[];
    };
    expect(o.series.map((x) => x.type)).toEqual(["line", "line", "line", "custom"]);
    expect(o.series[0]?.lineStyle?.type).toBe("solid");
    expect(o.series[1]?.lineStyle?.type).toEqual([8, 4]);
    expect(o.series[2]).toMatchObject({ step: "end", showSymbol: false });
    expect(o.series[0]?.markLine?.data[0]?.xAxis).toBe(160);
    expect(o.series[0]?.data[1]).toMatchObject({ value: [160, 10], symbolSize: 14 });
    // Only the subject has intervals: one custom series with its two bars.
    expect(o.series[3]?.data).toEqual([
      [80, 13.5, 16],
      [160, 9, 11],
    ]);
    const t = analyticsTable(line);
    expect(t.columns).toEqual(["Series", "Point", "Latency (ms)", "WER", "Low", "High"]);
    expect(t.rows[1]).toEqual(["Subject", "160ms ★", 160, 10, 9, 11]);
    expect(t.rows[4]).toEqual(["ECDF", null, 0, 0, null, null]);
    const sum = summarizeAnalytics(line);
    expect(sum).toContain("step lines for 3 series");
    expect(sum).toContain("Subject: 2 points, Latency (ms) 80 to 160, WER 10 % to 15 %, marked 160ms 10 %, 2 with intervals");
    expect(sum.endsWith("Intervals: baseline + delta.")).toBe(true);
    const cur = cursorItems(line).items;
    expect(cur).toHaveLength(6);
    expect(cur[1]).toEqual({ seriesIndex: 0, dataIndex: 1, text: "Subject 160ms (marked): Latency (ms) 160, WER 10 %, interval 9 to 11" });
  });
});
