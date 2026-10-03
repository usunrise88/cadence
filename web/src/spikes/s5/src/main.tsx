// Spike S5 harness (throwaway; docs/spikes/S5-audio-view.md). A Dockview layout with the 30-minute call docked, the
// 20-second clip floating, and popouts on demand; window.__s5 drives the measurements (s5.spec.ts).
import "dockview-react/dist/styles/dockview.css";
import "./s5.css";
import { DockviewReact, type DockviewApi, type DockviewReadyEvent, type IDockviewPanelProps } from "dockview-react";
import { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { mseAudio, type MseStats } from "./mse";
import { allRenderers, globalStats, sharedRenderer, type Colormap } from "./renderer";
import { schedulerFor } from "./scheduler";
import { Features, Peaks, PyramidSource, StftSource, stftWorker, type SpecSource, type WordTrackData } from "./sources";
import { AudioView } from "./view";
import { WsVariant } from "./ws-variant";

const FX = "/fixtures";
const params = new URLSearchParams(location.search);
const MODE = (params.get("mode") ?? "shared") as "shared" | "per-track";
// Memory baselines: ?only=empty (Dockview, no audio) and ?only=call (the 30-minute call alone).
const ONLY = params.get("only");

type Fixture = { duration: number; spec: SpecSource; peaks: Peaks; words: WordTrackData[]; features: Features; webm: string };
const fixtures: Record<string, Fixture> = {};
const views: Record<string, AudioView> = {};
const media: Record<string, { audio: HTMLAudioElement; stats: MseStats }> = {};
const popouts: Window[] = [];
let ws: WsVariant | undefined;
let api: DockviewApi | undefined;
const timings: Record<string, number> = {};

async function json<T>(url: string): Promise<T> {
  return (await (await fetch(url)).json()) as T;
}
async function bytes(url: string): Promise<ArrayBuffer> {
  return (await fetch(url)).arrayBuffer();
}

async function loadFixtures() {
  const meta = await json<{ call: { duration: number; analysisFrames: number; analysisChunk: number }; clip: { duration: number; analysisFrames: number } }>(`${FX}/fixtures.json`);
  let t = performance.now();
  const { src: callSpec } = await PyramidSource.load(`${FX}/tiles-call`);
  const callPeaks = new Peaks(2, new Int8Array(await bytes(`${FX}/tiles-call/peaks.i8`)));
  const callWords = (await json<{ tracks: WordTrackData[] }>(`${FX}/words-call.json`)).tracks;
  timings.callLoadMs = performance.now() - t;
  // 8 kHz origin upsampled to 16 kHz: mel filters above 4 kHz hold dither only — first such filter of 80 (0–8 kHz).
  const melAbove4k = Math.round((2595 * Math.log10(1 + 4000 / 700) * 81) / (2595 * Math.log10(1 + 8000 / 700))) - 1;
  fixtures.call = {
    duration: meta.call.duration,
    spec: callSpec,
    peaks: callPeaks,
    words: callWords,
    features: new Features((i) => `${FX}/analysis/call/${i}.f16`, meta.call.analysisFrames, meta.call.analysisChunk, 80, melAbove4k),
    webm: `${FX}/call.webm`,
  };
  t = performance.now();
  const pcm = new Float32Array(await bytes(`${FX}/clip.f32`));
  const peaks = Peaks.fromPcm(pcm, 16000);
  const r = await stftWorker({ pcm, backend: "wasm" });
  timings.clipStftMs = r.ms;
  timings.clipLoadMs = performance.now() - t;
  fixtures.clip = {
    duration: meta.clip.duration,
    spec: new StftSource("clip", r.u8, r.frames),
    peaks,
    words: (await json<{ tracks: WordTrackData[] }>(`${FX}/words-clip.json`)).tracks,
    features: new Features(() => `${FX}/analysis/clip.f16`, meta.clip.analysisFrames, meta.clip.analysisFrames),
    webm: `${FX}/clip.webm`,
  };
}

type ViewSpec = { id: string; fixture: "call" | "clip"; compact?: boolean; media?: boolean; features?: boolean; spectrogram?: boolean };
type PanelParams = { views: ViewSpec[]; ws?: boolean };

function AudioPanel(props: IDockviewPanelProps<PanelParams>) {
  const ref = useRef<HTMLDivElement>(null);
  const [home, setHome] = useState(0);
  useEffect(() => {
    // A popout moves the panel's DOM into another document: rebuild the views there (their renderer is per window).
    const d = props.api.onDidLocationChange(() => setTimeout(() => setHome((h) => h + 1), 50));
    return () => d.dispose();
  }, [props.api]);
  useEffect(() => {
    const el = ref.current!;
    const doc = el.ownerDocument;
    const made: AudioView[] = [];
    const offs: (() => void)[] = [];
    let cancelled = false;
    void (async () => {
      for (const v of props.params.views) {
        const fx = fixtures[v.fixture]!;
        let m: HTMLAudioElement | undefined;
        if (v.media && !params.has("nomedia")) {
          const r = await mseAudio(doc, fx.webm);
          if (cancelled) return;
          media[v.id] = { audio: r.audio, stats: r.stats };
          el.append(r.audio);
          offs.push(() => (r.audio.pause(), r.audio.remove()));
          m = r.audio;
        }
        const view = new AudioView(el, {
          id: v.id,
          title: v.fixture === "call" ? "Call · 30 min · 8 kHz stereo" : "Clip · 20 s · 16 kHz",
          duration: fx.duration,
          spec: fx.spec,
          peaks: fx.peaks,
          words: params.has("nowords") ? [] : v.compact ? fx.words.slice(0, 1) : fx.words,
          features: v.features === false || params.has("nofeatures") ? undefined : fx.features,
          media: m,
          mode: MODE,
          compact: v.compact,
          spectrogram: v.spectrogram,
        });
        views[v.id] = view;
        made.push(view);
      }
      if (props.params.ws) {
        const fx = fixtures.call!;
        const host = doc.createElement("div");
        host.className = "s5-ws";
        el.append(host);
        const r = await mseAudio(doc, fx.webm);
        offs.push(() => (r.audio.pause(), r.audio.remove(), host.remove()));
        media.ws = { audio: r.audio, stats: r.stats };
        ws = new WsVariant(host, fx.peaks, fx.duration, r.audio);
        const driver = made[0];
        const mine = ws;
        if (driver) offs.push(schedulerFor(doc.defaultView!).hook(() => wsFollow && mine.follow(driver.start, driver.span)));
      }
    })();
    return () => {
      cancelled = true;
      for (const off of offs) off();
      for (const v of made) {
        v.destroy();
        if (views[v.o.id] === v) delete views[v.o.id];
      }
      if (props.params.ws) ws?.destroy();
    };
  }, [props.params, home]);
  return <div ref={ref} className="s5-panel" />;
}
let wsFollow = false;

function App() {
  const ready = (e: DockviewReadyEvent) => {
    api = e.api;
    if (ONLY === "empty" || ONLY === "gl") return;
    e.api.addPanel({ id: "call", component: "audio", title: "Call", params: { views: [{ id: "call", fixture: "call", media: true }] } satisfies PanelParams });
    if (ONLY === "call") return;
    e.api.addPanel({
      id: "clip",
      component: "audio",
      title: "Clip",
      params: { views: [{ id: "clip", fixture: "clip", media: true }] } satisfies PanelParams,
      floating: { x: 760, y: 420, width: 620, height: 470 },
    });
  };
  return <DockviewReact className="dockview-theme-light" components={{ audio: AudioPanel }} popoutUrl="/popout.html" onReady={ready} />;
}

// ---- measurement API -------------------------------------------------------------------------------------------

function pct(xs: number[], p: number) {
  const s = [...xs].sort((a, b) => a - b);
  return s.length ? s[Math.min(s.length - 1, Math.floor((p / 100) * s.length))]! : 0;
}
function summary(intervals: number[], work: number[], loaf: number) {
  const r = (x: number) => Math.round(x * 10) / 10;
  return {
    frames: intervals.length,
    p50: r(pct(intervals, 50)),
    p95: r(pct(intervals, 95)),
    max: r(Math.max(0, ...intervals)),
    droppedShare: r((intervals.filter((d) => d > 20).length / Math.max(1, intervals.length)) * 100),
    workP50: r(pct(work, 50)),
    workP95: r(pct(work, 95)),
    longAnimationFrames: loaf,
  };
}

/** Runs `step(k)` before the views render on each of `frames` frames of the view's window; records frame intervals. */
function drive(view: AudioView, frames: number, step: (k: number) => void) {
  const win = view.win;
  const sched = schedulerFor(win);
  return new Promise<ReturnType<typeof summary>>((resolve) => {
    const intervals: number[] = [];
    const work: number[] = [];
    let loaf = 0;
    let obs: PerformanceObserver | undefined;
    try {
      obs = new (win as unknown as typeof globalThis).PerformanceObserver((l) => (loaf += l.getEntries().length));
      obs.observe({ type: "long-animation-frame", buffered: false });
    } catch {
      /* unsupported */
    }
    let k = 0;
    let last = 0;
    const off = sched.hook((now) => {
      if (k > 0) {
        intervals.push(now - last);
        work.push(sched.lastWorkMs);
      }
      last = now;
      if (k >= frames) {
        off();
        obs?.disconnect();
        resolve(summary(intervals.slice(2), work.slice(2), loaf));
        return;
      }
      step(k++);
    });
  });
}

const s5 = {
  mode: MODE,
  ready: (async () => {
    if (ONLY === "gl") sharedRenderer(window); // baseline: one WebGL2 context, nothing drawn
    if (ONLY !== "empty" && ONLY !== "gl") await loadFixtures();
    createRoot(document.getElementById("root")!).render(<App />);
    for (let i = 0; i < 200 && ONLY !== "empty" && ONLY !== "gl" && !(views.call && (views.clip || ONLY === "call")); i++) await new Promise((r) => setTimeout(r, 50));
    return true;
  })(),
  views,
  timings,
  media,
  get ws() {
    return ws;
  },
  setWsFollow(on: boolean) {
    wsFollow = on;
  },
  popouts,
  async openPanel(id: string, p: PanelParams) {
    api!.addPanel({ id, component: "audio", title: id, params: p, floating: { x: 40, y: 40, width: 1000, height: 600 } });
    for (let i = 0; i < 200 && !p.views.every((v) => views[v.id]?.drawCount); i++) await new Promise((r) => setTimeout(r, 50));
    if (p.ws) for (let i = 0; i < 200 && !ws; i++) await new Promise((r) => setTimeout(r, 50));
  },
  async openPopout(id: string, p: PanelParams, box = { left: 0, top: 0, width: 1200, height: 900 }) {
    const panel = api!.getPanel(id) ?? api!.addPanel({ id, component: "audio", title: id, params: p, floating: { x: 40, y: 40, width: 600, height: 400 } });
    let win: Window | undefined;
    await api!.addPopoutGroup(panel, { popoutUrl: "/popout.html", position: box, onDidOpen: (e) => (win = e.window) });
    if (win) popouts.push(win);
    for (let i = 0; i < 200; i++) {
      if (p.views.every((v) => views[v.id] && views[v.id]!.doc !== document && views[v.id]!.drawCount > 0) && (!p.ws || ws?.container.ownerDocument !== document)) break;
      await new Promise((r) => setTimeout(r, 50));
    }
    return popouts.length - 1;
  },
  zoomSweep(id: string, frames = 180, from = { start: 0, span: 0 }, to = { start: 0, span: 2 }) {
    const v = views[id]!;
    const a = from.span ? from : { start: 0, span: v.o.duration };
    return drive(v, frames, (k) => {
      const e = k / (frames - 1);
      const span = Math.exp(Math.log(a.span) + (Math.log(to.span) - Math.log(a.span)) * e);
      const center = a.start + a.span / 2 + (to.start + to.span / 2 - (a.start + a.span / 2)) * e;
      v.setRange(center - span / 2, span);
    });
  },
  scrollSweep(id: string, span: number, frames = 240, start = 600, widthsPerSecond = 1) {
    const v = views[id]!;
    v.setRange(start, span);
    return drive(v, frames, (k) => v.setRange(start + (k * span * widthsPerSecond) / 60, span));
  },
  async playFollow(id: string, span: number, at: number, frames = 240) {
    const v = views[id]!;
    const m = v.o.media!;
    v.follow = true;
    v.setRange(at - span * 0.3, span);
    m.currentTime = at;
    await m.play();
    const t0 = m.currentTime;
    const r = await drive(v, frames, () => undefined);
    const advanced = m.currentTime - t0;
    m.pause();
    return { ...r, mediaAdvancedS: Math.round(advanced * 100) / 100, startFollowed: Math.round((v.start - (m.currentTime - span * 0.3)) * 100) / 100 };
  },
  /** A range, gain or colormap change: time from the change to pixels in the view's canvas (GPU finished). */
  uniformChange(id: string, change: Partial<{ gainDb: number; rangeDb: number; colormap: Colormap; axis: "mel" | "hz" }>) {
    const v = views[id]!;
    const r = v.renderer;
    const before = r.stats.uploads;
    v.render();
    r.finish();
    const t0 = performance.now();
    v.set(change);
    v.render();
    r.finish();
    const ms = performance.now() - t0;
    return { ms: Math.round(ms * 100) / 100, uploads: r.stats.uploads - before };
  },
  probeAll() {
    return Object.fromEntries(Object.entries(views).map(([k, v]) => [k, { ...v.probe(), window: v.doc === document ? "main" : `popout${popouts.indexOf(v.win)}`, draws: v.drawCount }]));
  },
  renderers() {
    return { global: { ...globalStats }, list: [...allRenderers].map((r) => ({ window: r.win === window ? "main" : `popout${popouts.indexOf(r.win)}`, ...r.stats, lostNow: r.lost })) };
  },
  async loseAll(ms = 100) {
    const rs = [...allRenderers];
    for (const r of rs) r.loseContext(ms);
    const t0 = performance.now();
    for (let i = 0; i < 200 && rs.some((r) => r.lost || r.stats.restored === 0); i++) await new Promise((res) => setTimeout(res, 20));
    const restoredMs = performance.now() - t0;
    for (const v of Object.values(views)) v.invalidate();
    await new Promise((res) => setTimeout(res, 300));
    return { renderers: rs.length, restoredMs: Math.round(restoredMs), stats: rs.map((r) => ({ lost: r.stats.lost, restored: r.stats.restored })) };
  },
  memory() {
    const m = (performance as unknown as { memory?: { usedJSHeapSize: number; totalJSHeapSize: number } }).memory;
    const srcs = Object.values(fixtures);
    return {
      jsHeapMB: m ? Math.round(m.usedJSHeapSize / 1e5) / 10 : null,
      tileCacheMB: Math.round(srcs.reduce((s, f) => s + f.spec.cachedBytes(), 0) / 1e5) / 10,
      peaksMB: Math.round(srcs.reduce((s, f) => s + f.peaks.bytes(), 0) / 1e5) / 10,
      featuresMB: Math.round(srcs.reduce((s, f) => s + f.features.bytes(), 0) / 1e5) / 10,
      gpuArrayMB: Math.round([...allRenderers].reduce((s, r) => s + r.stats.gpuBytes, 0) / 1e5) / 10,
      mseMB: Math.round(Object.values(media).reduce((s, x) => s + x.stats.bytes, 0) / 1e5) / 10,
      wordDom: Object.values(views).reduce((s, v) => s + v.wordDom(), 0),
      domNodes: document.getElementsByTagName("*").length,
      tilesFetched: (fixtures.call?.spec as PyramidSource | undefined)?.fetched ?? 0,
      tileFetchP95Ms: Math.round(pct((fixtures.call?.spec as PyramidSource | undefined)?.fetchMs ?? [], 95)),
    };
  },
  async stft(backend: "wasm" | "js", wantFloat: boolean) {
    const pcm = new Float32Array(await bytes(`${FX}/clip.f32`));
    const r = await stftWorker({ pcm, backend, wantFloat });
    const enc = (b: ArrayBufferLike) => {
      const u = new Uint8Array(b);
      let s = "";
      for (let i = 0; i < u.length; i += 0x8000) s += String.fromCharCode(...u.subarray(i, i + 0x8000));
      return btoa(s);
    };
    return { frames: r.frames, ms: r.ms, u8: enc(r.u8.buffer), db: r.db ? enc(r.db.buffer) : null };
  },
  /** FFT time for `seconds` of 16 kHz audio (the clip repeated), per backend. */
  async stftBench(seconds: number) {
    const clip = new Float32Array(await bytes(`${FX}/clip.f32`));
    const out: Record<string, number> = {};
    for (const backend of ["wasm", "js", "wasm", "js"] as const) {
      const pcm = new Float32Array(seconds * 16000);
      for (let i = 0; i < pcm.length; i += clip.length) pcm.set(clip.subarray(0, Math.min(clip.length, pcm.length - i)), i);
      const r = await stftWorker({ pcm, backend });
      out[backend] = r.ms; // second run wins (warm JIT, WASM compiled)
    }
    return { seconds, wasmMs: Math.round(out.wasm!), jsMs: Math.round(out.js!), wasmMsPerMin: Math.round((out.wasm! / seconds) * 60 * 10) / 10, jsMsPerMin: Math.round((out.js! / seconds) * 60 * 10) / 10 };
  },
  samplePixels() {
    return Object.fromEntries(Object.entries(views).map(([k, v]) => [k, v.samplePixels()]));
  },
  setColormapAll(c: Colormap) {
    for (const v of Object.values(views)) v.set({ colormap: c });
  },
  setTheme(mode: "light" | "dark") {
    for (const w of [window, ...popouts]) w.document.documentElement.dataset.theme = mode;
    for (const v of Object.values(views)) v.readTheme();
  },
};
(window as unknown as { __s5: typeof s5 }).__s5 = s5;
export type S5 = typeof s5;
