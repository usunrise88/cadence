// Spectrogram tiles (R52; S5 tile format): uint8 dB tiles of TILE frames × bins, bins-major (bin 0 first), ready
// for an R8 texture. Level 0 is the 10 ms grid; level L max-pools 2^L frames. The server pyramid
// (spectrogram_tiles@1, read through spectrogram.get) and the browser STFT share the shape.
import type { SpectrogramManifest } from "@/api/gen/types.gen";

export const TILE = 512;

export type Level = { level: number; hopS: number; frames: number; tiles: number };

/** A tile pyramid of one audio, from the server or computed in the browser. */
export interface SpecSource {
  readonly id: string;
  readonly channels: number;
  /** Bins kept (≤ 257): up to the origin's Nyquist. */
  readonly bins: number;
  readonly binHz: number;
  readonly levels: Level[];
  /** Peak dB per channel (the range's reference). */
  readonly peakDb: number[];
  /** The origin's Nyquist (Hz): above it the view draws the "no data" band and a line. */
  readonly fmaxHz: number;
  /** The tile's bytes when held; otherwise starts loading it (onLoad fires when it arrives). */
  tile(ch: number, level: number, i: number): Uint8Array | undefined;
  /** The tile's bytes when held; never loads (coarser fallback levels). */
  peek(ch: number, level: number, i: number): Uint8Array | undefined;
  onLoad(fn: () => void): () => void;
  /** Bytes held in memory. */
  bytes(): number;
}

/** The tiles of a level a range covers: [first, last] inclusive, clamped to the level. */
export function tileRange(level: Level, start: number, span: number, hop0: number): [number, number] {
  const tileS = TILE * level.hopS;
  // Frame f is centred at f × hop: a tile starts half a base hop before its first frame.
  const a = Math.max(0, Math.floor((start + hop0 / 2) / tileS));
  const b = Math.min(level.tiles - 1, Math.floor((start + span + hop0 / 2) / tileS));
  return [a, b];
}

/** Where tile i of a level starts and ends on the time axis (seconds). */
export function tileSpan(level: Level, i: number, hop0: number): [number, number] {
  const tileS = TILE * level.hopS;
  const t0 = i * tileS - hop0 / 2;
  return [t0, t0 + tileS];
}

/** The path of a tile inside a spectrogram_tiles artifact (spectrogram.get?tile=…, without ".u8"). */
export function tilePath(ch: number, level: number, i: number): string {
  return `c${ch}/l${level}/${i}`;
}

/** The levels of a pyramid whose base has `frames` frames of `hopS`: halve until one tile holds a level. */
export function levelTable(frames: number, hopS: number): Level[] {
  const out: Level[] = [];
  let n = frames;
  for (let level = 0; ; level++) {
    out.push({ level, hopS: hopS * 2 ** level, frames: n, tiles: Math.max(1, Math.ceil(n / TILE)) });
    if (n <= TILE) return out;
    n = Math.ceil(n / 2);
  }
}

/**
 * Builds the pyramid of frame-major uint8 dB [frames × bins]: per level, bins-major tiles of TILE frames (the last
 * zero-padded); the next level is the max over pairs of frames (an odd last frame pairs with itself).
 */
export function buildPyramid(frameMajor: Uint8Array, frames: number, bins: number, hopS: number): { levels: Level[]; tiles: Uint8Array[][] } {
  const levels = levelTable(frames, hopS);
  const tiles: Uint8Array[][] = [];
  let cur = frameMajor;
  let n = frames;
  for (const lv of levels) {
    const lt: Uint8Array[] = [];
    for (let i = 0; i < lv.tiles; i++) {
      const t = new Uint8Array(TILE * bins);
      for (let f = 0; f < TILE && i * TILE + f < n; f++) {
        const row = (i * TILE + f) * bins;
        for (let k = 0; k < bins; k++) t[k * TILE + f] = cur[row + k]!;
      }
      lt.push(t);
    }
    tiles.push(lt);
    if (n <= TILE) break;
    const m = Math.ceil(n / 2);
    const next = new Uint8Array(m * bins);
    for (let f = 0; f < m; f++) {
      const a = 2 * f * bins;
      const b = Math.min(2 * f + 1, n - 1) * bins;
      for (let k = 0; k < bins; k++) next[f * bins + k] = Math.max(cur[a + k]!, cur[b + k]!);
    }
    cur = next;
    n = m;
  }
  return { levels, tiles };
}

class Listeners {
  private fns = new Set<() => void>();
  on(fn: () => void): () => void {
    this.fns.add(fn);
    return () => void this.fns.delete(fn);
  }
  emit(): void {
    for (const f of this.fns) f();
  }
}

/** The browser path: a pyramid held in memory (audio up to views.audio.browser_stft_max_s). */
export class MemorySource implements SpecSource {
  readonly id: string;
  readonly channels: number;
  readonly bins: number;
  readonly binHz: number;
  readonly levels: Level[];
  readonly peakDb: number[];
  readonly fmaxHz: number;
  private tiles: Uint8Array[][][]; // [channel][level][i]
  private ls = new Listeners();

