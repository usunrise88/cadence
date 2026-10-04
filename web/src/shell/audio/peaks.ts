// Waveform data: int8 min/max pairs per channel (peaks.get: frames × channels × [min, max], value / 127 = sample),
// with coarser levels max-pooled in memory once, so drawing any zoom reads about one pair per pixel (S5).
import type { AudioPeaks } from "@/api/gen/types.gen";

/** What the waveform and minimap read: min/max over a time range at a level chosen for the drawn width. */
export interface PeakSource {
  readonly channels: number;
  /** Seconds per pair of the clipped marks' grid. */
  readonly hopS: number;
  /** Start time of the clipped marks' grid. */
  readonly start: number;
  readonly clipped: number[];
  range(ch: number, t0: number, t1: number, level: number): [number, number];
  levelFor(span: number, widthPx: number): number;
}

export class PeakPyramid implements PeakSource {
  readonly channels: number;
  readonly hopS: number;
  /** Start time of the first pair. */
  readonly start: number;
  readonly levels: Int8Array[] = [];
  /** Base-level frames where a sample reached full scale. */
  readonly clipped: number[];

  constructor(channels: number, base: Int8Array, hopS: number, start = 0, clipped: number[] = []) {
    this.channels = channels;
    this.hopS = hopS;
    this.start = start;
    this.clipped = clipped;
    this.levels.push(base);
    let cur = base;
    while (cur.length / (2 * channels) > 1024) {
      const n = cur.length / (2 * channels);
      const m = Math.ceil(n / 2);
      const next = new Int8Array(m * 2 * channels);
      for (let f = 0; f < m; f++) {
        for (let c = 0; c < channels; c++) {
          const a = (2 * f * channels + c) * 2;
          const b = (Math.min(2 * f + 1, n - 1) * channels + c) * 2;
          const o = (f * channels + c) * 2;
          next[o] = Math.min(cur[a]!, cur[b]!);
          next[o + 1] = Math.max(cur[a + 1]!, cur[b + 1]!);
        }
      }
      this.levels.push(next);
      cur = next;
    }
  }

  get frames(): number {
    return this.levels[0]!.length / (2 * this.channels);
  }

  /** Min and max (×127) of channel ch over [t0, t1), read from the level that has about one pair per step. */
  range(ch: number, t0: number, t1: number, level: number): [number, number] {
    const data = this.levels[level]!;
    const hop = this.hopS * 2 ** level;
    const n = data.length / (2 * this.channels);
    let a = Math.floor((t0 - this.start) / hop);
    let b = Math.max(a + 1, Math.floor((t1 - this.start) / hop));
    a = Math.max(0, Math.min(n, a));
    b = Math.max(a, Math.min(n, b));
    let lo = 127;
    let hi = -127;
    for (let i = a; i < b; i++) {
      const o = (i * this.channels + ch) * 2;
      if (data[o]! < lo) lo = data[o]!;
      if (data[o + 1]! > hi) hi = data[o + 1]!;
    }
    return a === b ? [0, 0] : [lo, hi];
  }

  /** The level to read for a span drawn across widthPx pixels. */
  levelFor(span: number, widthPx: number): number {
    const fpp = span / this.hopS / Math.max(1, widthPx);
    return Math.max(0, Math.min(this.levels.length - 1, Math.floor(Math.log2(Math.max(1, fpp)))));
  }
}

function fromBase64(s: string): Int8Array {
  const bin = atob(s);
  const out = new Int8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = (bin.charCodeAt(i) << 24) >> 24;
  return out;
}

// Long audio (a call recording; phase 4 tail): the server stores peaks with coarser levels (×16 each, cadence.peaks/2),
// so the view reads an overview of the whole audio at a coarse hop and 10 ms detail only for the span it shows when
// zoomed in, instead of the 10 ms peaks of hours.

/** Base hop of server peaks (10 ms) and the server's level factor. */
export const PEAKS_BASE_MS = 10;
export const PEAKS_LEVEL_FACTOR = 16;
/** Pairs an overview may hold; audio whose 10 ms peaks hold more is long. */
export const OVERVIEW_MAX_FRAMES = 32768;
/** Detail windows are whole blocks of this many seconds, so panning reuses them. */
export const DETAIL_BLOCK_S = 60;
/** Offset of overview levels in LongPeaks' level numbers (below it: detail levels). */
const OVERVIEW_LEVEL = 1000;

