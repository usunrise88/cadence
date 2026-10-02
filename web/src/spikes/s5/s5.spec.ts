// Spike S5 measurements (docs/spikes/S5-audio-view.md). Results land in web/test-results/s5/*.json; the browser's
// STFT dumps are compared with librosa by docs/spikes/s5/compare.py. Frame times as S1: rAF intervals in the window
// that hosts the view, p50/p95, share of frames over 20 ms, long animation frames.
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";

const OUT = path.resolve(import.meta.dirname, "../../../test-results/s5");
mkdirSync(OUT, { recursive: true });
function record(id: string, data: unknown) {
  writeFileSync(path.join(OUT, `${id}.json`), JSON.stringify(data, null, 1));
  console.log(id, JSON.stringify(data));
}

/** PSS of the Chromium processes (same container), MB: renderers (the page and its popouts), GPU, browser. */
function chromeMemory() {
  const out = { rendererMB: 0, gpuMB: 0, browserMB: 0, renderers: 0 };
  for (const pid of readdirSync("/proc").filter((p) => /^\d+$/.test(p))) {
    let cmd = "";
    try {
      cmd = readFileSync(`/proc/${pid}/cmdline`, "utf8");
    } catch {
      continue;
    }
    if (!/chrom/i.test(cmd.split("\0")[0] ?? "")) continue;
    let pss = 0;
    try {
      const m = /^Pss:\s+(\d+) kB/m.exec(readFileSync(`/proc/${pid}/smaps_rollup`, "utf8"));
      pss = m ? Number(m[1]) / 1024 : 0;
    } catch {
      continue;
    }
    if (cmd.includes("--type=renderer")) {
      out.rendererMB += pss;
      out.renderers++;
    } else if (cmd.includes("--type=gpu-process")) out.gpuMB += pss;
    else if (!cmd.includes("--type=")) out.browserMB += pss;
  }
  for (const k of ["rendererMB", "gpuMB", "browserMB"] as const) out[k] = Math.round(out[k] * 10) / 10;
  return out;
}

async function open(page: Page, query = "") {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" || /context/i.test(m.text())) errors.push(m.text());
  });
  await page.goto(`/${query}`);
  await page.waitForFunction("window.__s5 !== undefined");
  await page.evaluate("window.__s5.ready");
  await page.waitForTimeout(500);
  return errors;
}
const ev = <T,>(page: Page, js: string) => page.evaluate(js) as Promise<T>;

const SIX = (prefix: string, media = false) => ({
  views: [
    { id: `${prefix}-call-1`, fixture: "call", compact: true, media },
    { id: `${prefix}-clip-1`, fixture: "clip", compact: true },
    { id: `${prefix}-call-2`, fixture: "call", compact: true, features: false },
    { id: `${prefix}-clip-2`, fixture: "clip", compact: true },
    { id: `${prefix}-call-3`, fixture: "call", compact: true, features: false },
    { id: `${prefix}-clip-3`, fixture: "clip", compact: true },
  ],
});

test.describe.configure({ mode: "serial" });

