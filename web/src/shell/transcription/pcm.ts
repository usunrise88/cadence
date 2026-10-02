// PCM16 framing shared by the capture worklet and the page (R50): channel 0 only, at the capture rate, 16-bit
// little-endian, frames of a fixed duration (transcriptions.frame_ms, 20 ms), with the frame's peak and the samples
// at full scale for the level meter's clipping mark.

/** Full scale: a float sample at or beyond this magnitude counts as clipped. */
export const CLIP_LEVEL = 0.999;

/** Samples per frame of frameMs at rate (at least one). */
export function frameSamples(rate: number, frameMs: number): number {
  return Math.max(1, Math.round((rate * frameMs) / 1000));
}

/** One float sample as PCM16 (clamped; −1 → −32768, +1 → 32767). */
export function toInt16(s: number): number {
  const c = s < -1 ? -1 : s > 1 ? 1 : s;
  return c < 0 ? Math.round(c * 0x8000) : Math.round(c * 0x7fff);
}

export type PcmFrame = { pcm: ArrayBuffer; peak: number; clipped: number };

/** Accumulates channel-0 samples into fixed-size PCM16 frames. */
export class Framer {
  private buf: Int16Array;
  private n = 0;
  private peak = 0;
  private clipped = 0;
  readonly size: number;

  constructor(size: number) {
    this.size = Math.max(1, size);
    this.buf = new Int16Array(this.size);
  }

  /** Adds samples; returns the frames they completed. */
  push(samples: ArrayLike<number>): PcmFrame[] {
    const out: PcmFrame[] = [];
    for (let i = 0; i < samples.length; i++) {
      const s = samples[i]!;
      const a = Math.abs(s);
      if (a > this.peak) this.peak = a;
      if (a >= CLIP_LEVEL) this.clipped++;
      this.buf[this.n++] = toInt16(s);
      if (this.n === this.size) {
        out.push({ pcm: this.buf.buffer as ArrayBuffer, peak: this.peak, clipped: this.clipped });
        this.buf = new Int16Array(this.size);
        this.n = 0;
        this.peak = 0;
        this.clipped = 0;
      }
    }
    return out;
  }
}

/** PCM16 little-endian bytes as float samples (the page's own copy of what it sent). */
export function pcm16ToFloat(pcm: ArrayBuffer): Float32Array {
  const v = new Int16Array(pcm);
  const out = new Float32Array(v.length);
  for (let i = 0; i < v.length; i++) out[i] = v[i]! / 32768;
  return out;
}
