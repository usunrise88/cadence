// Captures one figure of the book from a running Cadence: opens the screen a figure spec describes, saves the
// screenshot and the box of every region in the image's pixels (docs/tutorial/GUIDELINES.md §5.3, §5.5).
//
//   node capture.mjs <figure.json> <out-dir>
//
// Access: an API key read from KEY_FILE (default /key), sent as a Bearer header on every request; never on the
// command line. The capture only reads: every API request that is not a GET is refused before it leaves the page.
import { chromium } from "playwright";
import { readFileSync, mkdirSync, writeFileSync } from "node:fs";

const [specPath, out] = process.argv.slice(2);
const spec = JSON.parse(readFileSync(specPath, "utf8"));
const base = process.env.CADENCE_UI ?? "http://127.0.0.1:36200";
const key = readFileSync(process.env.KEY_FILE ?? "/key", "utf8").trim();
const vars = JSON.parse(process.env.FIGURE_VARS ?? "{}"); // ids the spec names as {{name}}, e.g. the gate eval
const fill = (s) => s.replace(/\{\{(\w+)\}\}/g, (_, k) => {
  if (!(k in vars)) throw new Error(`figure ${spec.id}: no value for {{${k}}}`);
  return vars[k];
});

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: spec.viewport ?? { width: 1440, height: 900 },
  colorScheme: "light",
  deviceScaleFactor: spec.scale ?? 2,
  extraHTTPHeaders: { Authorization: `Bearer ${key}` },
});
await ctx.addInitScript(() => {
  try {
    localStorage.setItem("cadence.debug", "1"); // exposes window.__cadence.openPanel
  } catch {}
});
await ctx.route("**/api/**", (route) => (route.request().method() === "GET" ? route.continue() : route.abort()));
const page = await ctx.newPage();
await page.goto(`${base}/p/${fill(spec.project)}/w/${fill(spec.workspace)}`);
await page.waitForFunction(() => !!window.__cadence, null, { timeout: 30000 });
await page.waitForTimeout(1500);

for (const step of spec.steps ?? []) {
  if (step.open) await page.evaluate(([p, d]) => window.__cadence.openPanel(p, d ? { doc: d } : undefined), [step.open, step.doc ? fill(step.doc) : null]);
  if (step.close) {
    const b = page.getByRole("button", { name: `Close ${step.close}` });
    if (await b.count()) await b.first().click();
  }
  if (step.maximize) {
    await page.locator(`[data-panel="${step.maximize}"]`).click({ position: { x: 4, y: 4 } });
    const m = page.locator(".dv-groupview", { has: page.locator(`[data-panel="${step.maximize}"]`) }).getByRole("button", { name: "Maximize" });
    if (await m.count()) await m.first().click();
  }
  if (step.waitFor) await page.locator(fill(step.waitFor)).first().waitFor({ timeout: 30000 });
  await page.waitForTimeout(step.settleMs ?? 800);
}

const regions = [];
for (const r of spec.regions) {
  const loc = page.locator(fill(r.target)).nth(r.nth ?? 0);
  if (!(await loc.count())) throw new Error(`figure ${spec.id}: region ${r.label} — target ${r.target} is not on the screen`);
  const b = await loc.boundingBox();
  const pad = r.pad ?? 3;
  regions.push({ ...r, box: [b.x - pad, b.y - pad, b.width + 2 * pad, b.height + 2 * pad].map((v) => Math.round(v)) });
}
mkdirSync(out, { recursive: true });
await page.screenshot({ path: `${out}/${spec.id}.png` });
const vp = page.viewportSize();
writeFileSync(`${out}/${spec.id}.json`, JSON.stringify({ id: spec.id, caption: spec.caption, alt: spec.alt, width: vp.width, height: vp.height, capturedAt: new Date().toISOString(), base, regions }, null, 2));
await browser.close();
console.log(`${spec.id}: ${regions.length} regions`);
