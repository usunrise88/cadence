import "@/styles/theme.css";
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { applyTheme, useTheme } from "@/shell/theme/store";
import { AnalyticsChart, TimeSeriesChart, readChartTheme, type AnalyticsSpec, type TimeSeries } from "@/shell/charts";
import { parseColor } from "./tokens";

// Real rendering in headless Chromium: both chart kinds, both themes, and a popout-like second document (an
// iframe: separate document and window, as Dockview's popout windows are).

// The few layout utilities the charts use (Tailwind is not part of the browser test build).
const LAYOUT = `.absolute{position:absolute}.relative{position:relative}.inset-0{inset:0}.hidden{display:none}
.flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1 1 0%}.flex-none{flex:none}.min-h-0{min-height:0}
.h-full{height:100%}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)}`;

function addLayout(doc: Document) {
  const style = doc.createElement("style");
  style.textContent = LAYOUT;
  doc.head.appendChild(style);
}

beforeAll(async () => {
  addLayout(document);
  // Warm the lazy ECharts chunk once (the first load optimises the dependency, which is slow on a busy machine).
  await import("./AnalyticsChartImpl");
}, 60000);
afterEach(() => {
  cleanup();
  setDark(false);
});

function setDark(dark: boolean) {
  act(() => {
    applyTheme(document, dark);
    useTheme.setState({ dark });
  });
}

function series(): TimeSeries[] {
  const step = Array.from({ length: 200 }, (_, i) => i * 10);
  return [
    { id: "train", label: "train loss", x: { step }, y: step.map((_, i) => 2 / (1 + i / 20) + 0.05 * Math.sin(i)) },
    { id: "val", label: "val loss", x: { step }, y: step.map((_, i) => 2.2 / (1 + i / 25)) },
  ];
}

function box(doc: Document = document, w = 640, h = 320): HTMLDivElement {
  const el = doc.createElement("div");
  el.style.width = `${w}px`;
  el.style.height = `${h}px`;
  el.style.position = "relative";
  doc.body.appendChild(el);
  return el;
}

/** Pixels of a canvas within `tol` of a colour (and nearer to it than to `other`). */
function countNear(canvas: HTMLCanvasElement, color: string, other?: string, tol = 60): number {
  const c = parseColor(color);
  const o = other ? parseColor(other) : null;
  if (!c) throw new Error(`unparsed ${color}`);
  const ctx = canvas.getContext("2d");
  if (!ctx) return 0;
  const { data } = ctx.getImageData(0, 0, canvas.width, canvas.height);
  const d = (r: number, g: number, b: number, k: { r: number; g: number; b: number }) => Math.hypot(r - k.r, g - k.g, b - k.b);
  let n = 0;
  for (let i = 0; i < data.length; i += 4) {
    if ((data[i + 3] ?? 0) < 200) continue;
    const [r, g, b] = [data[i] ?? 0, data[i + 1] ?? 0, data[i + 2] ?? 0];
    const dc = d(r, g, b, c);
    if (dc <= tol && (!o || dc < d(r, g, b, o))) n++;
  }
  return n;
}

function canvasesIn(el: Element): HTMLCanvasElement[] {
  return [...el.querySelectorAll("canvas")];
}