test("S5-A: FFT, zoom/scroll/playback frames, uniform changes, memory, a11y — docked and floating", async ({ page }) => {
  const errors = await open(page);
  const timings = await ev(page, "window.__s5.timings");

  // 1. STFT in the worker: dumps for the librosa comparison; FFT time per minute of 16 kHz audio.
  for (const backend of ["wasm", "js"]) {
    const r = await ev<{ frames: number; u8: string; db: string }>(page, `window.__s5.stft("${backend}", true)`);
    writeFileSync(path.join(OUT, `stft-clip-${backend}.f32`), Buffer.from(r.db, "base64"));
    if (backend === "wasm") writeFileSync(path.join(OUT, "stft-clip-u8.u8"), Buffer.from(r.u8, "base64"));
  }
  const bench = await ev(page, "window.__s5.stftBench(600)");

  // 2. The call, docked: zoom whole → 2 s → whole (cold tiles, then warm), scroll, playback with follow.
  const call = "'call'";
  const zoomIn = `window.__s5.zoomSweep(${call}, 180, {start:0,span:1800}, {start:899,span:2})`;
  const zoomOut = `window.__s5.zoomSweep(${call}, 180, {start:899,span:2}, {start:0,span:1800})`;
  const frames: Record<string, unknown> = {};
  frames.zoomInCold = await ev(page, zoomIn);
  frames.zoomOutCold = await ev(page, zoomOut);
  frames.zoomInWarm = await ev(page, zoomIn);
  frames.zoomOutWarm = await ev(page, zoomOut);
  frames.scroll2s = await ev(page, `window.__s5.scrollSweep(${call}, 2, 240, 600)`);
  frames.scroll30s = await ev(page, `window.__s5.scrollSweep(${call}, 30, 240, 600)`);
  frames.scroll5min = await ev(page, `window.__s5.scrollSweep(${call}, 300, 240, 300)`);
  frames.play2s = await ev(page, `window.__s5.playFollow(${call}, 2, 700)`);
  frames.play30s = await ev(page, `window.__s5.playFollow(${call}, 30, 760)`);
  frames.clipZoomIn = await ev(page, `window.__s5.zoomSweep('clip', 180, {start:0,span:20}, {start:5,span:2})`);
  frames.clipZoomOut = await ev(page, `window.__s5.zoomSweep('clip', 180, {start:5,span:2}, {start:0,span:20})`);
  frames.clipPlay2s = await ev(page, `window.__s5.playFollow('clip', 2, 3)`);

  // 3. Range, gain, colormap, axis: one redraw, no texture upload.
  await ev(page, `window.__s5.views.call.setRange(600, 30)`);
  await page.waitForTimeout(500);
  const changes: Record<string, unknown> = {};
  for (const [k, c] of Object.entries({
    range60: { rangeDb: 60 },
    gain6: { gainDb: 6 },
    viridis: { colormap: "viridis" },
    grey: { colormap: "grey" },
    magma: { colormap: "magma" },
    hzAxis: { axis: "hz" },
    melAxis: { axis: "mel" },
    range80: { rangeDb: 80, gainDb: 0 },
  }))
    changes[k] = await ev(page, `window.__s5.uniformChange('call', ${JSON.stringify(c)})`);

  // 4. Memory: the call (and the 20 s clip) after all of the above.
  const memory = { page: await ev(page, "window.__s5.memory()"), processes: chromeMemory() };

  // 5. Keyboard, summary, reduced motion, theme, bidi.
  const a11y: Record<string, unknown> = {};
  await ev(page, `(() => { const v = window.__s5.views.call; v.setRange(898, 10); v.seek(900); })()`);
  let tabs = 0;
  for (; tabs < 15; tabs++) {
    if (await ev<boolean>(page, "document.activeElement?.dataset?.view === 'call'")) break;
    await page.keyboard.press("Tab");
  }
  a11y.tabsToReachView = tabs;
  const st = () => ev<{ start: number; span: number; playhead: number; loop: number[] | null; paused: boolean; live: string; word: number }>(page, `(() => { const v = window.__s5.views.call; return { start: v.start, span: v.span, playhead: v.playhead, loop: v.loop, paused: v.o.media.paused, live: v.root.querySelector('[aria-live]').textContent, word: v.wordIndex }; })()`);
  const keys: Record<string, unknown> = { before: await st() };
  for (const k of ["Space", "Space", "ArrowRight", "ArrowLeft", "+", "-", "[", "ArrowRight", "]", ".", ".", ","]) {
    await page.keyboard.press(k === "+" ? "Shift+Equal" : k);
    await page.waitForTimeout(250);
    keys[k + "@" + Object.keys(keys).length] = await st();
  }
  a11y.keys = keys;
  a11y.summary = await ev(page, "document.getElementById('s5-summary-call').textContent");
  a11y.rootAttrs = await ev(page, `(() => { const r = document.querySelector('[data-view=call]'); return { role: r.getAttribute('role'), roledescription: r.getAttribute('aria-roledescription'), label: r.getAttribute('aria-label'), describedby: r.getAttribute('aria-describedby'), tabIndex: r.tabIndex }; })()`);
  // Reduced motion: a zoom key applies within one frame; playback follow pages instead of scrolling.
  await page.emulateMedia({ reducedMotion: "reduce" });
  const rm1 = await ev<{ before: number; after: number }>(page, `new Promise((res) => { const v = window.__s5.views.call; const before = v.span; v.zoomBy(0.5); requestAnimationFrame(() => res({ before, after: v.span })); })`);
  const rmPlay = await ev<{ frames: number; starts: number }>(page, `new Promise(async (res) => { const v = window.__s5.views.call; const m = v.o.media; v.setRange(1000, 2); m.currentTime = 1000.2; await m.play(); const starts = new Set(); let n = 0; const tick = () => { starts.add(v.start.toFixed(3)); if (++n < 180) requestAnimationFrame(tick); else { m.pause(); res({ frames: n, starts: starts.size }); } }; requestAnimationFrame(tick); })`);
  await page.emulateMedia({ reducedMotion: "no-preference" });
  const motion1 = await ev<{ before: number; after: number }>(page, `new Promise((res) => { const v = window.__s5.views.call; const before = v.span; v.zoomBy(0.5); requestAnimationFrame(() => requestAnimationFrame(() => res({ before, after: v.span }))); })`);
  a11y.reducedMotion = { zoomKeyAfterOneFrame: rm1, playbackFollowPagesDistinctStartsIn180Frames: rmPlay, withoutReducedMotionZoomAfterTwoFrames: motion1 };
  // Theme: tokens switch, the colormap does not.
  await ev(page, `window.__s5.views.call.setRange(700, 20)`);
  await page.waitForTimeout(300);
  const px1 = await ev<Record<string, number[][]>>(page, "window.__s5.samplePixels()");
  const fg1 = await ev(page, "getComputedStyle(document.querySelector('[data-view=call]')).getPropertyValue('--s5-fg')");
  await ev(page, "window.__s5.setTheme('dark')");
  await page.waitForTimeout(300);
  const px2 = await ev<Record<string, number[][]>>(page, "window.__s5.samplePixels()");
  const fg2 = await ev(page, "getComputedStyle(document.querySelector('[data-view=call]')).getPropertyValue('--s5-fg')");
  await page.screenshot({ path: path.join(OUT, "A-dark.png") });
  await ev(page, "window.__s5.setTheme('light')");
  a11y.theme = { fgLight: fg1, fgDark: fg2, spectrogramPixelUnchanged: JSON.stringify(px1.call) === JSON.stringify(px2.call) };
  // Bidi: the bot track (Hebrew with digits and Latin) at 10 s.
  await ev(page, `window.__s5.views.call.setRange(1203, 10)`);
  await page.waitForTimeout(300);
  a11y.bidi = await ev(page, `(() => {
    const track = document.querySelector('[data-view=call] [data-track=bot]');
    const words = [...track.querySelectorAll('bdi.s5-word')].filter((w) => w.style.display !== 'none' && w.textContent);
    const info = words.map((w) => ({ text: w.textContent, dir: getComputedStyle(w).direction, bidi: getComputedStyle(w).unicodeBidi, x: w.getBoundingClientRect().left }));
    const ordered = info.every((w, i) => i === 0 || w.x >= info[i - 1].x);
    return { trackDirection: getComputedStyle(track).direction, lang: track.getAttribute('lang'), shown: info.length, ordered, sample: info.slice(0, 8), tags: [...new Set(words.map((w) => w.tagName))] };
  })()`);
  await page.screenshot({ path: path.join(OUT, "A-bidi.png") });

  record("S5-A", { timings, bench, frames, changes, memory, a11y, renderers: await ev(page, "window.__s5.renderers()"), errors });
  expect(errors.filter((e) => !/GPU stall/.test(e))).toEqual([]);
});

