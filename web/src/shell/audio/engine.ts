// The audio view's imperative core (R51, R52; product form of spike S5's view): tracks stacked against one AudioAxis
// — ruler, waveform per channel (Cadence canvas over peaks, clipping marks), spectrogram per channel (the window's
// shared WebGL2 renderer), word tracks (DOM), a minimap — plus the playhead, the loop region and a span selection.
// Everything is created in the container's ownerDocument and drawn in that window's frame loop, so the same code runs
// docked, floating and in a popout; the React wrapper rebuilds the engine when the panel moves to another window.
import { resolveColor } from "@/shell/charts/tokens";
import { formatTime, pyramidLevel, rulerStep, timeToX, xToTime, type AudioAxis } from "./axis";
import { blurAudio, setFocusedAudio, type AudioController } from "./controller";
import type { PeakPyramid } from "./peaks";
import { sharedRenderer, type Colormap, type Renderer, type TileQuad } from "./renderer";
import { schedulerFor, type Frameable, type WindowScheduler } from "./scheduler";
import { TILE, tileRange, tileSpan, type SpecSource } from "./tiles";
import { stepWord, WordTrack, wordIndexAt, type WordTrackData } from "./words";

export type SpecSettings = { gainDb: number; rangeDb: number; colormap: Colormap; axis: "mel" | "hz"; fmaxHz: number };

export type EngineOptions = {
  axis: AudioAxis;
  title: string;
  compact?: boolean;
  /** Names of the channels in the summary ("caller", "bot"); default "channel n". */
  channelLabels?: string[];
  tileSlots: number;
  maxVisibleWords: number;
  settings: SpecSettings;
  /** A span selected with the pointer (also set as the loop). */
  onSpan?: (start: number, end: number) => void;
  /** The media element failed (an expired link): the owner fetches a new source. */
  onMediaError?: () => void;
  /** Playback started or stopped. */
  onPlayState?: (playing: boolean) => void;
};

const HOP0 = 0.01;
const SUMMARY_MS = 500;

type Colors = { text: string; muted: string; grid: string; wave: string; clip: string; viewport: string };

export class AudioEngine implements Frameable, AudioController {
  readonly doc: Document;
  readonly win: Window;
  readonly root: HTMLDivElement;
  readonly renderer: Renderer;
  readonly title: string;
  private o: EngineOptions;
  private axis: AudioAxis;
  private sched: WindowScheduler;
  private ruler: HTMLCanvasElement;
  private waveBox: HTMLDivElement;
  private wave: HTMLCanvasElement;
  private specBox: HTMLDivElement;
  private specs: HTMLCanvasElement[] = [];
  private specNote: HTMLDivElement;
  private wordsBox: HTMLDivElement;
  private tracks: WordTrack[] = [];
  private minimap: HTMLCanvasElement;
  private overlay: HTMLDivElement;
  private playheadEl: HTMLDivElement;
  private loopEl: HTMLDivElement;
  private selEl: HTMLDivElement;
  private summary: HTMLDivElement;
  private live: HTMLDivElement;
  private peaks: PeakPyramid | null = null;
  private spec: SpecSource | null = null;
  private media: HTMLAudioElement | null = null;
  private settings: SpecSettings;
  private colors: Colors = { text: "", muted: "", grid: "", wave: "", clip: "", viewport: "" };
  private cleanup: (() => void)[] = [];
  private specCleanup: (() => void) | null = null;
  private dirty = true;
  private width = 0;
  private dpr = 1;
  private summaryAt = 0;
  private drag: { x0: number; t0: number; moved: boolean; el: HTMLElement; pointer: number } | null = null;
  private miniDrag = false;
  private destroyed = false;
  /** Draws so far and the last draw's time (ms): measurements. */
  draws = 0;
  lastDrawMs = 0;