describe("TimeSeriesChart (uPlot)", () => {
  it("draws series in the categorical colours and switches theme without re-creating the canvas", async () => {
    const container = box();
    const { getByTestId } = render(<TimeSeriesChart title="Loss" series={series()} xKey="step" smoothing={0.6} markers={[{ id: "c", label: "ckpt-1000", x: { step: 1000 } }]} />, { container });
    const plot = getByTestId("timeseries-plot");
    await waitFor(() => expect(canvasesIn(plot)).toHaveLength(1));
    const canvas = canvasesIn(plot)[0]!;
    expect(canvas.width).toBeGreaterThan(300);
    const light = readChartTheme(plot).categorical[0]!;
    setDark(true);
    const dark = readChartTheme(plot).categorical[0]!;
    expect(dark).not.toBe(light);
    await waitFor(() => expect(countNear(canvas, dark, light)).toBeGreaterThan(50));
    expect(canvasesIn(plot)[0]).toBe(canvas);
    setDark(false);
    await waitFor(() => expect(countNear(canvas, light, dark)).toBeGreaterThan(50));
    expect(canvasesIn(plot)[0]).toBe(canvas);
  });

  it("has a keyboard cursor with a spoken readout, a table view and a summary", async () => {
    const container = box();
    const { getByTestId, getByRole, getByText } = render(<TimeSeriesChart title="Loss" series={series()} xKey="step" />, { container });
    const plot = getByTestId("timeseries-plot");
    await waitFor(() => expect(canvasesIn(plot)).toHaveLength(1));
    plot.focus();
    fireEvent.keyDown(plot, { key: "ArrowRight" });
    fireEvent.keyDown(plot, { key: "ArrowRight" });
    await waitFor(() => expect(getByRole("status").textContent).toContain("Step 10: train loss"));
    fireEvent.keyDown(plot, { key: "End" });
    await waitFor(() => expect(getByRole("status").textContent).toContain("Step 1,990"));
    const summary = document.getElementById(plot.getAttribute("aria-describedby") ?? "");
    expect(summary?.textContent).toContain("train loss: 200 points");
    fireEvent.click(getByText("Table"));
    await waitFor(() => expect(getByTestId("chart-table").querySelectorAll("tbody tr")).toHaveLength(200));
  });

  it("resizes with its container", async () => {
    const container = box(document, 400, 240);
    const { getByTestId } = render(<TimeSeriesChart title="Loss" series={series()} xKey="step" />, { container });
    const plot = getByTestId("timeseries-plot");
    await waitFor(() => expect(canvasesIn(plot)).toHaveLength(1));
    const w0 = canvasesIn(plot)[0]!.getBoundingClientRect().width;
    container.style.width = "700px";
    await waitFor(() => expect(canvasesIn(plot)[0]!.getBoundingClientRect().width).toBeGreaterThan(w0 + 200));
  });
});

const histogram: AnalyticsSpec = {
  kind: "histogram",
  title: "Utterance duration",
  xLabel: "Duration",
  unit: "s",
  series: [{ id: "v1", label: "2026-09-01.abc", bins: Array.from({ length: 12 }, (_, i) => ({ start: i, end: i + 1, count: Math.round(100 * Math.exp(-((i - 4) ** 2) / 8)) })) }],
  marks: [{ label: "max 10 s", value: 10 }],
};

