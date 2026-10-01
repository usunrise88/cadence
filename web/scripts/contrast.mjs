// Contrast check for every pairing in the Theming table (docs/spec/10-ui-shell.md), light and dark.
// Text pairs need 4.5:1 (WCAG 2.2 SC 1.4.3); focus rings, sashes and guides 3:1 (SC 1.4.11).
// Colours come from @radix-ui/colors (sRGB values); alpha steps are composited over their background.
// Exit code 1 when any pair fails. `node scripts/contrast.mjs --json` prints the table for the S3 spike record.
import * as radix from "@radix-ui/colors";

function scale(name, dark, alpha = false) {
  const key = `${name}${dark ? "Dark" : ""}${alpha ? "A" : ""}`;
  const s = radix[key];
  if (!s) throw new Error(`unknown scale ${key}`);
  return s;
}

/** "slate-12", "indigo-a3", "white", or "light|dark" per mode ("indigo-9|indigo-10") → hex for the mode. */
function token(t, dark) {
  if (t.includes("|")) return token(t.split("|")[dark ? 1 : 0], dark);
  if (t === "white") return "#ffffff";
  const m = /^([a-z]+)-(a?)(\d+)$/.exec(t);
  if (!m) throw new Error(`bad token ${t}`);
  const [, name, a, step] = m;
  return scale(name, dark, a === "a")[`${name}${a ? "A" : ""}${step}`];
}

function parse(hex) {
  const h = hex.replace("#", "");
  const n = (i) => parseInt(h.slice(i, i + 2), 16);
  return { r: n(0), g: n(2), b: n(4), a: h.length === 8 ? n(6) / 255 : 1 };
}

function over(fg, bg) {
  return { r: fg.r * fg.a + bg.r * (1 - fg.a), g: fg.g * fg.a + bg.g * (1 - fg.a), b: fg.b * fg.a + bg.b * (1 - fg.a), a: 1 };
}

function lum({ r, g, b }) {
  const f = (c) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}

export function ratio(fgHex, bgHex, baseHex) {
  let bg = parse(bgHex);
  if (bg.a < 1) bg = over(bg, parse(baseHex));
  let fg = parse(fgHex);
  if (fg.a < 1) fg = over(fg, bg);
  const [a, b] = [lum(fg), lum(bg)].sort((x, y) => y - x);
  return (a + 0.05) / (b + 0.05);
}

// [label, foreground, background, minimum]; alpha backgrounds sit on slate-1.
// Deviations from the Theming table found in spike S3: focus ring indigo-9 light / indigo-10 dark (indigo-8 is
// 2.3:1 on slate-1),
// active sash slate-9 (slate-8 is 1.9:1), warning text amber-12 (amber-11 is 4.498:1).
export const PAIRS = [
  ["Primary text on documents", "slate-12", "slate-1", 4.5],
  ["Primary text on tool panels, tab bars, menus", "slate-12", "slate-2", 4.5],
  ["Secondary text on documents", "slate-11", "slate-1", 4.5],
  ["Secondary text / inactive tab labels on tool panels", "slate-11", "slate-2", 4.5],
  ["Text on hovered rows", "slate-12", "slate-4", 4.5],
  ["Secondary text on hovered rows", "slate-11", "slate-3", 4.5],
  ["Text on the selected row", "slate-12", "slate-5", 4.5],
  ["Text on the Chat", "slate-12", "slate-3|slate-2", 4.5],
  ["Secondary text on the Chat (tool lines)", "slate-11", "slate-3|slate-2", 4.5],
  ["Tool line hover in the Chat", "slate-12", "slate-4", 4.5],
  ["Status running text on the Chat", "blue-12|blue-11", "slate-3|slate-2", 4.5],
  ["Status done text on the Chat", "grass-12|grass-11", "slate-3|slate-2", 4.5],
  ["Status failed text on the Chat", "red-11", "slate-3|slate-2", 4.5],
  ["Diff added count on the Chat", "grass-12|grass-11", "slate-3|slate-2", 4.5],
  ["Diff added inside the Chat", "grass-12|grass-11", "grass-3", 4.5],
  ["Diff removed count on the Chat", "red-11", "slate-3|slate-2", 4.5],
  ["Focus ring on the Chat", "indigo-9|indigo-10", "slate-3|slate-2", 3],
  ["Unread dot on a tab", "indigo-9", "slate-1", 3],
  ["Status running text", "blue-11", "slate-1", 4.5],
  ["Status done text", "grass-11", "slate-1", 4.5],
  ["Status warning text", "amber-12", "slate-1", 4.5],
  ["Status failed text", "red-11", "slate-1", 4.5],
  ["Status running text on tool panels", "blue-11", "slate-2", 4.5],
  ["Status failed text on tool panels", "red-11", "slate-2", 4.5],
  ["Agent attribution badge", "indigo-11", "indigo-a3", 4.5],
  ["Accent chip (stepper, next step)", "indigo-11", "indigo-3", 4.5],
  ["Diff added", "grass-11", "grass-3", 4.5],
  ["Diff removed", "red-11", "red-3", 4.5],
  ["Primary button label", "white", "indigo-9", 4.5],
  ["Focus ring on documents", "indigo-9|indigo-10", "slate-1", 3],
  ["Focus ring on tool panels", "indigo-9|indigo-10", "slate-2", 3],
  ["Focus ring on hovered rows", "indigo-9|indigo-10", "slate-4", 3],
  ["Snap guide", "indigo-9", "slate-1", 3],
  ["Active sash", "slate-9", "slate-1", 3],
  ["Active sash on tool panels", "slate-9", "slate-2", 3],
  ["Active tab accent line", "indigo-9", "slate-2", 3],
  // Charts (R53): axes and labels are text; the crosshair and checkpoint marks are graphics (3:1).
  ["Chart axis labels on documents", "slate-11", "slate-1", 4.5],
  ["Chart axis labels on tool panels", "slate-11", "slate-2", 4.5],
  ["Chart crosshair on documents", "slate-9", "slate-1", 3],
  ["Chart crosshair on tool panels", "slate-9", "slate-2", 3],
  ["Chart checkpoint mark on tool panels", "slate-11", "slate-2", 3],
];