  constructor(container: HTMLElement, o: EngineOptions) {
    this.o = o;
    this.axis = o.axis;
    this.title = o.title;
    this.settings = o.settings;
    this.doc = container.ownerDocument;
    this.win = this.doc.defaultView!;
    this.renderer = sharedRenderer(this.win, o.tileSlots);
    this.sched = schedulerFor(this.win);
    const el = <K extends keyof HTMLElementTagNameMap>(tag: K, cls: string, parent: HTMLElement = this.root) => {
      const e = this.doc.createElement(tag);
      e.className = cls;
      parent.append(e);
      return e;
    };
    this.root = this.doc.createElement("div");
    this.root.className = "cadence-audio-view" + (o.compact ? " cadence-audio-compact" : "");
    this.root.tabIndex = 0;
    this.root.setAttribute("role", "group");
    this.root.setAttribute("aria-roledescription", "audio view");
    this.root.setAttribute("aria-label", o.title);
    container.append(this.root);
    this.ruler = el("canvas", "cadence-audio-ruler");
    this.waveBox = el("div", "cadence-audio-lane cadence-audio-wave-box");
    this.wave = el("canvas", "cadence-audio-wave", this.waveBox);
    this.specBox = el("div", "cadence-audio-lane cadence-audio-spec-box");
    this.specNote = el("div", "cadence-audio-note", this.specBox);
    this.wordsBox = el("div", "cadence-audio-words-box");
    this.minimap = el("canvas", "cadence-audio-minimap");
    this.minimap.setAttribute("aria-hidden", "true");
    this.overlay = el("div", "cadence-audio-overlay");
    this.loopEl = el("div", "cadence-audio-loop", this.overlay);
    this.selEl = el("div", "cadence-audio-selection", this.overlay);
    this.playheadEl = el("div", "cadence-audio-playhead", this.overlay);
    this.summary = el("div", "cadence-audio-sr");
    this.summary.id = `cadence-audio-summary-${Math.random().toString(36).slice(2)}`;
    this.root.setAttribute("aria-describedby", this.summary.id);
    this.live = el("div", "cadence-audio-sr");
    this.live.setAttribute("aria-live", "polite");

    this.cleanup.push(this.axis.subscribe(() => this.invalidate()));
    this.cleanup.push(this.renderer.whenRestored(() => this.invalidate()));
    const RO = (this.win as unknown as typeof globalThis).ResizeObserver;
    if (RO) {
      const ro = new RO(() => this.measure());
      ro.observe(this.root);
      this.cleanup.push(() => ro.disconnect());
    }
    const mo = new (this.win as unknown as typeof globalThis).MutationObserver(() => this.readTheme());
    mo.observe(this.doc.documentElement, { attributes: true, attributeFilter: ["class", "data-theme", "style"] });
    this.cleanup.push(() => mo.disconnect());
    const on = <K extends keyof HTMLElementEventMap>(target: HTMLElement, type: K, fn: (e: HTMLElementEventMap[K]) => void, opts?: AddEventListenerOptions) => {
      target.addEventListener(type, fn, opts);
      this.cleanup.push(() => target.removeEventListener(type, fn, opts));
    };
    on(this.root, "focusin", () => setFocusedAudio(this));
    on(this.root, "focusout", (e) => {
      if (!this.root.contains(e.relatedTarget as Node | null)) blurAudio(this);
    });
    on(this.root, "wheel", (e) => this.wheel(e), { passive: false });
    on(this.root, "contextmenu", (e) => e.preventDefault()); // no "Save audio as…" on the view
    on(this.root, "keydown", (e) => {
      // Shift+= types "+": zoom in like "=" (view.audioZoomIn), which a chord cannot name.
      if (e.key === "+" && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        this.zoomBy(0.5);
      }
    });
    for (const lane of [this.waveBox, this.specBox]) {
      on(lane, "pointerdown", (e) => this.pointerDown(e, lane));
      on(lane, "pointermove", (e) => this.pointerMove(e));
      on(lane, "pointerup", (e) => this.pointerUp(e));
      on(lane, "pointercancel", () => this.cancelDrag());
    }
    on(this.minimap, "pointerdown", (e) => {
      this.miniDrag = true;
      this.minimap.setPointerCapture?.(e.pointerId);
      this.miniMove(e);
    });
    on(this.minimap, "pointermove", (e) => this.miniDrag && this.miniMove(e));
    on(this.minimap, "pointerup", () => (this.miniDrag = false));
    this.readTheme();
    this.measure();
    this.setSpec(null, "");
    this.cleanup.push(this.sched.add(this));
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    blurAudio(this);
    for (const c of this.cleanup) c();
    this.specCleanup?.();
    if (this.spec) this.renderer.release(`${this.spec.id}/`);
    this.setMedia(null);
    this.root.remove();
  }