test("S5-B: 12 views across two popouts, one renderer per window, forced context loss", async ({ page }) => {
  const errors = await open(page);
  const popups: Page[] = [];
  page.context().on("page", (p) => popups.push(p));
  await ev(page, `window.__s5.openPopout('popA', ${JSON.stringify(SIX("a", true))})`);
  await ev(page, `window.__s5.openPopout('popB', ${JSON.stringify(SIX("b"))}, { left: 50, top: 50, width: 1200, height: 900 })`);
  await page.waitForTimeout(1500);
  const probe1 = await ev<Record<string, { blank: number; window: string }>>(page, "window.__s5.probeAll()");
  const renderers1 = await ev(page, "window.__s5.renderers()");
  const memory12 = { page: await ev(page, "window.__s5.memory()"), processes: chromeMemory() };
  for (const [i, p] of popups.entries()) await p.screenshot({ path: path.join(OUT, `B-popout${i}.png`) });
  // Frames in a popout: zoom a call view there, and scroll a clip view.
  const frames = {
    popoutZoomIn: await ev(page, "window.__s5.zoomSweep('a-call-1', 180, {start:0,span:1800}, {start:899,span:2})"),
    popoutZoomOut: await ev(page, "window.__s5.zoomSweep('a-call-1', 180, {start:899,span:2}, {start:0,span:1800})"),
    popoutZoomInWarm: await ev(page, "window.__s5.zoomSweep('a-call-1', 180, {start:0,span:1800}, {start:899,span:2})"),
    popoutPlay2s: await ev(page, "window.__s5.playFollow('a-call-1', 2, 300)"),
    mainZoomWith12Open: await ev(page, "window.__s5.zoomSweep('call', 180, {start:0,span:1800}, {start:899,span:2})"),
  };
  // Forced loss in every window, then a colormap change every view must draw (grey ⇒ r = g = b).
  const loss = await ev(page, "window.__s5.loseAll(100)");
  const probe2 = await ev<Record<string, { blank: number }>>(page, "window.__s5.probeAll()");
  await ev(page, "window.__s5.setColormapAll('grey')");
  await page.waitForTimeout(500);
  const px = await ev<Record<string, number[][]>>(page, "window.__s5.samplePixels()");
  const notGrey = Object.entries(px).filter(([, l]) => l.some(([r, g, b]) => !(r === g && g === b)));
  const renderers2 = await ev(page, "window.__s5.renderers()");
  const blank = (p: Record<string, { blank: number }>) => Object.values(p).reduce((s, x) => s + x.blank, 0);
  record("S5-B", {
    views: Object.keys(probe1).length,
    windows: [...new Set(Object.values(probe1).map((x) => x.window))],
    blankBefore: blank(probe1),
    renderers1,
    memory12,
    frames,
    loss,
    blankAfterLoss: blank(probe2),
    viewsNotRedrawnAfterRestore: notGrey.map(([k]) => k),
    renderers2,
    errors,
  });
  expect(Object.keys(probe1).length).toBeGreaterThanOrEqual(14);
  expect(blank(probe1)).toBe(0);
  expect(blank(probe2)).toBe(0);
  expect(notGrey).toEqual([]);
});

