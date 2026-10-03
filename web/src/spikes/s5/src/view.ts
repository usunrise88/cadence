// AudioView (R51) as a throwaway imperative component: one time axis (visible range, zoom, playhead, loop) and tracks
// stacked against it — ruler, waveform (Cadence canvas over peaks), spectrogram per channel (shared WebGL2 renderer),
// model input (float16 analysis), word tracks (DOM). Everything is created in the container's ownerDocument and timed
// by that window's frame loop, so the same code runs docked, floating and in a popout.
import { Renderer, sharedRenderer, type Colormap, type FeatQuad, type TileQuad } from "./renderer";
import { schedulerFor, type Frameable } from "./scheduler";
import { TILE, type Features, type Peaks, type SpecSource, type WordTrackData } from "./sources";
import { WordTrack } from "./words";

export type ViewOptions = {
  id: string;
  title: string;
  duration: number;
  spec: SpecSource;
  peaks: Peaks;
  words: WordTrackData[];
  features?: Features;
  media?: HTMLMediaElement;
  mode?: "shared" | "per-track";
  compact?: boolean;
  waveform?: boolean;
  spectrogram?: boolean;
};

const FEATURE_MAX_SPAN_S = 60;
const HOP0 = 0.01;

function fmt(t: number, span = 10): string {
  const m = Math.floor(t / 60);
  const s = t - m * 60;
  const dec = span < 5 ? 2 : span < 60 ? 1 : 0;
  return `${m}:${s.toFixed(dec).padStart(dec ? 3 + dec : 2, "0")}`;
}

function half(h: number): number {
  const s = h & 0x8000 ? -1 : 1;
  const e = (h >> 10) & 0x1f;
  const f = h & 0x3ff;
  if (e === 0) return s * 2 ** -14 * (f / 1024);
  if (e === 31) return f ? NaN : s * Infinity;
  return s * 2 ** (e - 15) * (1 + f / 1024);
}

export class AudioView implements Frameable {
  readonly doc: Document;
  readonly win: Window;
  readonly root: HTMLDivElement;
  start = 0;
  span: number;
  playhead = 0;
  loop: [number, number] | null = null;
  gainDb = 0;
  rangeDb = 80;
  colormap: Colormap = "magma";
  axis: "mel" | "hz" = "mel";
  follow = true;
  drawCount = 0;
  lastDrawMs = 0;
  private dirty = true;
  private width = 0;
  private dpr = 1;
  private ruler: HTMLCanvasElement;
  private wave?: HTMLCanvasElement;
  private specs: HTMLCanvasElement[] = [];
  private feat?: HTMLCanvasElement;
  private overlay: HTMLDivElement;
  private playheadEl: HTMLDivElement;
  private loopEl: HTMLDivElement;
  private summary: HTMLDivElement;
  private live: HTMLDivElement;
  private tracks: WordTrack[] = [];
  private renderers: Renderer[] = [];
  private cleanup: (() => void)[] = [];
  private colors = { fg: "", grid: "", wave: "", clip: "" };
  private anim?: { from: [number, number]; to: [number, number]; t0: number; ms: number };
  private summaryAt = 0;
  private featRange?: [number, number];
  wordIndex = -1;