describe("AnalyticsChart (ECharts)", () => {
  it("renders lazily, keeps its canvas across a theme switch (setTheme) and exposes ARIA", async () => {
    const container = box();
    const { findByTestId } = render(<AnalyticsChart spec={histogram} />, { container });
    const inner = await findByTestId("analytics-canvas", {}, { timeout: 20000 });
    await waitFor(() => expect(canvasesIn(inner).length).toBeGreaterThan(0));
    const canvas = canvasesIn(inner)[0]!;
    const light = readChartTheme(inner).categorical[0]!;
    await waitFor(() => expect(countNear(canvas, light, undefined, 30)).toBeGreaterThan(500));
    await waitFor(() => expect(inner.getAttribute("aria-label") ?? "").toContain("Utterance duration"));
    setDark(true);
    const dark = readChartTheme(inner).categorical[0]!;
    await waitFor(() => expect(countNear(canvas, dark, light, 30)).toBeGreaterThan(500));
    expect(canvasesIn(inner)[0]).toBe(canvas);
  });

  it.each<AnalyticsSpec>([
    { kind: "bar", title: "S/D/I", categories: ["a", "b", "c"], series: [{ id: "s", label: "S", values: [1, 2, 3] }, { id: "d", label: "D", values: [2, 1, 1] }], stacked: true },
    { kind: "heatmap", title: "Matrix", x: ["m1", "m2"], y: ["g1", "g2"], cells: [{ x: 0, y: 0, value: 10 }, { x: 1, y: 0, value: 12 }, { x: 0, y: 1, value: 8 }, { x: 1, y: 1, value: null }] },
    { kind: "scatter", title: "WER vs latency", series: [{ id: "p", label: "profiles", points: [{ x: 100, y: 12 }, { x: 200, y: 10 }, { x: 300, y: 11 }] }], front: { x: "min", y: "min" } },
    { kind: "forest", title: "WER delta", rows: [{ label: "a", estimate: -1, low: -2, high: -0.4 }, { label: "b", estimate: 0.3, low: -0.2, high: 0.9 }] },
    {
      kind: "parallel",
      title: "Sweep",
      axes: [{ id: "lr", label: "peak_lr", type: "log" }, { id: "aug", label: "augmentation", type: "category", categories: ["clean", "telephony"] }, { id: "wer", label: "val WER" }],
      lines: [
        { id: "r1", label: "run 1", values: [0.0001, "clean", 0.3] },
        { id: "r2", label: "run 2", values: [0.001, "telephony", 0.25], highlight: true },
      ],
    },
  ])("renders the $kind preset and walks it with the keyboard", async (spec) => {
    const container = box();
    const { findByTestId, getByRole } = render(<AnalyticsChart spec={spec} />, { container });
    const plot = await findByTestId("analytics-plot", {}, { timeout: 20000 });
    await waitFor(() => expect(canvasesIn(plot).length).toBeGreaterThan(0));
    plot.focus();
    fireEvent.keyDown(plot, { key: "ArrowRight" });
    await waitFor(() => expect(getByRole("status").textContent?.length ?? 0).toBeGreaterThan(3));
  });
});

describe("in a popout-like second document", () => {
  async function secondDocument(): Promise<Document> {
    const frame = document.createElement("iframe");
    frame.style.width = "900px";
    frame.style.height = "700px";
    document.body.appendChild(frame);
    const doc = frame.contentDocument!;
    doc.open();
    doc.write("<!doctype html><html><head></head><body></body></html>");
    doc.close();
    // As Dockview does for popouts: copy the opener's stylesheets; the shell applies the theme class.
    for (const node of document.head.querySelectorAll("style, link[rel=stylesheet]")) doc.head.appendChild(node.cloneNode(true));
    applyTheme(doc, false);
    return doc;
  }

  it("draws both chart kinds inside the other document and follows its size", async () => {
    const doc = await secondDocument();
    const a = box(doc, 500, 260);
    const b = box(doc, 500, 260);
    const ts = render(<TimeSeriesChart title="Loss" series={series()} xKey="step" />, { container: a });
    const an = render(<AnalyticsChart spec={histogram} />, { container: b });
    const plot = ts.getByTestId("timeseries-plot");
    await waitFor(() => expect(canvasesIn(plot)).toHaveLength(1));
    const canvas = canvasesIn(plot)[0]!;
    expect(canvas.ownerDocument).toBe(doc);
    const color = readChartTheme(plot).categorical[0]!;
    await waitFor(() => expect(countNear(canvas, color)).toBeGreaterThan(50));
    const inner = await an.findByTestId("analytics-canvas", {}, { timeout: 20000 });
    await waitFor(() => expect(canvasesIn(inner).length).toBeGreaterThan(0));
    expect(canvasesIn(inner)[0]!.ownerDocument).toBe(doc);
    await waitFor(() => expect(countNear(canvasesIn(inner)[0]!, color, undefined, 30)).toBeGreaterThan(300));
    const w0 = canvas.getBoundingClientRect().width;
    a.style.width = "800px";
    await waitFor(() => expect(canvasesIn(plot)[0]!.getBoundingClientRect().width).toBeGreaterThan(w0 + 200));
  });
});