  // ---------------------------------------------------------------- data

  setPeaks(p: PeakPyramid | null): void {
    this.peaks = p;
    this.invalidate();
  }

  /** The spectrogram source, or null with a note shown in its place ("" hides the lane). */
  setSpec(src: SpecSource | null, note: string): void {
    if (this.spec && this.spec !== src) this.renderer.release(`${this.spec.id}/`);
    this.specCleanup?.();
    this.specCleanup = null;
    this.spec = src;
    for (const c of this.specs) c.remove();
    this.specs = [];
    if (src) {
      this.specCleanup = src.onLoad(() => this.invalidate());
      for (let ch = 0; ch < src.channels; ch++) {
        const c = this.doc.createElement("canvas");
        c.className = "cadence-audio-spec";
        c.dataset.ch = String(ch);
        this.specBox.insertBefore(c, this.specNote);
        this.specs.push(c);
      }
    }
    const message = !src && note ? note : src && this.renderer.unsupported ? "This browser has no WebGL2: the spectrogram cannot be drawn." : "";
    this.specNote.textContent = message;
    this.specNote.hidden = !message;
    this.specBox.hidden = !src && !note;
    this.measure(true);
  }

  setWords(tracks: WordTrackData[]): void {
    for (const t of this.tracks) t.el.remove();
    this.tracks = tracks.map((d) => {
      const t = new WordTrack(this.doc, d, this.o.maxVisibleWords, (track, i) => {
        for (const x of this.tracks) x.hovered = x === track ? i : -1;
        this.invalidate();
      });
      this.wordsBox.append(t.el);
      return t;
    });
    this.wordsBox.hidden = tracks.length === 0;
    this.invalidate();
  }

  setSettings(s: Partial<SpecSettings>): void {
    this.settings = { ...this.settings, ...s };
    this.invalidate();
  }

  /** The source the view plays (a signed audio link), or null. The element lives in the view's own document. */
  setMedia(src: string | null): void {
    if (this.media) {
      this.media.pause();
      this.media.removeAttribute("src");
      this.media.load();
      this.media = null;
    }
    if (!src) return;
    const m = this.doc.createElement("audio");
    m.preload = "metadata";
    m.setAttribute("controlsList", "nodownload noplaybackrate");
    m.addEventListener("error", () => this.o.onMediaError?.());
    m.addEventListener("play", () => {
      this.sched.kick();
      this.o.onPlayState?.(true);
    });
    m.addEventListener("pause", () => {
      this.invalidate();
      this.o.onPlayState?.(false);
    });
    m.src = src;
    m.playbackRate = this.rate;
    this.media = m;
  }

  /** Playback speed (the element keeps pitch). */
  setRate(rate: number): void {
    this.rate = rate;
    if (this.media) this.media.playbackRate = rate;
  }
  private rate = 1;

  get playing(): boolean {
    return !!this.media && !this.media.paused;
  }

  // ---------------------------------------------------------------- controller (view.audio.* commands)

  get reducedMotion(): boolean {
    return this.win.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  }

  togglePlay(): void {
    const m = this.media;
    if (!m) {
      this.announce("No audio to play yet");
      return;
    }
    if (m.paused) {
      const s = this.axis.get();
      let t = s.playhead;
      if (s.loop && (t < s.loop[0] || t >= s.loop[1])) t = s.loop[0];
      if (Math.abs(m.currentTime - t) > 0.05) m.currentTime = t;
      void m.play().catch(() => this.announce("Playback was blocked; press Space again"));
    } else m.pause();
    this.sched.kick();
  }

  seekBy(fraction: number): void {
    const s = this.axis.get();
    this.seek(s.playhead + s.span * fraction);
  }

