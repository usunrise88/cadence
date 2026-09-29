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
];

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

if (import.meta.url === `file://${process.argv[1]}`) {
  const rows = check();
  if (process.argv.includes("--json")) {
    console.log(JSON.stringify(rows, null, 2));
  } else {
    for (const r of rows) console.log(`${r.ok ? "ok  " : "FAIL"} ${r.mode.padEnd(5)} ${r.ratio.toFixed(2).padStart(5)} ≥ ${r.min}  ${r.label} (${r.fg} on ${r.bg})`);
  }
  const failed = rows.filter((r) => !r.ok);
  if (failed.length) {
    console.error(`${failed.length} pairing(s) below threshold`);
    process.exit(1);
  }
}