test("S5-C: one WebGL context per track instead (the naive variant) against Chrome's limit", async ({ page }) => {
  const errors = await open(page, "?mode=per-track");
  await ev(page, `window.__s5.openPopout('popA', ${JSON.stringify(SIX("a"))})`);
  await ev(page, `window.__s5.openPopout('popB', ${JSON.stringify(SIX("b"))}, { left: 50, top: 50, width: 1200, height: 900 })`);
  await page.waitForTimeout(1500);
  await ev(page, "window.__s5.setColormapAll('grey')");
  await page.waitForTimeout(800);
  const px = await ev<Record<string, number[][]>>(page, "window.__s5.samplePixels()");
  const r = await ev<{ global: unknown; list: { lostNow: boolean }[] }>(page, "window.__s5.renderers()");
  record("S5-C", {
    renderersGlobal: r.global,
    renderers: r.list.length,
    lostNow: r.list.filter((x) => x.lostNow).length,
    viewsThatCouldNotRedraw: Object.entries(px).filter(([, l]) => l.some(([a, b, c]) => !(a === b && b === c))).map(([k]) => k),
    contextWarnings: errors.filter((e) => /context/i.test(e)).slice(0, 3),
    contextWarningCount: errors.filter((e) => /context/i.test(e)).length,
  });
});

