// The acoustic spectrogram's STFT (R52), the same as the server's spectrogram_tiles@1: a periodic Hann window of
// `win` samples centred in an `nFft`-point frame, every `hop` samples, frames centred with zero padding at both ends
// (librosa center=True, constant pad), magnitudes scaled so a sine's bin reads its amplitude, dB re full scale,
// encoded as uint8 (dB = -120 + 0.5 × value). S5: the FFT kernel is not the cost (≈ 45 ms per minute of audio in a
// worker); the windowing and log10 are.

export const FLOOR_DB = -120;
export const STEP_DB = 0.5;

/** In-place iterative radix-2 complex FFT; n must be a power of two. */
export function fft(re: Float64Array, im: Float64Array): void {
  const n = re.length;
  if (n & (n - 1)) throw new Error(`FFT size ${n} is not a power of two`);
  for (let i = 1, j = 0; i < n; i++) {
    let bit = n >> 1;
    for (; j & bit; bit >>= 1) j ^= bit;
    j ^= bit;
    if (i < j) {
      [re[i], re[j]] = [re[j]!, re[i]!];
      [im[i], im[j]] = [im[j]!, im[i]!];
    }
  }
  for (let len = 2; len <= n; len <<= 1) {
    const ang = (-2 * Math.PI) / len;
    const wr = Math.cos(ang);
    const wi = Math.sin(ang);
    for (let i = 0; i < n; i += len) {
      let cr = 1;
      let ci = 0;
      for (let k = 0; k < len / 2; k++) {
        const a = i + k;
        const b = a + len / 2;
        const xr = re[b]! * cr - im[b]! * ci;
        const xi = re[b]! * ci + im[b]! * cr;
        re[b] = re[a]! - xr;
        im[b] = im[a]! - xi;
        re[a] = re[a]! + xr;
        im[a] = im[a]! + xi;
        const t = cr * wr - ci * wi;
        ci = cr * wi + ci * wr;
        cr = t;
      }
    }
  }
}

export type StftParams = { win: number; hop: number; nFft: number; bins: number };

export function quantize(db: number): number {
  const q = Math.round((db - FLOOR_DB) / STEP_DB);
  return q < 0 ? 0 : q > 255 ? 255 : q;
}

/** uint8 dB, frame-major [frames × bins], of mono samples in [-1, 1]. */
export function stftU8(pcm: Float32Array, p: StftParams): { frames: number; u8: Uint8Array } {
  const { win, hop, nFft, bins } = p;
  const window = new Float64Array(nFft);
  let sum = 0;
  const off = (nFft - win) >> 1;
  for (let n = 0; n < win; n++) {
    const w = 0.5 - 0.5 * Math.cos((2 * Math.PI * n) / win); // periodic Hann
    window[off + n] = w;
    sum += w;
  }
  const scale = 2 / sum;
  const frames = 1 + Math.floor(pcm.length / hop);
  const u8 = new Uint8Array(frames * bins);
  const re = new Float64Array(nFft);
  const im = new Float64Array(nFft);
  const half = nFft >> 1;
  for (let f = 0; f < frames; f++) {
    const c = f * hop - half;
    for (let n = 0; n < nFft; n++) {
      const i = c + n;
      re[n] = i >= 0 && i < pcm.length ? pcm[i]! * window[n]! : 0;
      im[n] = 0;
    }
    fft(re, im);
    const row = f * bins;
    for (let k = 0; k < bins; k++) {
      const mag = Math.sqrt(re[k]! * re[k]! + im[k]! * im[k]!) * scale;
      u8[row + k] = quantize(20 * Math.log10(mag > 1e-7 ? mag : 1e-7));
    }
  }
  return { frames, u8 };
}