/** The overview hop (ms) of audio this long: the finest stored level that keeps the overview within OVERVIEW_MAX_FRAMES. */
export function overviewHopMs(durationS: number): number {
  let hop = PEAKS_BASE_MS;
  while ((durationS * 1000) / hop > OVERVIEW_MAX_FRAMES) hop *= PEAKS_LEVEL_FACTOR;
  return hop;
}

/**
 * The detail window to load for a visible range, or null when the overview has a pair per pixel already: from one
 * span before to one span after the view, widened to whole DETAIL_BLOCK_S blocks and clamped to the audio.
 */
export function detailWindow(start: number, span: number, widthPx: number, overviewHopS: number, durationS: number): [number, number] | null {
  if (overviewHopS <= PEAKS_BASE_MS / 1000 || span / Math.max(1, widthPx) >= overviewHopS) return null;
  const a = Math.max(0, Math.floor((start - span) / DETAIL_BLOCK_S) * DETAIL_BLOCK_S);
  const b = Math.min(durationS, Math.ceil((start + 2 * span) / DETAIL_BLOCK_S) * DETAIL_BLOCK_S);
  return b > a ? [a, b] : null;
}

/** An overview of the whole audio and, when loaded, 10 ms detail for a window of it. */
export class LongPeaks implements PeakSource {
  readonly overview: PeakPyramid;
  readonly detail: PeakPyramid | null;
  private detailEnd: number;

  constructor(overview: PeakPyramid, detail: PeakPyramid | null) {
    this.overview = overview;
    this.detail = detail;
    this.detailEnd = detail ? detail.start + detail.frames * detail.hopS : 0;
  }
  get channels(): number {
    return this.overview.channels;
  }
  get hopS(): number {
    return this.overview.hopS;
  }
  get start(): number {
    return this.overview.start;
  }
  get clipped(): number[] {
    return this.overview.clipped;
  }
  levelFor(span: number, widthPx: number): number {
    if (this.detail && span / Math.max(1, widthPx) < this.overview.hopS) return this.detail.levelFor(span, widthPx);
    return OVERVIEW_LEVEL + this.overview.levelFor(span, widthPx);
  }
  range(ch: number, t0: number, t1: number, level: number): [number, number] {
    if (level < OVERVIEW_LEVEL && this.detail && t0 >= this.detail.start && t1 <= this.detailEnd) return this.detail.range(ch, t0, t1, level);
    return this.overview.range(ch, t0, t1, level >= OVERVIEW_LEVEL ? level - OVERVIEW_LEVEL : 0);
  }
}

/** The pyramid of a peaks.get answer. */
export function peaksFromApi(p: AudioPeaks): PeakPyramid {
  return new PeakPyramid(p.channels, fromBase64(p.data), p.hopS, p.start, p.clipped ?? []);
}

/** Peaks of PCM channels at 10 ms (the browser path when the samples are already here). */
export function peaksFromPcm(channels: Float32Array[], rate: number): PeakPyramid {
  const hop = rate / 100;
  const n = Math.floor((channels[0]?.length ?? 0) / hop);
  const out = new Int8Array(n * 2 * channels.length);
  const clipped: number[] = [];
  for (let f = 0; f < n; f++) {
    let clip = false;
    channels.forEach((pcm, c) => {
      let lo = 1;
      let hi = -1;
      for (let i = Math.floor(f * hop); i < Math.floor((f + 1) * hop); i++) {
        const v = pcm[i]!;
        if (v < lo) lo = v;
        if (v > hi) hi = v;
      }
      if (hi >= 32766 / 32768 || lo <= -32766 / 32768) clip = true;
      out[(f * channels.length + c) * 2] = Math.round(Math.max(-1, lo) * 127);
      out[(f * channels.length + c) * 2 + 1] = Math.round(Math.min(1, hi) * 127);
    });
    if (clip) clipped.push(f);
  }
  return new PeakPyramid(channels.length, out, 0.01, 0, clipped);
}