test("S5-D: wavesurfer.js 8 variant — external axis, popout, MSE, regions; bundle sizes", async ({ page }) => {
  const errors = await open(page);
  const P = { views: [{ id: "wsdrv", fixture: "call", spectrogram: false, features: false }], ws: true };
  await ev(page, `window.__s5.openPanel('wsp', ${JSON.stringify(P)})`);
  await page.waitForTimeout(800);
  const res: Record<string, unknown> = {};
  const probe = () => ev<Record<string, unknown>>(page, "window.__s5.ws.probe()");
  // In the main window (floating group): follow the axis, drag the region.
  await ev(page, "(() => { window.__s5.setWsFollow(true); window.__s5.views.wsdrv.setRange(0, 60); })()");
  await page.waitForTimeout(500);
  const dragRegion = async (p: Page) => {
    const before = (await ev<{ regionStart: number }>(page, "window.__s5.ws.probe()")).regionStart;
    const loc = p.locator('[part~="region"]').first();
    const box = (await loc.count()) ? await loc.boundingBox({ timeout: 5000 }) : null;
    if (!box) return { before, after: before, moved: false, note: "region element not found" };
    await p.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await p.mouse.down();
    await p.mouse.move(box.x + box.width / 2 + 120, box.y + box.height / 2, { steps: 12 });
    await p.mouse.up();
    await page.waitForTimeout(200);
    const after = (await ev<{ regionStart: number }>(page, "window.__s5.ws.probe()")).regionStart;
    return { before, after, moved: Math.abs(after - before) > 0.01 };
  };
  await page.screenshot({ path: path.join(OUT, "D-ws-main.png") });
  res.main = { probe: await probe(), regionDrag: await dragRegion(page) };
  // Pop the same panel out: the views and wavesurfer are rebuilt in the popout document.
  const popupP = page.context().waitForEvent("page");
  await ev(page, `window.__s5.openPopout('wsp', ${JSON.stringify(P)})`);
  const popup = await popupP;
  await page.waitForTimeout(1500);
  await ev(page, "(() => { window.__s5.setWsFollow(true); window.__s5.views.wsdrv.setRange(0, 60); })()");
  await page.waitForTimeout(500);
  const pr = await probe();
  await popup.screenshot({ path: path.join(OUT, "D-ws-popout.png") });
  const drag = await dragRegion(popup);
  // ResizeObserver: shrink the popout; does wavesurfer re-render to the new width (Cadence's view does)?
  const w0 = await ev<{ ws: number; cadence: number }>(page, "({ ws: window.__s5.ws.ws.getWrapper().parentElement.clientWidth, cadence: window.__s5.views.wsdrv.root.clientWidth })");
  await popup.setViewportSize({ width: 800, height: 700 });
  await page.waitForTimeout(800);
  const w1 = await ev<{ ws: number; cadence: number; wsCanvasCssWidth: number }>(page, "({ ws: window.__s5.ws.ws.getWrapper().parentElement.clientWidth, cadence: window.__s5.views.wsdrv.root.clientWidth, wsWidthOption: window.__s5.ws.ws.getWidth(), wsCanvasCssWidth: Math.max(...[...window.__s5.ws.ws.getWrapper().querySelectorAll('canvas')].map((c) => c.getBoundingClientRect().width)) })");
  // Playback through MSE in the popout.
  const play = await ev(page, "new Promise(async (res) => { const m = window.__s5.media.ws.audio; m.currentTime = 100; await m.play(); setTimeout(() => { const t = m.currentTime; m.pause(); res({ advancedS: Math.round((t - 100) * 100) / 100, wsTime: window.__s5.ws.ws.getCurrentTime(), mse: window.__s5.media.ws.stats }); }, 1500); })");
  // Frames in the popout: the Cadence waveform alone vs. wavesurfer following the same axis.
  await ev(page, "window.__s5.setWsFollow(false)");
  const cadence = {
    zoomIn: await ev(page, "window.__s5.zoomSweep('wsdrv', 180, {start:0,span:1800}, {start:899,span:2})"),
    zoomOut: await ev(page, "window.__s5.zoomSweep('wsdrv', 180, {start:899,span:2}, {start:0,span:1800})"),
    scroll30s: await ev(page, "window.__s5.scrollSweep('wsdrv', 30, 240, 600)"),
  };
  await ev(page, "window.__s5.setWsFollow(true)");
  const withWs = {
    zoomIn: await ev(page, "window.__s5.zoomSweep('wsdrv', 180, {start:0,span:1800}, {start:899,span:2})"),
    zoomOut: await ev(page, "window.__s5.zoomSweep('wsdrv', 180, {start:899,span:2}, {start:0,span:1800})"),
    scroll30s: await ev(page, "window.__s5.scrollSweep('wsdrv', 30, 240, 600)"),
  };
  const followCheck = await ev(page, "new Promise((res) => { const v = window.__s5.views.wsdrv; v.setRange(1234, 10); setTimeout(() => res({ axisStart: v.start, wsScrollTime: Math.round(window.__s5.ws.ws.getScroll() / (window.__s5.ws.ws.getWrapper().scrollWidth / 1800) * 100) / 100 }), 300); })");
  res.popout = { probe: pr, regionDrag: drag, resize: { before: w0, after: w1 }, play, followCheck, frames: { cadenceWaveformOnly: cadence, cadencePlusWavesurferFollowing: withWs } };
  // Bundle sizes (minified + gzip): wavesurfer core + regions + timeline + minimap, and the Cadence view code.
  const { build } = await import("vite");
  const sizes: Record<string, { minKB: number; gzipKB: number }> = {};
  for (const [name, entry] of Object.entries({ wavesurfer: "src/bundle-ws.ts", cadenceAudioView: "src/bundle-cadence.ts" })) {
    const out = (await build({
      configFile: false,
      logLevel: "silent",
      root: import.meta.dirname,
      resolve: { alias: { "@pffft": path.resolve(import.meta.dirname, "node_modules/@echogarden/pffft-wasm/dist/simd") } },
      build: { write: false, minify: true, lib: { entry: path.resolve(import.meta.dirname, entry), formats: ["es"], fileName: name } },
    })) as { output: { type: string; code?: string; source?: string | Uint8Array; fileName: string }[] }[] | { output: { type: string; code?: string; fileName: string }[] };
    const outputs = (Array.isArray(out) ? out : [out]).flatMap((o) => o.output);
    let min = 0;
    let gz = 0;
    for (const o of outputs) {
      const src = o.type === "chunk" ? (o.code ?? "") : "";
      if (!src) continue;
      min += Buffer.byteLength(src);
      gz += gzipSync(src).length;
    }
    sizes[name] = { minKB: Math.round(min / 102.4) / 10, gzipKB: Math.round(gz / 102.4) / 10 };
  }
  res.bundle = sizes;
  record("S5-D", { ...res, errors });
});