  seek(t: number): void {
    this.axis.seek(t, this.reducedMotion);
    if (this.media) this.media.currentTime = this.axis.get().playhead;
    this.sched.kick();
  }

  zoomBy(factor: number): void {
    this.axis.zoomBy(factor, this.axis.get().playhead, this.reducedMotion);
    this.sched.kick();
  }

  loopIn(): void {
    const s = this.axis.get();
    this.axis.setLoop(s.playhead, s.loop?.[1] ?? s.duration);
    this.announceLoop();
  }

  loopOut(): void {
    const s = this.axis.get();
    this.axis.setLoop(s.loop?.[0] ?? 0, s.playhead);
    this.announceLoop();
  }

  stepWord(dir: 1 | -1): void {
    const tr = this.tracks[0];
    if (!tr || tr.data.words.length === 0) {
      this.announce("No words in this view");
      return;
    }
    const i = stepWord(tr.data.words, this.axis.get().playhead, dir);
    const w = tr.word(i)!;
    for (const t of this.tracks) t.hovered = t === tr ? i : -1;
    this.seek(w.start);
    this.announce(`${w.word}, ${formatTime(w.start, 1)}`);
  }

  private announceLoop(): void {
    const l = this.axis.get().loop;
    this.announce(l ? `Loop ${formatTime(l[0], 1)} to ${formatTime(l[1], 1)}` : "Loop cleared");
    if (l) this.o.onSpan?.(l[0], l[1]);
  }

  private announce(text: string): void {
    this.live.textContent = text;
  }

  // ---------------------------------------------------------------- pointer

  private timeAt(e: PointerEvent | WheelEvent, el: HTMLElement): number {
    const r = el.getBoundingClientRect();
    const s = this.axis.get();
    return xToTime(e.clientX - r.left, s.start, s.span, r.width);
  }

  private pointerDown(e: PointerEvent, el: HTMLElement): void {
    if (e.button !== 0) return;
    this.root.focus({ preventScroll: true });
    el.setPointerCapture?.(e.pointerId);
    this.drag = { x0: e.clientX, t0: this.timeAt(e, el), moved: false, el, pointer: e.pointerId };
  }

  private pointerMove(e: PointerEvent): void {
    const d = this.drag;
    if (!d || e.pointerId !== d.pointer) return;
    if (Math.abs(e.clientX - d.x0) > 3) d.moved = true;
    if (d.moved) this.showSelection(d.t0, this.timeAt(e, d.el));
  }

  private pointerUp(e: PointerEvent): void {
    const d = this.drag;
    this.drag = null;
    if (!d || e.pointerId !== d.pointer) return;
    const t = this.timeAt(e, d.el);
    this.selEl.style.display = "none";
    if (!d.moved) {
      this.seek(t);
      return;
    }
    const a = Math.max(0, Math.min(d.t0, t));
    const b = Math.min(this.axis.get().duration, Math.max(d.t0, t));
    if (b - a < 0.02) return;
    this.axis.setLoop(a, b);
    this.axis.setPlayhead(a);
    if (this.media) this.media.currentTime = a;
    this.announceLoop();
  }

  private cancelDrag(): void {
    this.drag = null;
    this.selEl.style.display = "none";
  }

  private showSelection(a: number, b: number): void {
    const s = this.axis.get();
    const w = this.width;
    const x0 = timeToX(Math.min(a, b), s.start, s.span, w);
    const x1 = timeToX(Math.max(a, b), s.start, s.span, w);
    this.selEl.style.display = "";
    this.selEl.style.transform = `translateX(${x0.toFixed(1)}px)`;
    this.selEl.style.width = `${Math.max(1, x1 - x0).toFixed(1)}px`;
  }

  private miniMove(e: PointerEvent): void {
    const r = this.minimap.getBoundingClientRect();
    const s = this.axis.get();
    const t = ((e.clientX - r.left) / Math.max(1, r.width)) * s.duration;
    this.axis.setRange(t - s.span / 2, s.span);
  }

