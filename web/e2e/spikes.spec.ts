import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { Page } from "@playwright/test";
import { expect, floatBounds, newProject, openWorkspace, test } from "./fixtures";

// Measurements for spikes S1, S3, S4 (docs/spikes). Results land in test-results/spikes/<id>.json and are copied
// into the spike's Result section. Budgets: 60 fps drag with 10 floats; restore < 300 ms; zero hidden
// subscriptions; no unthemed Dockview surface.

// Tracing snapshots the DOM on every pointer action; that work lands in the frames we measure. Off here.
test.use({ trace: "off" });

const OUT = path.resolve(import.meta.dirname, "../test-results/spikes");
function record(id: string, data: unknown): void {
  mkdirSync(OUT, { recursive: true });
  writeFileSync(path.join(OUT, `${id}.json`), JSON.stringify(data, null, 2));
  console.log(id, JSON.stringify(data));
}

export type Cadence = {
  dock(): {
    addPanel(o: unknown): unknown;
    getPanel(id: string): { group: unknown } | undefined;
    clear(): void;
    toJSON(): { floatingGroups?: unknown[] };
    groups: unknown[];
  };
  openPanel(id: string, o?: unknown): string;
  events: { subscriptionOwners(): string[] };
  sync: { getState(): { lastRestoreMs?: number; rev?: number; saving: boolean; restoring: boolean } };
};
// Evaluated as plain JS in the page.
const cadence = "window.__cadence";

function pct(xs: number[], p: number): number {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]!;
}