// Categorical chart series, in order (theme.css `--cadence-chart-1..8`; a test keeps the two equal). Step 9
// (dark: 10) of eight Radix hues outside the status hues (blue, grass, amber, red) and the accent (indigo);
// lime, sky, orange and teal take a darker light step because their step 9 is under 3:1 on slate-1/slate-2.
export const CHART_CATEGORICAL = [
  "crimson-9|crimson-10",
  "violet-9|violet-10",
  "bronze-9|bronze-10",
  "plum-9|plum-10",
  "lime-11|lime-10",
  "sky-11|sky-10",
  "orange-10",
  "teal-10",
];

for (const [i, t] of CHART_CATEGORICAL.entries()) {
  PAIRS.push([`Chart series ${i + 1} on documents`, t, "slate-1", 3]);
  PAIRS.push([`Chart series ${i + 1} on tool panels`, t, "slate-2", 3]);
}

export function check() {
  const rows = [];
  for (const dark of [false, true]) {
    for (const [label, fg, bg, min] of PAIRS) {
      const r = ratio(token(fg, dark), token(bg, dark), token("slate-1", dark));
      rows.push({ mode: dark ? "dark" : "light", label, fg, bg, ratio: Math.round(r * 100) / 100, min, ok: r >= min });
    }
  }
  return rows;
}

// ---- Colour-vision deficiency ----
// Machado, Oliveira & Fernandes (2009), "A physiologically-based model for simulation of color vision deficiency",
// severity 1.0 (dichromacy), applied in linear sRGB. Distances are Euclidean in OKLab ×100 (Ottosson 2020).
// Thresholds (calibrated to this simulation; the dataviz convention of ΔE 8 as the target, 6 as the floor that is
// legal only with a second channel — every series also has its own dash pattern, so we hold the target):
//   - neighbouring series (i, i+1), simulated protanopia and deuteranopia: ΔE ≥ 8; tritanopia (rare, about
//     1 in 10 000): ΔE ≥ 6, the floor — light lime/sky are 6.7 apart there;
//   - neighbouring series, normal vision: ΔE ≥ 15;
//   - any two series, normal vision: ΔE ≥ 8 (eight Radix hues at one step cannot all be 15 apart; the dash
//     pattern and the legend carry identity for non-neighbours);
//   - the diverging poles (blue and orange ends), all three simulations: ΔE ≥ 15;
//   - sequential ramps: OKLab lightness strictly increasing (perceptually ordered in every simulation).
export const CVD = { neighbour: 8, neighbourTritan: 6, neighbourNormal: 15, anyNormal: 8, poles: 15 };

const MACHADO = {
  protan: [
    [0.152286, 1.052583, -0.204868],
    [0.114503, 0.786281, 0.099216],
    [-0.003882, -0.048116, 1.051998],
  ],
  deutan: [
    [0.367322, 0.860646, -0.227968],
    [0.280085, 0.672501, 0.047413],
    [-0.01182, 0.04294, 0.968881],
  ],
  tritan: [
    [1.255528, -0.076749, -0.178779],
    [-0.078411, 0.930809, 0.147602],
    [0.004733, 0.691367, 0.3039],
  ],
};