  private wheel(e: WheelEvent): void {
    e.preventDefault();
    const s = this.axis.get();
    if (e.ctrlKey || e.metaKey) {
      const r = this.root.getBoundingClientRect();
      const at = xToTime(e.clientX - r.left, s.start, s.span, r.width);
      this.axis.zoomBy(e.deltaY > 0 ? 1.25 : 0.8, at, true);
    } else {
      const px = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
      this.axis.setRange(s.start + px * (s.span / Math.max(1, this.width)), s.span);
    }
  }

  // ---------------------------------------------------------------- drawing

  invalidate(): void {
    this.dirty = true;
    this.sched.kick();
  }

  readTheme(): void {
    const cs = this.win.getComputedStyle(this.root);
    const get = (name: string, fallback: string) => resolveColor(cs.getPropertyValue(name).trim() || fallback, this.doc);
    this.colors = {
      text: get("--cadence-chart-axis", "rgb(96,100,108)"),
      muted: get("--cadence-text-secondary", "rgb(96,100,108)"),
      grid: get("--cadence-chart-grid", "rgb(185,187,198)"),
      wave: get("--cadence-chart-6", "rgb(62,99,221)"),
      clip: get("--cadence-status-failed", "rgb(229,72,77)"),
      viewport: get("--cadence-accent-line", "rgb(62,99,221)"),
    };
    this.invalidate();
  }

  private measure(force = false): void {
    const w = Math.floor(this.root.clientWidth);
    const dpr = this.win.devicePixelRatio || 1;
    if (!force && w === this.width && dpr === this.dpr) return;
    this.width = w;
    this.dpr = dpr;
    for (const c of [this.ruler, this.wave, this.minimap, ...this.specs]) {
      c.width = Math.max(1, Math.round(w * dpr));
      c.height = Math.max(1, Math.round((c.clientHeight || 1) * dpr));
    }
    this.invalidate();
  }

  frame(now: number): boolean {
    if (this.destroyed) return false;
    const animating = this.axis.tick(now);
    const m = this.media;
    let playing = false;
    if (m && !m.paused) {
      playing = true;
      const s = this.axis.get();
      let t = m.currentTime;
      if (s.loop && t >= s.loop[1]) {
        m.currentTime = s.loop[0];
        t = s.loop[0];
      }
      this.axis.setPlayhead(t);
      // Follow the playhead: page at the edges with reduced motion, scroll continuously otherwise.
      if (this.reducedMotion) {
        if (t > s.start + s.span * 0.95 || t < s.start) this.axis.setRange(t - s.span * 0.05, s.span);
      } else if (t > s.start + s.span * 0.3 || t < s.start) this.axis.setRange(t - s.span * 0.3, s.span);
    }
    let retry = false;
    if (this.dirty && this.width > 0 && this.root.isConnected) {
      const t0 = performance.now();
      this.dirty = false;
      retry = !this.render();
      if (retry) this.dirty = true;
      this.lastDrawMs = performance.now() - t0;
      this.draws++;
      this.updateSummary(false);
    }
    return animating || playing || retry;
  }

  /** Draws every track; false when the spectrogram could not draw (lost context), so the next frame retries. */
  render(): boolean {
    const s = this.axis.get();
    this.drawRuler();
    this.drawWave();
    this.drawMinimap();
    let ok = true;
    if (this.spec && !this.renderer.unsupported) {
      this.specs.forEach((c, ch) => {
        ok = this.renderer.drawInto(c, this.tileQuads(ch, c.width, c.height), this.uniforms(ch)) && ok;
      });
    }
    const w = this.width;
    for (const t of this.tracks) t.render(s.start, s.span, w);
    this.playheadEl.style.transform = `translateX(${timeToX(s.playhead, s.start, s.span, w).toFixed(1)}px)`;
    if (s.loop) {
      this.loopEl.style.display = "";
      const x0 = timeToX(s.loop[0], s.start, s.span, w);
      this.loopEl.style.transform = `translateX(${x0.toFixed(1)}px)`;
      this.loopEl.style.width = `${Math.max(1, timeToX(s.loop[1], s.start, s.span, w) - x0).toFixed(1)}px`;
    } else this.loopEl.style.display = "none";
    return ok;
  }