  constructor(id: string, perChannel: { frameMajor: Uint8Array; frames: number }[], bins: number, binHz: number, hopS: number, fmaxHz: number) {
    this.id = id;
    this.channels = perChannel.length;
    this.bins = bins;
    this.binHz = binHz;
    this.fmaxHz = fmaxHz;
    this.tiles = [];
    this.peakDb = [];
    let levels: Level[] = [];
    for (const c of perChannel) {
      let peak = 0;
      for (let i = 0; i < c.frameMajor.length; i++) if (c.frameMajor[i]! > peak) peak = c.frameMajor[i]!;
      this.peakDb.push(peak * 0.5 - 120);
      const p = buildPyramid(c.frameMajor, c.frames, bins, hopS);
      levels = p.levels;
      this.tiles.push(p.tiles);
    }
    this.levels = levels;
  }
  tile(ch: number, level: number, i: number): Uint8Array | undefined {
    return this.tiles[ch]?.[level]?.[i];
  }
  peek(ch: number, level: number, i: number): Uint8Array | undefined {
    return this.tile(ch, level, i);
  }
  onLoad(fn: () => void): () => void {
    return this.ls.on(fn);
  }
  bytes(): number {
    let s = 0;
    for (const ch of this.tiles) for (const lv of ch) s += lv.length * TILE * this.bins;
    return s;
  }
}

/** A byte-bounded LRU of tiles; keys of the pinned level are never evicted (the fallback while finer tiles load). */
export class TileCache {
  private map = new Map<string, Uint8Array>();
  private size = 0;
  private maxBytes: number;
  private pinned: (key: string) => boolean;
  constructor(maxBytes: number, pinned: (key: string) => boolean = () => false) {
    this.maxBytes = maxBytes;
    this.pinned = pinned;
  }
  get(key: string): Uint8Array | undefined {
    const v = this.map.get(key);
    if (v) {
      this.map.delete(key);
      this.map.set(key, v);
    }
    return v;
  }
  peek(key: string): Uint8Array | undefined {
    return this.map.get(key);
  }
  has(key: string): boolean {
    return this.map.has(key);
  }
  set(key: string, v: Uint8Array): void {
    const old = this.map.get(key);
    if (old) this.size -= old.byteLength;
    this.map.delete(key);
    this.map.set(key, v);
    this.size += v.byteLength;
    for (const [k, b] of this.map) {
      if (this.size <= this.maxBytes) break;
      if (this.pinned(k) || k === key) continue;
      this.map.delete(k);
      this.size -= b.byteLength;
    }
  }
  bytes(): number {
    return this.size;
  }
  keys(): string[] {
    return [...this.map.keys()];
  }
}

export type TileFetcher = (path: string) => Promise<Uint8Array>;

/** The server path: tiles of a spectrogram_tiles artifact fetched on demand, bounded by views.audio.tile_cache_mb. */
export class ServerSource implements SpecSource {
  readonly id: string;
  readonly channels: number;
  readonly bins: number;
  readonly binHz: number;
  readonly levels: Level[];
  readonly peakDb: number[];
  readonly fmaxHz: number;
  private cache: TileCache;
  private inflight = new Set<string>();
  private failed = new Set<string>();
  private ls = new Listeners();
  private fetcher: TileFetcher;

  constructor(m: SpectrogramManifest, fetcher: TileFetcher, maxBytes: number) {
    this.id = m.artifact ?? m.audio ?? "tiles";
    this.channels = m.channels;
    this.bins = m.bins;
    this.binHz = m.binHz;
    this.levels = m.levels;
    this.peakDb = m.peakDb;
    this.fmaxHz = Math.min(m.originSampleRate, m.sampleRate) / 2;
    this.fetcher = fetcher;
    const top = `/l${m.levels.length - 1}/`;
    this.cache = new TileCache(maxBytes, (k) => k.includes(top));
  }
  /** Loads the top level of every channel (one tile each): the fallback that is always drawn first. */
  async warm(): Promise<void> {
    const top = this.levels.length - 1;
    await Promise.all(Array.from({ length: this.channels }, (_, ch) => this.load(ch, top, 0)));
  }
  tile(ch: number, level: number, i: number): Uint8Array | undefined {
    const k = tilePath(ch, level, i);
    const t = this.cache.get(k);
    if (!t) void this.load(ch, level, i);
    return t;
  }
  peek(ch: number, level: number, i: number): Uint8Array | undefined {
    return this.cache.peek(tilePath(ch, level, i));
  }
  private async load(ch: number, level: number, i: number): Promise<void> {
    const k = tilePath(ch, level, i);
    if (this.inflight.has(k) || this.cache.has(k) || this.failed.has(k)) return;
    this.inflight.add(k);
    try {
      const b = await this.fetcher(k);
      if (b.byteLength === TILE * this.bins) this.cache.set(k, b);
      else this.failed.add(k);
    } catch {
      this.failed.add(k);
    } finally {
      this.inflight.delete(k);
    }
    this.ls.emit();
  }
  onLoad(fn: () => void): () => void {
    return this.ls.on(fn);
  }
  bytes(): number {
    return this.cache.bytes();
  }
}