test("S5-M: memory of the 30-minute call against an empty shell (renderer + GPU process PSS)", async ({ browser }) => {
  const out: Record<string, unknown> = {};
  const variants = (process.env.S5_MEM || "empty,gl,call-idle,call,empty,gl,call-idle,call").split(",");
  for (const only of variants) {
    const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const page = await ctx.newPage();
    const [base, ...flags] = only.split("+");
    await open(page, `?only=${base!.replace("-idle", "")}${flags.map((f) => `&${f}`).join("")}`);
    if (base === "call") {
      await ev(page, "window.__s5.zoomSweep('call', 180, {start:0,span:1800}, {start:899,span:2})");
      await ev(page, "window.__s5.zoomSweep('call', 180, {start:899,span:2}, {start:0,span:1800})");
      await ev(page, "window.__s5.scrollSweep('call', 30, 240, 600)");
      if (!flags.includes("nomedia")) await ev(page, "window.__s5.playFollow('call', 2, 700)");
    }
    const cdp = await ctx.newCDPSession(page);
    await cdp.send("HeapProfiler.collectGarbage");
    await page.waitForTimeout(1000);
    await cdp.send("Performance.enable");
    const metrics = (await cdp.send("Performance.getMetrics")).metrics;
    const heap = metrics.find((m) => m.name === "JSHeapUsedSize")?.value ?? 0;
    out[`${only}-${Object.keys(out).length}`] = { processes: chromeMemory(), jsHeapMB: Math.round(heap / 1e5) / 10, page: await ev(page, "window.__s5.memory()") };
    await ctx.close();
    await new Promise((r) => setTimeout(r, 1500));
  }
  record("S5-M", out);
});