function linear(hex) {
  const { r, g, b } = parse(hex);
  return [r, g, b].map((c) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
}

/** Linear sRGB as seen with a dichromacy (`kind` protan | deutan | tritan), or unchanged without one. */
export function simulate(hex, kind) {
  const c = linear(hex);
  if (!kind) return c;
  return MACHADO[kind].map((row) => Math.min(1, Math.max(0, row[0] * c[0] + row[1] * c[1] + row[2] * c[2])));
}

function oklab([r, g, b]) {
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

/** OKLab ΔE ×100 between two colours, optionally both under a simulated dichromacy. */
export function deltaE(a, b, kind) {
  const p = oklab(simulate(a, kind));
  const q = oklab(simulate(b, kind));
  return 100 * Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]);
}

const KINDS = ["protan", "deutan", "tritan"];

/** Sequential ramps from theme.css (`--cadence-chart-<name>-<i>: #hex`), in order. */
export function sequentialRamps(css) {
  const ramps = {};
  for (const m of css.matchAll(/--cadence-chart-(magma|viridis)-(\d+):\s*(#[0-9a-fA-F]{6})/g)) {
    (ramps[m[1]] ??= [])[Number(m[2])] = m[3];
  }
  return ramps;
}

/** Categorical tokens from theme.css in the script's "light|dark" form, to compare with CHART_CATEGORICAL. */
export function categoricalFromTheme(css) {
  const block = (sel) => {
    const m = new RegExp(`(^|\\n)${sel.replace(".", "\\.")} \\{([^}]*--cadence-chart-1:[^}]*)\\}`).exec(css);
    const out = [];
    for (const v of (m?.[2] ?? "").matchAll(/--cadence-chart-(\d+):\s*var\(--([a-z]+-\d+)\)/g)) out[Number(v[1]) - 1] = v[2];
    return out;
  };
  const light = block(":root");
  const dark = block(".dark");
  return light.map((l, i) => (l === dark[i] ? l : `${l}|${dark[i]}`));
}

export function checkCvd(css) {
  const rows = [];
  const add = (mode, label, value, min) => rows.push({ mode, label, value: Math.round(value * 10) / 10, min, ok: value >= min });
  for (const dark of [false, true]) {
    const mode = dark ? "dark" : "light";
    const cols = CHART_CATEGORICAL.map((t) => token(t, dark));
    for (let i = 0; i + 1 < cols.length; i++) {
      for (const kind of KINDS) add(mode, `series ${i + 1}/${i + 2} under ${kind}`, deltaE(cols[i], cols[i + 1], kind), kind === "tritan" ? CVD.neighbourTritan : CVD.neighbour);
      add(mode, `series ${i + 1}/${i + 2} normal vision`, deltaE(cols[i], cols[i + 1]), CVD.neighbourNormal);
    }
    for (let i = 0; i < cols.length; i++) {
      for (let j = i + 2; j < cols.length; j++) add(mode, `series ${i + 1}/${j + 1} normal vision`, deltaE(cols[i], cols[j]), CVD.anyNormal);
    }
    for (const kind of [undefined, ...KINDS]) {
      add(mode, `diverging poles ${kind ?? "normal vision"}`, deltaE(token("blue-9", dark), token("orange-9", dark), kind), CVD.poles);
    }
  }
  for (const [name, ramp] of Object.entries(sequentialRamps(css))) {
    for (const kind of [undefined, ...KINDS]) {
      const ls = ramp.map((h) => oklab(simulate(h, kind))[0]);
      const minStep = Math.min(...ls.slice(1).map((l, i) => l - ls[i]));
      add("any", `${name} lightness increases ${kind ?? "normal vision"} (min step ×100)`, minStep * 100, 0.01);
    }
  }
  return rows;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const { readFileSync } = await import("node:fs");
  const css = readFileSync(new URL("../src/styles/theme.css", import.meta.url), "utf8");
  const rows = check();
  const cvd = checkCvd(css);
  const themed = categoricalFromTheme(css);
  const inSync = JSON.stringify(themed) === JSON.stringify(CHART_CATEGORICAL);
  if (process.argv.includes("--json")) {
    console.log(JSON.stringify({ contrast: rows, cvd }, null, 2));
  } else {
    for (const r of rows) console.log(`${r.ok ? "ok  " : "FAIL"} ${r.mode.padEnd(5)} ${r.ratio.toFixed(2).padStart(5)} ≥ ${r.min}  ${r.label} (${r.fg} on ${r.bg})`);
    for (const r of cvd) console.log(`${r.ok ? "ok  " : "FAIL"} ${r.mode.padEnd(5)} ΔE ${r.value.toFixed(1).padStart(5)} ≥ ${r.min}  ${r.label}`);
    console.log(`${inSync ? "ok  " : "FAIL"} theme.css chart tokens match CHART_CATEGORICAL`);
  }
  const failed = rows.filter((r) => !r.ok).length + cvd.filter((r) => !r.ok).length + (inSync ? 0 : 1);
  if (failed) {
    console.error(`${failed} check(s) below threshold`);
    process.exit(1);
  }
}
