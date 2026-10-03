// Data behind the tracks. A spectrogram source serves uint8 dB tiles of TILE frames x bins (bins-major, R8-ready) for
// a pyramid level; the server pyramid (the throwaway step's output) and the browser STFT share the shape.
import type { StftRequest, StftResponse } from "./stft.worker";

export const TILE = 512;
export const FLOOR_DB = -120;
export const STEP_DB = 0.5;

export type Level = { level: number; hopS: number; frames: number; tiles: number };
export interface SpecSource {
  readonly id: string;
  readonly channels: number;
  readonly bins: number; // bins kept (≤ 257)
  readonly binHz: number;
  readonly levels: Level[];
  readonly peakDb: number[];
  readonly fmaxHz: number; // the origin's Nyquist
  /** The tile's bytes if cached; otherwise starts loading and calls onLoad when they arrive. */
  tile(ch: number, level: number, i: number): Uint8Array | undefined;
  onLoad(fn: () => void): () => void;
  cachedBytes(): number;
}

class Listeners {
  private fns = new Set<() => void>();
  on(fn: () => void) {
    this.fns.add(fn);
    return () => void this.fns.delete(fn);
  }
  emit() {
    for (const f of this.fns) f();
  }
}

type Manifest = {
  channels: number;
  bins: number;
  binHz: number;
  originSampleRate: number;
  levels: Level[];
  peakDb: number[];
  peaks: { hopS: number; frames: number };
  duration: number;
};

/** Tiles fetched on demand from the step's directory artifact; an LRU of bytes bounds what the tab holds. */
export class PyramidSource implements SpecSource {
  readonly id: string;
  readonly channels: number;
  readonly bins: number;
  readonly binHz: number;
  readonly levels: Level[];
  readonly peakDb: number[];
  readonly fmaxHz: number;
  private cache = new Map<string, Uint8Array>();
  private inflight = new Set<string>();
  private bytes = 0;
  private ls = new Listeners();
  fetched = 0;
  fetchMs: number[] = [];
  constructor(
    private base: string,
    m: Manifest,
    private maxBytes = 24 << 20,
  ) {
    this.id = base;
    this.channels = m.channels;
    this.bins = m.bins;
    this.binHz = m.binHz;
    this.levels = m.levels;
    this.peakDb = m.peakDb;
    this.fmaxHz = m.originSampleRate / 2;
  }
  static async load(base: string): Promise<{ src: PyramidSource; manifest: Manifest }> {
    const m = (await (await fetch(`${base}/manifest.json`)).json()) as Manifest;
    const src = new PyramidSource(base, m);
    // The top level is one tile per channel: always resident, the fallback while finer tiles load.
    const top = m.levels.length - 1;
    await Promise.all(Array.from({ length: m.channels }, (_, ch) => src.fetchTile(ch, top, 0)));
    return { src, manifest: m };
  }
  onLoad(fn: () => void) {
    return this.ls.on(fn);
  }
  cachedBytes() {
    return this.bytes;
  }
  tile(ch: number, level: number, i: number): Uint8Array | undefined {
    const k = `${ch}/${level}/${i}`;
    const t = this.cache.get(k);
    if (t) {
      this.cache.delete(k); // LRU touch
      this.cache.set(k, t);
      return t;
    }
    void this.fetchTile(ch, level, i);
    return undefined;
  }
  /** Cached bytes only (fallback levels never trigger a fetch). */
  peek(ch: number, level: number, i: number): Uint8Array | undefined {
    return this.cache.get(`${ch}/${level}/${i}`);
  }
  private async fetchTile(ch: number, level: number, i: number): Promise<void> {
    const k = `${ch}/${level}/${i}`;
    if (this.inflight.has(k) || this.cache.has(k)) return;
    this.inflight.add(k);
    const t0 = performance.now();
    const buf = new Uint8Array(await (await fetch(`${this.base}/c${ch}/l${level}/${i}.u8`)).arrayBuffer());
    this.fetchMs.push(performance.now() - t0);
    this.fetched++;
    this.inflight.delete(k);
    this.cache.set(k, buf);
    this.bytes += buf.byteLength;
    const top = this.levels.length - 1;
    for (const [key, v] of this.cache) {
      if (this.bytes <= this.maxBytes) break;
      if (key.split("/")[1] === String(top)) continue;
      this.cache.delete(key);
      this.bytes -= v.byteLength;
    }
    this.ls.emit();
  }
}