  private uniforms(ch: number) {
    const src = this.spec!;
    return { ...this.settings, fmaxHz: this.settings.fmaxHz, peakDb: src.peakDb[ch] ?? 0 };
  }

  /** The quads of channel ch: the top level first (always held), coarser fallbacks, then the target level. */
  tileQuads(ch: number, w: number, h: number): TileQuad[] {
    const src = this.spec;
    if (!src || src.levels.length === 0) return [];
    const s = this.axis.get();
    const target = pyramidLevel(s.span, w, HOP0, src.levels.length);
    const top = src.levels.length - 1;
    const quads: TileQuad[] = [];
    const push = (l: number, load: boolean) => {
      const lv = src.levels[l]!;
      const [a, b] = tileRange(lv, s.start, s.span, HOP0);
      for (let i = a; i <= b; i++) {
        const bytes = load ? src.tile(ch, l, i) : src.peek(ch, l, i);
        if (!bytes) continue;
        const [t0, t1] = tileSpan(lv, i, HOP0);
        quads.push({
          key: `${src.id}/${ch}/${l}/${i}`,
          bytes,
          bins: src.bins,
          binHz: src.binHz,
          srcFmaxHz: src.fmaxHz,
          x0: timeToX(t0, s.start, s.span, w),
          x1: timeToX(t1, s.start, s.span, w),
          y0: 0,
          y1: h,
        });
      }
      if (load) {
        // Prefetch one tile either side.
        if (a > 0) src.tile(ch, l, a - 1);
        if (b < lv.tiles - 1) src.tile(ch, l, b + 1);
      }
    };
    push(top, true);
    for (let l = Math.min(top - 1, target + 2); l > target; l--) push(l, false);
    if (target < top) push(target, true);
    return quads;
  }

  private drawRuler(): void {
    const c = this.ruler;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    const { width: w, height: h } = c;
    ctx.clearRect(0, 0, w, h);
    const s = this.axis.get();
    const step = rulerStep(s.span, w, 80 * this.dpr);
    ctx.fillStyle = this.colors.text;
    ctx.strokeStyle = this.colors.grid;
    ctx.font = `${11 * this.dpr}px system-ui, sans-serif`;
    ctx.textBaseline = "top";
    ctx.beginPath();
    for (let t = Math.ceil(s.start / step) * step; t <= s.start + s.span; t += step) {
      const x = Math.round(timeToX(t, s.start, s.span, w)) + 0.5;
      ctx.moveTo(x, h * 0.55);
      ctx.lineTo(x, h);
      ctx.fillText(formatTime(t, step * 10), x + 3 * this.dpr, 2 * this.dpr);
    }
    ctx.stroke();
  }

  private drawWave(): void {
    const c = this.wave;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    const { width: w, height: h } = c;
    ctx.clearRect(0, 0, w, h);
    const p = this.peaks;
    if (!p) return;
    const s = this.axis.get();
    const lane = h / p.channels;
    const level = p.levelFor(s.span, w);
    const scale = lane / 2 / 127;
    ctx.fillStyle = this.colors.wave;
    for (let ch = 0; ch < p.channels; ch++) {
      const mid = lane * ch + lane / 2;
      ctx.beginPath();
      for (let x = 0; x < w; x++) {
        const [lo, hi] = p.range(ch, s.start + (x / w) * s.span, s.start + ((x + 1) / w) * s.span, level);
        ctx.rect(x, mid - hi * scale, 1, Math.max(1, (hi - lo) * scale));
      }
      ctx.fill();
    }
    if (p.clipped.length) {
      ctx.fillStyle = this.colors.clip;
      for (const f of p.clipped) {
        const t = p.start + f * p.hopS;
        if (t < s.start || t > s.start + s.span) continue;
        ctx.fillRect(Math.round(timeToX(t, s.start, s.span, w)), 0, Math.max(1, this.dpr), 3 * this.dpr);
      }
    }
  }

