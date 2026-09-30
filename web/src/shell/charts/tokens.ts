// Chart colours (R53, docs/spec/10-ui-shell.md "Theming"). The values live in src/styles/theme.css as CSS variables;
// this module only names them and resolves them to sRGB strings for canvas and ECharts. The contrast script
// (scripts/contrast.mjs) checks the categorical hues at 3:1 on both panel backgrounds and under a
// colour-vision-deficiency simulation.

/** Eight categorical slots, assigned in fixed order and never cycled into new hues: a ninth series reuses slot 1
 * with a different dash, so identity is never colour alone. */
export const CATEGORICAL_COUNT = 8;

/** One dash pattern per slot (canvas `setLineDash`, CSS px): the second channel beside colour (WCAG 1.4.1). */
export const DASHES: readonly (readonly number[])[] = [[], [8, 4], [2, 3], [10, 3, 2, 3], [4, 2], [12, 4], [2, 2, 6, 2], [6, 2, 2, 2, 2, 2]];

/** Scatter symbols per slot (ECharts names): the shape channel for points. */
export const SYMBOLS = ["circle", "rect", "triangle", "diamond", "roundRect", "pin", "arrow", "emptyCircle"] as const;

export type Colormap = "magma" | "viridis";
export const SEQUENTIAL_STOPS = 9;
export const DIVERGING_STOPS = 9;

export function categoricalVar(slot: number): string {
  return `--cadence-chart-${(((slot % CATEGORICAL_COUNT) + CATEGORICAL_COUNT) % CATEGORICAL_COUNT) + 1}`;
}

/** The dash for a slot; slots past eight wrap and use the next pattern so the pair (colour, dash) stays unique. */
export function dashFor(slot: number): readonly number[] {
  const n = DASHES.length;
  const wrap = Math.floor(slot / CATEGORICAL_COUNT);
  return DASHES[(slot + wrap) % n] ?? [];
}

export type ChartTheme = {
  dark: boolean;
  /** Eight categorical colours, `rgb(…)`. */
  categorical: string[];
  axis: string;
  grid: string;
  cursor: string;
  marker: string;
  text: string;
  textSecondary: string;
  surface: string;
  border: string;
  sequential: Record<Colormap, string[]>;
  /** Blue → slate → orange, nine stops. */
  diverging: string[];
};

export type Rgba = { r: number; g: number; b: number; a: number };

/** Parses `#rgb`, `#rrggbb(aa)`, `rgb()`/`rgba()`; null for anything else (named colours, color(), oklch()). */
export function parseColor(value: string): Rgba | null {
  const v = value.trim();
  let m = /^#([0-9a-f]{3,8})$/i.exec(v);
  if (m) {
    let h = m[1] ?? "";
    if (h.length === 3 || h.length === 4) h = [...h].map((c) => c + c).join("");
    if (h.length !== 6 && h.length !== 8) return null;
    const n = (i: number) => parseInt(h.slice(i, i + 2), 16);
    return { r: n(0), g: n(2), b: n(4), a: h.length === 8 ? n(6) / 255 : 1 };
  }
  m = /^rgba?\(([^)]+)\)$/i.exec(v);
  if (m) {
    const parts = (m[1] ?? "").split(/[\s,/]+/).filter(Boolean).map(Number);
    const [r = 0, g = 0, b = 0, a = 1] = parts;
    if ([r, g, b, a].some((x) => Number.isNaN(x))) return null;
    return { r, g, b, a };
  }
  return null;
}

export function rgbaString({ r, g, b, a }: Rgba, alpha = a): string {
  const k = (x: number) => Math.round(x);
  return alpha >= 1 ? `rgb(${k(r)}, ${k(g)}, ${k(b)})` : `rgba(${k(r)}, ${k(g)}, ${k(b)}, ${Math.round(alpha * 1000) / 1000})`;
}

/** The same colour with another alpha (for faint raw lines and envelopes); unknown formats pass through. */
export function withAlpha(color: string, alpha: number): string {
  const c = parseColor(color);
  return c ? rgbaString(c, alpha * c.a) : color;
}

const resolved = new Map<string, string>();

/** Any CSS colour → `rgb()`/`rgba()` in sRGB (Radix serves display-p3 values where supported). Uses a 1×1 canvas of
 * the element's own document, so it works in popout windows; without canvas (jsdom) the value passes through. */
export function resolveColor(value: string, doc: Document): string {
  const direct = parseColor(value);
  if (direct) return rgbaString(direct);
  const hit = resolved.get(value);
  if (hit) return hit;
  let out = value;
  try {
    const canvas = doc.createElement("canvas");
    canvas.width = canvas.height = 1;
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    if (ctx) {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = value;
      ctx.fillRect(0, 0, 1, 1);
      const [r = 0, g = 0, b = 0, a = 255] = ctx.getImageData(0, 0, 1, 1).data;
      out = rgbaString({ r, g, b, a: a / 255 });
      resolved.set(value, out);
    }
  } catch {
    /* no canvas: keep the CSS value */
  }
  return out;
}

function isJsdom(): boolean {
  return typeof navigator !== "undefined" && /jsdom/i.test(navigator.userAgent);
}

/** Reads the chart tokens as the element sees them (its document's theme class; popouts get the same class). */
export function readChartTheme(el: Element): ChartTheme {
  const doc = el.ownerDocument;
  const view = doc.defaultView ?? window;
  const style = view.getComputedStyle(el);
  const raw = (name: string) => style.getPropertyValue(name).trim();
  const get = (name: string) => {
    const v = raw(name);
    if (!v) return "";
    return isJsdom() ? v : resolveColor(v, doc);
  };
  const range = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => get(`${prefix}-${i}`));
  return {
    dark: doc.documentElement.classList.contains("dark"),
    categorical: Array.from({ length: CATEGORICAL_COUNT }, (_, i) => get(categoricalVar(i))),
    axis: get("--cadence-chart-axis"),
    grid: get("--cadence-chart-grid"),
    cursor: get("--cadence-chart-cursor"),
    marker: get("--cadence-chart-marker"),
    text: get("--cadence-text"),
    textSecondary: get("--cadence-text-secondary"),
    surface: get("--cadence-tool"),
    border: get("--cadence-separator"),
    sequential: { magma: range("--cadence-chart-magma", SEQUENTIAL_STOPS), viridis: range("--cadence-chart-viridis", SEQUENTIAL_STOPS) },
    diverging: range("--cadence-chart-div", DIVERGING_STOPS),
  };
}

/** The colour of a categorical slot in a theme. */
export function seriesColor(theme: ChartTheme, slot: number): string {
  return theme.categorical[((slot % CATEGORICAL_COUNT) + CATEGORICAL_COUNT) % CATEGORICAL_COUNT] ?? theme.axis;
}