/** The browser path (spans under 10 minutes): PCM → STFT in the worker → the same tile shape, pyramid in memory. */
export class StftSource implements SpecSource {
  readonly channels = 1;
  readonly bins = 257;
  readonly binHz = 16000 / 512;
  readonly levels: Level[] = [];
  readonly peakDb: number[];
  readonly fmaxHz = 8000;
  private tiles: Uint8Array[][] = []; // [level][i]
  private ls = new Listeners();
  constructor(
    readonly id: string,
    frameMajor: Uint8Array,
    frames: number,
  ) {
    let peak = 0;
    for (let i = 0; i < frameMajor.length; i++) if (frameMajor[i]! > peak) peak = frameMajor[i]!;
    this.peakDb = [peak * STEP_DB + FLOOR_DB];
    let cur = frameMajor;
    let n = frames;
    for (let level = 0; ; level++) {
      const count = Math.ceil(n / TILE);
      this.levels.push({ level, hopS: 0.01 * 2 ** level, frames: n, tiles: count });
      const lt: Uint8Array[] = [];
      for (let i = 0; i < count; i++) {
        const t = new Uint8Array(TILE * this.bins);
        for (let f = 0; f < TILE && i * TILE + f < n; f++) {
          const row = (i * TILE + f) * this.bins;
          for (let k = 0; k < this.bins; k++) t[k * TILE + f] = cur[row + k]!;
        }
        lt.push(t);
      }
      this.tiles.push(lt);
      if (n <= TILE) break;
      const m = Math.ceil(n / 2);
      const next = new Uint8Array(m * this.bins);
      for (let f = 0; f < m; f++) {
        const a = 2 * f * this.bins;
        const b = Math.min(2 * f + 1, n - 1) * this.bins;
        for (let k = 0; k < this.bins; k++) next[f * this.bins + k] = Math.max(cur[a + k]!, cur[b + k]!);
      }
      cur = next;
      n = m;
    }
  }
  tile(_ch: number, level: number, i: number) {
    return this.tiles[level]?.[i];
  }
  onLoad(fn: () => void) {
    return this.ls.on(fn);
  }
  cachedBytes() {
    return this.tiles.reduce((s, l) => s + l.length * TILE * this.bins, 0);
  }
}

let worker: Worker | undefined;
let nextId = 1;
const pending = new Map<number, (r: StftResponse) => void>();
export function stftWorker(req: Omit<StftRequest, "id">): Promise<StftResponse> {
  if (!worker) {
    worker = new Worker(new URL("./stft.worker.ts", import.meta.url), { type: "module" });
    worker.onmessage = (e: MessageEvent<StftResponse>) => {
      pending.get(e.data.id)?.(e.data);
      pending.delete(e.data.id);
    };
  }
  const id = nextId++;
  return new Promise((resolve) => {
    pending.set(id, resolve);
    worker!.postMessage({ ...req, id }, [req.pcm.buffer]);
  });
}

/** Min/max peaks, [frames x channels x 2] at 10 ms, with a max-pooled pyramid built once in memory. */
export class Peaks {
  readonly levels: Int8Array[] = [];
  constructor(
    readonly channels: number,
    base: Int8Array,
    readonly hopS = 0.01,
  ) {
    this.levels.push(base);
    let cur = base;
    while (cur.length / (2 * channels) > 1024) {
      const n = cur.length / (2 * channels);
      const m = Math.floor(n / 2);
      const next = new Int8Array(m * 2 * channels);
      for (let f = 0; f < m; f++)
        for (let c = 0; c < channels; c++) {
          const a = (2 * f * channels + c) * 2;
          const b = ((2 * f + 1) * channels + c) * 2;
          const o = (f * channels + c) * 2;
          next[o] = Math.min(cur[a]!, cur[b]!);
          next[o + 1] = Math.max(cur[a + 1]!, cur[b + 1]!);
        }
      this.levels.push(next);
      cur = next;
    }
  }
  static fromPcm(pcm: Float32Array, sr: number): Peaks {
    const hop = sr / 100;
    const n = Math.floor(pcm.length / hop);
    const out = new Int8Array(n * 2);
    for (let f = 0; f < n; f++) {
      let lo = 1;
      let hi = -1;
      for (let i = f * hop; i < (f + 1) * hop; i++) {
        const v = pcm[i]!;
        if (v < lo) lo = v;
        if (v > hi) hi = v;
      }
      out[2 * f] = Math.round(lo * 127);
      out[2 * f + 1] = Math.round(hi * 127);
    }
    return new Peaks(1, out);
  }
  bytes() {
    return this.levels.reduce((s, l) => s + l.byteLength, 0);
  }
}

/** The model-input track: float16 [80 x frames] chunks, loaded only for spans the view can show at frame detail. */
export class Features {
  private cache = new Map<number, Uint16Array>();
  private inflight = new Set<number>();
  private ls = new Listeners();
  constructor(
    readonly url: (chunk: number) => string,
    readonly frames: number,
    readonly chunk: number,
    readonly mels = 80,
    readonly dimAboveMel?: number, // first mel filter above the origin's Nyquist (holds dither only)
  ) {}
  get(i: number): Uint16Array | undefined {
    const c = this.cache.get(i);
    if (c) return c;
    if (!this.inflight.has(i)) {
      this.inflight.add(i);
      void fetch(this.url(i))
        .then((r) => r.arrayBuffer())
        .then((b) => {
          this.cache.set(i, new Uint16Array(b));
          if (this.cache.size > 6) this.cache.delete(this.cache.keys().next().value!);
          this.inflight.delete(i);
          this.ls.emit();
        });
    }
    return undefined;
  }
  onLoad(fn: () => void) {
    return this.ls.on(fn);
  }
  bytes() {
    let s = 0;
    for (const v of this.cache.values()) s += v.byteLength;
    return s;
  }
}

export type Word = { s: number; e: number; w: string; c: number; op: "" | "S" | "I" | "D" };
export type WordTrackData = { id: string; label: string; lang: string; words: Word[] };