  constructor(
    readonly container: HTMLElement,
    readonly o: ViewOptions,
  ) {
    this.doc = container.ownerDocument;
    this.win = this.doc.defaultView!;
    this.span = o.duration;
    const el = <K extends keyof HTMLElementTagNameMap>(tag: K, cls: string, parent: HTMLElement = this.root) => {
      const e = this.doc.createElement(tag);
      e.className = cls;
      parent.append(e);
      return e;
    };
    this.root = this.doc.createElement("div");
    this.root.className = "s5-view" + (o.compact ? " s5-compact" : "");
    this.root.tabIndex = 0;
    this.root.dataset.view = o.id;
    this.root.setAttribute("role", "group");
    this.root.setAttribute("aria-roledescription", "audio view");
    this.root.setAttribute("aria-label", o.title);
    container.append(this.root);
    this.ruler = el("canvas", "s5-ruler");
    if (o.waveform !== false) this.wave = el("canvas", "s5-wave");
    for (let ch = 0; ch < (o.spectrogram === false ? 0 : o.spec.channels); ch++) {
      const c = el("canvas", "s5-spec");
      c.dataset.ch = String(ch);
      this.specs.push(c);
    }
    if (o.features) this.feat = el("canvas", "s5-feat");
    for (const t of o.words) {
      const tr = new WordTrack(this.doc, t, (track, i) => this.hoverWord(track, i));
      this.root.append(tr.el);
      this.tracks.push(tr);
    }
    this.overlay = el("div", "s5-overlay");
    this.loopEl = el("div", "s5-loop", this.overlay);
    this.playheadEl = el("div", "s5-playhead", this.overlay);
    this.summary = el("div", "s5-sr");
    this.summary.id = `s5-summary-${o.id}`;
    this.root.setAttribute("aria-describedby", this.summary.id);
    this.live = el("div", "s5-sr");
    this.live.setAttribute("aria-live", "polite");

    if ((o.mode ?? "shared") === "shared") this.renderers = [sharedRenderer(this.win)];
    else this.renderers = [...this.specs, ...(this.feat ? [this.feat] : [])].map(() => new Renderer(this.win, 32));
    for (const r of new Set(this.renderers)) this.cleanup.push(r.whenRestored(() => this.invalidate()));
    this.cleanup.push(o.spec.onLoad(() => this.invalidate()));
    if (o.features) this.cleanup.push(o.features.onLoad(() => this.invalidate()));
    const ro = new (this.win as unknown as typeof globalThis).ResizeObserver(() => this.measure());
    ro.observe(this.root);
    this.cleanup.push(() => ro.disconnect());
    this.root.addEventListener("keydown", (e) => this.key(e));
    this.root.addEventListener("wheel", (e) => this.wheel(e), { passive: false });
    this.readTheme();
    this.measure();
    this.cleanup.push(schedulerFor(this.win).add(this));
  }

  destroy() {
    for (const c of this.cleanup) c();
    this.root.remove();
  }

  get renderer() {
    return this.renderers[0]!;
  }
  get reducedMotion() {
    return this.win.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }

  readTheme() {
    const cs = this.win.getComputedStyle(this.root);
    this.colors = {
      fg: cs.getPropertyValue("--s5-fg").trim(),
      grid: cs.getPropertyValue("--s5-grid").trim(),
      wave: cs.getPropertyValue("--s5-wave").trim(),
      clip: cs.getPropertyValue("--s5-clip").trim(),
    };
    this.invalidate();
  }

  private measure() {
    const w = Math.floor(this.root.clientWidth);
    const dpr = this.win.devicePixelRatio || 1;
    if (w === this.width && dpr === this.dpr) return;
    this.width = w;
    this.dpr = dpr;
    for (const c of [this.ruler, this.wave, ...this.specs, this.feat]) {
      if (!c) continue;
      c.width = Math.max(1, Math.round(w * dpr));
      c.height = Math.max(1, Math.round(c.clientHeight * dpr));
    }
    this.invalidate();
  }

  invalidate() {
    this.dirty = true;
  }

  setRange(start: number, span: number) {
    span = Math.min(this.o.duration, Math.max(0.5, span));
    start = Math.min(this.o.duration - span, Math.max(0, start));
    if (start === this.start && span === this.span) return;
    this.start = start;
    this.span = span;
    this.dirty = true;
  }
  animateTo(start: number, span: number, ms = 160) {
    if (this.reducedMotion || ms === 0) return this.setRange(start, span);
    this.anim = { from: [this.start, this.span], to: [start, span], t0: performance.now(), ms };
  }
  zoomBy(factor: number, at = this.playhead) {
    const span = Math.min(this.o.duration, Math.max(0.5, this.span * factor));
    const rel = this.span > 0 ? (at - this.start) / this.span : 0.5;
    this.animateTo(at - rel * span, span);
  }
  seek(t: number) {
    this.playhead = Math.min(this.o.duration, Math.max(0, t));
    if (this.o.media) this.o.media.currentTime = this.playhead;
    if (this.playhead < this.start || this.playhead > this.start + this.span) this.animateTo(this.playhead - this.span * 0.3, this.span);
    this.dirty = true;
  }
  set(opts: Partial<{ gainDb: number; rangeDb: number; colormap: Colormap; axis: "mel" | "hz" }>) {
    Object.assign(this, opts);
    this.dirty = true;
  }