test("S1: drag with 10 floats at 60 fps, tab docking intact, bounds in toJSON()", async ({ page, request }) => {
  const slug = await newProject(request, "S1");
  await openWorkspace(page, slug, "Training", "?spikes");
  await page.evaluate(`{
    const c = ${cadence};
    for (let i = 0; i < 10; i++) {
      const id = "stub-" + String(i + 1).padStart(2, "0");
      c.openPanel(id, { floating: { x: 40 + i * 60, y: 40 + i * 30, width: 320, height: 220 } });
    }
  }`);
  await expect(page.locator(".dv-resize-container")).toHaveCount(10);
  // Drag the top-most float (stub-10) by its tab bar and record frame times between pointer down and up only.
  // Each pass is paired with a control: the same pointer path without a drag, so frames the environment drops
  // (a shared host, CDP event cadence in headless Chromium) are not charged to the shell. Long animation frames
  // (Chromium LoAF, > 50 ms of work) are counted directly.
  const handle = page.locator(".dv-resize-container").last().locator(".dv-void-container");
  const startRecording = () =>
    page.evaluate(() => {
      const w = window as unknown as { __frames: number[]; __rec: boolean; __loaf: number };
      w.__frames = [];
      w.__rec = true;
      w.__loaf = 0;
      try {
        new PerformanceObserver((l) => (w.__loaf += l.getEntries().length)).observe({ type: "long-animation-frame", buffered: false });
      } catch {
        /* LoAF unsupported */
      }
      let last = performance.now();
      const tick = (t: number) => {
        if (!w.__rec) return;
        w.__frames.push(t - last);
        last = t;
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
  const stopRecording = () =>
    page.evaluate(() => {
      const w = window as unknown as { __frames: number[]; __rec: boolean; __loaf: number };
      w.__rec = false;
      return { frames: w.__frames.slice(2), loaf: w.__loaf };
    });
  const path = async (x: number, y: number) => {
    for (let i = 0; i < 180; i++) {
      const t = i / 180;
      await page.mouse.move(x - 300 * Math.sin(t * Math.PI * 2), y + 150 * t);
    }
  };
  const dropped = (f: number[]) => f.filter((d) => d > 20).length / Math.max(1, f.length);
  const passes: { p50: number; p95: number; frames: number; dropped: number; controlDropped: number; longFrames: number }[] = [];
  for (let pass = 0; pass < 3; pass++) {
    const h = (await handle.boundingBox())!;
    await startRecording();
    await path(h.x + 20, h.y + h.height / 2 + 400); // control: same moves over the grid, no drag
    const control = await stopRecording();
    await page.mouse.move(h.x + 20, h.y + h.height / 2);
    await page.mouse.down();
    await startRecording();
    await path(h.x + 20, h.y + h.height / 2);
    const drag = await stopRecording();
    await page.mouse.up();
    passes.push({
      p50: pct(drag.frames, 50),
      p95: pct(drag.frames, 95),
      frames: drag.frames.length,
      dropped: dropped(drag.frames),
      controlDropped: dropped(control.frames),
      longFrames: drag.loaf,
    });
  }
  const p95 = pct(passes.map((x) => x.p95), 50);
  const bounds = await floatBounds(page);
  const json = await page.evaluate(`${cadence}.dock().toJSON().floatingGroups.length`);
  // Tab docking still works: drop a floating tab onto the Project document's group.
  const floatsBefore = await page.locator(".dv-resize-container").count();
  // Floats cover most of the centre; drop on the Project group's uncovered lower-right area.
  const src = (await page.locator('.dv-resize-container [data-tab="stub-01"]').boundingBox())!;
  const dst = (await page.locator('[data-panel="project"]').boundingBox())!;
  await page.mouse.move(src.x + src.width / 2, src.y + src.height / 2);
  await page.mouse.down();
  await page.mouse.move(dst.x + dst.width - 60, dst.y + dst.height - 60, { steps: 20 });
  await page.mouse.up();
  await page.waitForTimeout(300);
  const floatsAfter = await page.locator(".dv-resize-container").count();
  record("S1", {
    dockview: "8.3.1",
    floats: 10,
    passes: passes.map((x) => ({
      frames: x.frames,
      p50: Math.round(x.p50 * 10) / 10,
      p95: Math.round(x.p95 * 10) / 10,
      droppedShare: Math.round(x.dropped * 1000) / 10,
      controlDroppedShare: Math.round(x.controlDropped * 1000) / 10,
      longAnimationFrames: x.longFrames,
    })),
    frameTimeMsP95Median: Math.round(p95 * 10) / 10,
    floatingGroupsInToJSON: json,
    tabDockedIntoGrid: floatsAfter === floatsBefore - 1,
    lastBounds: bounds,
  });
  expect(json).toBe(10);
  expect(floatsAfter).toBe(floatsBefore - 1);
  // Budget: 60 fps while dragging — the median frame is one vsync, no long frames, and the drag drops no more
  // frames than the same pointer path without a drag (+2 points).
  expect(pct(passes.map((x) => x.p50), 50)).toBeLessThan(1000 / 60 + 1);
  for (const x of passes) {
    expect(x.longFrames).toBe(0);
    expect(x.dropped).toBeLessThanOrEqual(x.controlDropped + 0.02);
  }
});

async function buildTwentyPanelWorkspace(page: Page, renderer: string): Promise<void> {
  await page.evaluate(`{
    const c = ${cadence};
    const api = c.dock();
    api.clear();
    const ids = Array.from({ length: 20 }, (_, i) => "stub-" + String(i + 1).padStart(2, "0"));
    const dirs = [undefined, "right", "below", "right"];
    let ref;
    for (let g = 0; g < 4; g++) {
      for (let t = 0; t < 5; t++) {
        const id = ids[g * 5 + t];
        const position = t === 0 ? (ref ? { referencePanel: ref, direction: dirs[g] } : undefined) : { referencePanel: ids[g * 5], direction: "within" };
        api.addPanel({ id, component: "panel", title: id, renderer: "${renderer}", params: { panel: id, loc: "right" }, ...(position ? { position } : {}) });
      }
      ref = ids[g * 5];
    }
  }`);
}

async function restoreRun(page: Page, url: string): Promise<{ ms: number; subs: number; visible: number; heapMB: number | null }> {
  await page.goto(url);
  await page.waitForFunction(`(() => { const s = ${cadence}?.sync.getState(); return s && !s.restoring && s.lastRestoreMs !== undefined; })()`);
  await page.waitForTimeout(300);
  return page.evaluate(`(() => {
    const c = ${cadence};
    const owners = c.events.subscriptionOwners().filter((o) => o.startsWith("panel:stub-"));
    const visible = document.querySelectorAll("[data-stub]").length;
    const m = performance.memory;
    return { ms: c.sync.getState().lastRestoreMs, subs: owners.length, visible, heapMB: m ? Math.round(m.usedJSHeapSize / 1e5) / 10 : null };
  })()`);
}

test("S4: a 20-panel workspace restores under 300 ms with zero hidden subscriptions", async ({ page, request }) => {
  const slug = await newProject(request, "S4");
  const results: Record<string, unknown> = {};
  for (const mode of ["onlyWhenVisible", "always"] as const) {
    const ws = mode === "always" ? "Always" : "Visible";
    await openWorkspace(page, slug, ws, "?spikes");
    await buildTwentyPanelWorkspace(page, mode);
    await expect(page.getByTestId("workspace-sync")).toContainText("Saved", { timeout: 5000 });
    const runs = [];
    for (let i = 0; i < 5; i++) runs.push(await restoreRun(page, `/p/${slug}/w/${ws}?spikes`));
    const ms = runs.map((r) => r.ms);
    results[mode] = {
      restoreMsMedian: Math.round(pct(ms, 50)),
      restoreMsMax: Math.round(Math.max(...ms)),
      subscriptions: runs[0]!.subs,
      renderedStubs: runs[0]!.visible,
      heapMB: runs[0]!.heapMB,
    };
    expect(pct(ms, 50)).toBeLessThan(300);
    // Four groups show four panels; the other sixteen hold no subscription, whatever the renderer mode.
    expect(runs[0]!.subs).toBe(4);
    expect(runs[0]!.visible).toBe(mode === "always" ? 20 : 4);
  }
  record("S4", { panels: 20, groups: 4, ...results });
});

/**
 * The centre of the largest visible docked panel outside the group that holds `tab`: a drop target that does not
 * depend on which panels a default workspace opens.
 */
async function dropTarget(page: Page, tab: string): Promise<{ panel: string; x: number; y: number }> {
  const target = await page.evaluate((tabId) => {
    const from = document.querySelector(`[data-tab="${tabId}"]`)?.closest(".dv-groupview");
    let best: { panel: string; x: number; y: number; area: number } | undefined;
    for (const el of Array.from(document.querySelectorAll<HTMLElement>("[data-panel]"))) {
      if (el.closest(".dv-resize-container") || (from && from.contains(el))) continue;
      const r = el.getBoundingClientRect();
      const area = r.width * r.height;
      if (area > (best?.area ?? 0)) best = { panel: el.dataset.panel!, x: r.x + r.width / 2, y: r.y + r.height / 2, area };
    }
    return best;
  }, tab);
  expect(target, `a visible panel outside the group of the ${tab} tab`).toBeDefined();
  return target!;
}

test("S3: every Dockview surface uses theme tokens, light and dark", async ({ page, request }) => {
  const report: Record<string, unknown> = {};
  for (const scheme of ["light", "dark"] as const) {
    // A project per pass: the drop below is saved (autosave flushes on page hide), so a second pass in the same project
    // would restore the moved tab instead of the default layout both schemes must scan.
    const slug = await newProject(request, `S3 ${scheme}`);
    await page.emulateMedia({ colorScheme: scheme });
    await openWorkspace(page, slug, "Training");
    await page.locator('[data-tab="help"]').click();
    await page.evaluate(`${cadence}.openPanel("inspector", { floating: { x: 200, y: 120, width: 360, height: 260 } })`);
    // Start a tab drag so the drop overlay exists while we scan.
    const tab = page.locator('[data-tab="library"]');
    const tb = (await tab.boundingBox())!;
    const target = await dropTarget(page, "library");
    await page.mouse.move(tb.x + tb.width / 2, tb.y + tb.height / 2);
    await page.mouse.down();
    await page.mouse.move(target.x, target.y, { steps: 8 });
    const scan = await page.evaluate(() => {
      // Every colour a token can resolve to, measured through a probe element.
      const probe = document.createElement("div");
      document.body.appendChild(probe);
      const allowed = new Set<string>(["rgba(0, 0, 0, 0)", "transparent"]);
      const root = getComputedStyle(document.documentElement);
      const vars: string[] = [];
      for (const sheet of Array.from(document.styleSheets)) {
        let rules: CSSRuleList;
        try {
          rules = sheet.cssRules;
        } catch {
          continue;
        }
        for (const r of Array.from(rules)) {
          const text = r.cssText;
          for (const m of text.matchAll(/(--(?:slate|indigo|blue|grass|amber|red|cadence)[a-z0-9-]*)\s*:/g)) vars.push(m[1]!);
        }
      }
      for (const v of new Set(vars)) {
        if (!root.getPropertyValue(v)) continue;
        probe.style.color = `var(${v})`;
        allowed.add(getComputedStyle(probe).color);
      }
      probe.style.color = "white";
      allowed.add(getComputedStyle(probe).color);
      // Translucent values can be mid-transition (Dockview fades scrollbars): match them by their RGB.
      const rgb = (c: string) => c.replace(/^rgba?\(([^,]+),([^,]+),([^,)]+).*$/, "$1,$2,$3").replace(/\s/g, "");
      const allowedRgb = new Set([...allowed].map(rgb));
      probe.remove();
      const offenders: Record<string, string[]> = {};
      const els = Array.from(document.querySelectorAll(".dv-dockview *, .dv-dockview")) as HTMLElement[];
      for (const el of els) {
        const cs = getComputedStyle(el);
        if (cs.visibility === "hidden" || cs.display === "none") continue;
        const props: [string, string][] = [
          ["background", cs.backgroundColor],
          ["border", el.offsetWidth && parseFloat(cs.borderTopWidth) ? cs.borderTopColor : ""],
        ];
        if (el.childNodes.length && Array.from(el.childNodes).some((n) => n.nodeType === 3 && n.textContent?.trim())) props.push(["color", cs.color]);
        for (const [p, value] of props) {
          if (!value || allowed.has(value) || (value.startsWith("rgba") && allowedRgb.has(rgb(value)))) continue;
          const key = `${el.className.toString().split(" ").filter((c) => c.startsWith("dv-") || c.startsWith("cadence")).slice(0, 2).join(".") || el.tagName.toLowerCase()} ${p}`;
          (offenders[key] ??= []).push(value);
        }
      }
      return { scanned: els.length, overlay: !!document.querySelector(".dv-drop-target-anchor, .dv-drop-target-selection, .dv-drop-target"), offenders: Object.fromEntries(Object.entries(offenders).map(([k, v]) => [k, [...new Set(v)]])) };
    });
    await page.mouse.up();
    report[scheme] = { dropTarget: target.panel, ...scan };
    expect(scan.overlay, `${scheme}: the drop overlay is scanned`).toBe(true);
    expect(scan.offenders, `${scheme}: unthemed surfaces`).toEqual({});
  }
  record("S3", report);
});