  private drawMinimap(): void {
    const c = this.minimap;
    const ctx = c.getContext("2d");
    if (!ctx) return;
    const { width: w, height: h } = c;
    ctx.clearRect(0, 0, w, h);
    const s = this.axis.get();
    const p = this.peaks;
    if (p && s.duration > 0) {
      const level = p.levelFor(s.duration, w);
      ctx.fillStyle = this.colors.muted;
      ctx.beginPath();
      for (let x = 0; x < w; x++) {
        let lo = 0;
        let hi = 0;
        for (let ch = 0; ch < p.channels; ch++) {
          const [a, b] = p.range(ch, (x / w) * s.duration, ((x + 1) / w) * s.duration, level);
          lo = Math.min(lo, a);
          hi = Math.max(hi, b);
        }
        ctx.rect(x, h / 2 - (hi / 127) * (h / 2), 1, Math.max(1, ((hi - lo) / 127) * (h / 2)));
      }
      ctx.fill();
    }
    if (s.duration > 0) {
      const x0 = (s.start / s.duration) * w;
      const x1 = ((s.start + s.span) / s.duration) * w;
      ctx.strokeStyle = this.colors.viewport;
      ctx.lineWidth = Math.max(1, this.dpr);
      ctx.strokeRect(x0 + 0.5, 0.5, Math.max(2, x1 - x0) - 1, h - 1);
      ctx.fillStyle = this.colors.clip;
      ctx.fillRect((s.playhead / s.duration) * w, 0, Math.max(1, this.dpr), h);
    }
  }

  // ---------------------------------------------------------------- summary

  summaryText(): string {
    const s = this.axis.get();
    const p = this.peaks;
    const chs = p?.channels ?? this.spec?.channels ?? 1;
    const labels = this.o.channelLabels;
    const ch = chs === 1 ? "mono" : labels?.length === chs ? `${chs} channels: ${labels.join(", ")}` : `${chs} channels`;
    const loop = s.loop ? ` Loop ${formatTime(s.loop[0], 1)}–${formatTime(s.loop[1], 1)}.` : "";
    const tracks = ["waveform", ...(this.spec ? ["spectrogram"] : []), ...this.tracks.map((t) => `${t.data.label} (${t.data.words.length} words)`)];
    const tr = this.tracks[0];
    const w = tr?.word(wordIndexAt(tr.data.words, s.playhead));
    const clips = p?.clipped.length ? ` Clipping at ${p.clipped.length} points.` : "";
    return (
      `${this.title}: ${formatTime(s.duration, 100)}, ${ch}. Showing ${formatTime(s.start, s.span)} to ${formatTime(s.start + s.span, s.span)}. ` +
      `Playhead ${formatTime(s.playhead, s.span)}.${loop}${clips} Tracks: ${tracks.join(", ")}.` +
      (w && w.start <= s.playhead ? ` Word at playhead: ${w.word}.` : "")
    );
  }

  private updateSummary(force: boolean): void {
    const now = performance.now();
    if (!force && now - this.summaryAt < SUMMARY_MS) return;
    this.summaryAt = now;
    const t = this.summaryText();
    if (this.summary.textContent !== t) this.summary.textContent = t;
  }

  // ---------------------------------------------------------------- tests and measurements

  /** Spectrogram canvases holding a picture (more than a few colours) vs blank ones. */
  probe(): { canvases: number; blank: number } {
    let blank = 0;
    for (const c of this.specs) {
      const ctx = c.getContext("2d");
      if (!ctx || c.width === 0) continue;
      const d = ctx.getImageData(0, 0, c.width, c.height).data;
      const seen = new Set<number>();
      for (let i = 0; i < d.length && seen.size < 16; i += 4 * 97) seen.add((d[i]! << 16) | (d[i + 1]! << 8) | d[i + 2]!);
      if (seen.size < 4) blank++;
    }
    return { canvases: this.specs.length, blank };
  }

  /** The centre pixel of each spectrogram canvas. */
  samplePixels(): number[][] {
    return this.specs.map((c) => {
      const d = c.getContext("2d")!.getImageData(Math.floor(c.width / 2), Math.floor(c.height / 2), 1, 1).data;
      return [d[0]!, d[1]!, d[2]!];
    });
  }

  get tileFrames(): number {
    return TILE;
  }
}