  private key(e: KeyboardEvent) {
    const k = e.key;
    let handled = true;
    if (k === " ") this.togglePlay();
    else if (k === "ArrowLeft") this.seek(this.playhead - this.span * 0.1);
    else if (k === "ArrowRight") this.seek(this.playhead + this.span * 0.1);
    else if (k === "+" || k === "=") this.zoomBy(0.5);
    else if (k === "-") this.zoomBy(2);
    else if (k === "[") this.setLoop(this.playhead, this.loop?.[1] ?? this.o.duration);
    else if (k === "]") this.setLoop(this.loop?.[0] ?? 0, this.playhead);
    else if (k === "," || k === ".") this.stepWord(k === "." ? 1 : -1);
    else handled = false;
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
      this.updateSummary(true);
    }
  }
  private wheel(e: WheelEvent) {
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) this.zoomBy(e.deltaY > 0 ? 1.25 : 0.8, this.start + ((e.offsetX || 0) / this.width) * this.span);
    else this.setRange(this.start + (e.deltaX || e.deltaY) * (this.span / this.width), this.span);
  }
  togglePlay() {
    const m = this.o.media;
    if (!m) return;
    if (m.paused) {
      if (Math.abs(m.currentTime - this.playhead) > 0.05) m.currentTime = this.playhead;
      void m.play();
    } else m.pause();
  }
  setLoop(a: number, b: number) {
    this.loop = a < b ? [a, b] : null;
    this.announce(this.loop ? `Loop ${fmt(a, 1)} to ${fmt(b, 1)}` : "Loop cleared");
    this.dirty = true;
  }
  stepWord(dir: 1 | -1) {
    const tr = this.tracks[0];
    if (!tr) return;
    const cur = tr.indexAt(this.playhead + (dir > 0 ? 0.001 : -0.001));
    let i = dir > 0 ? cur + (tr.word(cur) && tr.word(cur)!.s <= this.playhead + 0.001 ? 1 : 0) : cur - 1;
    i = Math.max(0, Math.min(tr.data.words.length - 1, i));
    const w = tr.word(i)!;
    this.wordIndex = i;
    tr.hovered = i;
    this.seek(w.s);
    this.announce(`${w.w}, ${fmt(w.s, 1)}`);
  }
  private hoverWord(track: WordTrack, i: number) {
    for (const t of this.tracks) t.hovered = t === track ? i : -1;
    this.root.dispatchEvent(new CustomEvent("s5-word-hover", { detail: { track: track.data.id, index: i }, bubbles: true }));
    this.dirty = true;
  }
  private announce(text: string) {
    this.live.textContent = text;
  }
  summaryText() {
    const ch = this.o.spec.channels === 2 ? "stereo, caller left and bot right" : "mono";
    const loop = this.loop ? ` Loop ${fmt(this.loop[0], 1)}–${fmt(this.loop[1], 1)}.` : "";
    const tr = this.tracks[0];
    const w = tr?.word(tr.indexAt(this.playhead));
    return (
      `${this.o.title}: ${fmt(this.o.duration, 100)}, ${ch}. Showing ${fmt(this.start, this.span)} to ${fmt(this.start + this.span, this.span)}. ` +
      `Playhead ${fmt(this.playhead, this.span)}.${loop} Tracks: waveform, spectrogram${this.o.features ? ", model input" : ""}, ` +
      `${this.tracks.map((t) => `${t.data.label} (${t.data.words.length} words)`).join(", ")}.` +
      (w ? ` Word at playhead: ${w.w}.` : "")
    );
  }
  private updateSummary(force = false) {
    const now = performance.now();
    if (!force && now - this.summaryAt < 500) return;
    this.summaryAt = now;
    const s = this.summaryText();
    if (this.summary.textContent !== s) this.summary.textContent = s;
  }

  frame(now: number) {
    if (this.anim) {
      const a = this.anim;
      const k = Math.min(1, (now - a.t0) / a.ms);
      const e = 1 - (1 - k) * (1 - k);
      // zoom in log space so the motion looks even
      const span = Math.exp(Math.log(a.from[1]) + (Math.log(a.to[1]) - Math.log(a.from[1])) * e);
      this.setRange(a.from[0] + (a.to[0] - a.from[0]) * e, span);
      if (k >= 1) this.anim = undefined;
    }
    const m = this.o.media;
    if (m && !m.paused) {
      this.playhead = m.currentTime;
      if (this.loop && this.playhead >= this.loop[1]) m.currentTime = this.loop[0];
      if (this.follow) {
        if (this.reducedMotion) {
          if (this.playhead > this.start + this.span * 0.95 || this.playhead < this.start) this.setRange(this.playhead - this.span * 0.05, this.span);
        } else this.setRange(this.playhead - this.span * 0.3, this.span);
      }
      this.dirty = true;
    }
    if (!this.dirty || this.width === 0 || !this.root.isConnected) return;
    const t0 = performance.now();
    this.dirty = !this.render();
    this.lastDrawMs = performance.now() - t0;
    this.drawCount++;
    this.updateSummary();
  }

  private x(t: number, w: number) {
    return ((t - this.start) / this.span) * w;
  }

  /** Returns false when something could not draw yet (lost context), so the next frame retries. */
  render(): boolean {
    this.drawRuler();
    if (this.wave) this.drawWave();
    let ok = true;
    this.specs.forEach((c, ch) => {
      const r = this.renderers[(this.o.mode ?? "shared") === "shared" ? 0 : ch]!;
      ok = r.drawInto(c, (rr, w, h) => rr.tileQuads(this.tileQuads(ch, w, h), this.uniforms(ch))) && ok;
    });
    if (this.feat) {
      const r = this.renderers[(this.o.mode ?? "shared") === "shared" ? 0 : this.specs.length]!;
      ok = r.drawInto(this.feat, (rr, w, h) => rr.featQuads(this.featQuads(w, h))) && ok;
    }
    const w = this.width;
    for (const t of this.tracks) t.render(this.start, this.span, w);
    this.playheadEl.style.transform = `translateX(${this.x(this.playhead, w).toFixed(1)}px)`;
    if (this.loop) {
      this.loopEl.style.display = "";
      this.loopEl.style.transform = `translateX(${this.x(this.loop[0], w).toFixed(1)}px)`;
      this.loopEl.style.width = `${Math.max(1, ((this.loop[1] - this.loop[0]) / this.span) * w).toFixed(1)}px`;
    } else this.loopEl.style.display = "none";
    return ok;
  }

  uniforms(ch: number) {
    return { gainDb: this.gainDb, rangeDb: this.rangeDb, peakDb: this.o.spec.peakDb[ch] ?? 0, colormap: this.colormap, axis: this.axis, fmaxHz: this.o.spec.fmaxHz };
  }

  level(wPx: number): number {
    const src = this.o.spec;
    const fpp = this.span / HOP0 / wPx;
    return Math.max(0, Math.min(src.levels.length - 1, Math.floor(Math.log2(Math.max(1, fpp)))));
  }

  tileQuads(ch: number, w: number, h: number): TileQuad[] {
    const src = this.o.spec;
    const target = this.level(w);
    const top = src.levels.length - 1;
    const quads: TileQuad[] = [];
    const push = (l: number, fetch: boolean) => {
      const hop = src.levels[l]!.hopS;
      const tileS = TILE * hop;
      const a = Math.max(0, Math.floor((this.start + HOP0 / 2) / tileS));
      const b = Math.min(src.levels[l]!.tiles - 1, Math.floor((this.start + this.span + HOP0 / 2) / tileS));
      for (let i = a; i <= b; i++) {
        const bytes = fetch ? src.tile(ch, l, i) : peek(src, ch, l, i);
        if (!bytes) continue;
        const t0 = i * tileS - HOP0 / 2;
        quads.push({ key: `${src.id}/${ch}/${l}/${i}`, bytes, bins: src.bins, binHz: src.binHz, srcFmaxHz: src.fmaxHz, x0: this.x(t0, w), x1: this.x(t0 + tileS, w), y0: 0, y1: h });
      }
      if (fetch) {
        // prefetch one tile either side
        if (a > 0) src.tile(ch, l, a - 1);
        if (b < src.levels[l]!.tiles - 1) src.tile(ch, l, b + 1);
      }
    };
    push(top, false);
    for (let l = Math.min(top - 1, target + 2); l > target; l--) push(l, false);
    if (target < top) push(target, true);
    return quads;
  }

  featQuads(w: number, h: number): FeatQuad[] {
    const f = this.o.features!;
    if (this.span > FEATURE_MAX_SPAN_S) return [];
    const a = Math.max(0, Math.floor(this.start / HOP0 / f.chunk));
    const b = Math.min(Math.ceil(f.frames / f.chunk) - 1, Math.floor((this.start + this.span) / HOP0 / f.chunk));
    const out: FeatQuad[] = [];
    for (let i = a; i <= b; i++) {
      const data = f.get(i);
      if (!data) continue;
      const width = Math.min(f.chunk, f.frames - i * f.chunk);
      if (!this.featRange) {
        const vals: number[] = [];
        for (let k = 0; k < data.length; k += 97) vals.push(half(data[k]!));
        vals.sort((p, q) => p - q);
        this.featRange = [vals[Math.floor(vals.length * 0.05)]!, vals[Math.floor(vals.length * 0.999)]!];
      }
      const t0 = i * f.chunk * HOP0 - HOP0 / 2;
      out.push({
        key: `feat/${this.o.id}/${i}`,
        data,
        width,
        mels: f.mels,
        x0: this.x(t0, w),
        x1: this.x(t0 + width * HOP0, w),
        y0: 0,
        y1: h,
        lo: this.featRange[0],
        hi: this.featRange[1],
        dimAbove: f.dimAboveMel ?? 0,
        colormap: this.colormap,
      });
    }
    return out;
  }

  private drawRuler() {
    const c = this.ruler;
    const ctx = c.getContext("2d")!;
    const w = c.width;
    const h = c.height;
    ctx.clearRect(0, 0, w, h);
    const pxPerS = w / this.span;
    const steps = [0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600];
    const step = steps.find((s) => s * pxPerS >= 80 * this.dpr) ?? 600;
    ctx.fillStyle = this.colors.fg;
    ctx.strokeStyle = this.colors.grid;
    ctx.font = `${11 * this.dpr}px system-ui, sans-serif`;
    ctx.textBaseline = "top";
    ctx.beginPath();
    for (let t = Math.ceil(this.start / step) * step; t <= this.start + this.span; t += step) {
      const x = Math.round(this.x(t, w)) + 0.5;
      ctx.moveTo(x, h * 0.55);
      ctx.lineTo(x, h);
      ctx.fillText(fmt(t, step * 10), x + 3 * this.dpr, 2 * this.dpr);
    }
    ctx.stroke();
  }

  private drawWave() {
    const c = this.wave!;
    const ctx = c.getContext("2d")!;
    const w = c.width;
    const h = c.height;
    ctx.clearRect(0, 0, w, h);
    const p = this.o.peaks;
    const chs = p.channels;
    const lane = h / chs;
    const fpp = this.span / p.hopS / w;
    const lvl = Math.max(0, Math.min(p.levels.length - 1, Math.floor(Math.log2(Math.max(1, fpp)))));
    const data = p.levels[lvl]!;
    const hop = p.hopS * 2 ** lvl;
    const n = data.length / (2 * chs);
    ctx.fillStyle = this.colors.wave;
    for (let ch = 0; ch < chs; ch++) {
      const mid = lane * ch + lane / 2;
      const scale = lane / 2 / 127;
      ctx.beginPath();
      for (let x = 0; x < w; x++) {
        const t0 = this.start + (x / w) * this.span;
        const t1 = this.start + ((x + 1) / w) * this.span;
        let a = Math.floor(t0 / hop);
        let b = Math.max(a + 1, Math.floor(t1 / hop));
        a = Math.max(0, Math.min(n - 1, a));
        b = Math.max(a + 1, Math.min(n, b));
        let lo = 127;
        let hi = -127;
        for (let i = a; i < b; i++) {
          const o = (i * chs + ch) * 2;
          if (data[o]! < lo) lo = data[o]!;
          if (data[o + 1]! > hi) hi = data[o + 1]!;
        }
        ctx.rect(x, mid - hi * scale, 1, Math.max(1, (hi - lo) * scale));
      }
      ctx.fill();
    }
  }

  /** For the measurements: does every canvas the renderer feeds hold a picture (more than a handful of colours)? */
  probe(): { canvases: number; blank: number } {
    let blank = 0;
    const list = [...this.specs, ...(this.feat && this.span <= FEATURE_MAX_SPAN_S ? [this.feat] : [])];
    for (const c of list) {
      const ctx = c.getContext("2d")!;
      const d = ctx.getImageData(0, 0, c.width, c.height).data;
      const seen = new Set<number>();
      for (let i = 0; i < d.length && seen.size < 16; i += 4 * 97) seen.add((d[i]! << 16) | (d[i + 1]! << 8) | d[i + 2]!);
      if (seen.size < 4) blank++;
    }
    return { canvases: list.length, blank };
  }
  /** Centre pixel of each spectrogram canvas (to check a redraw after a context loss, and theme independence). */
  samplePixels(): number[][] {
    return this.specs.map((c) => {
      const d = c.getContext("2d")!.getImageData(Math.floor(c.width / 2), Math.floor(c.height / 2), 1, 1).data;
      return [d[0]!, d[1]!, d[2]!];
    });
  }
  wordDom() {
    return this.tracks.reduce((s, t) => s + t.domCount(), 0);
  }
  wordModes() {
    return this.tracks.map((t) => ({ mode: t.mode, visible: t.visibleCount }));
  }
}

function peek(src: SpecSource, ch: number, l: number, i: number): Uint8Array | undefined {
  const p = src as SpecSource & { peek?: (ch: number, l: number, i: number) => Uint8Array | undefined };
  return p.peek ? p.peek(ch, l, i) : src.tile(ch, l, i);
}
