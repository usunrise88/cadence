/// <reference lib="webworker" />
// STFT in a Web Worker: 25 ms periodic Hann (400 samples at 16 kHz) centred in a 512-point frame, 10 ms hop,
// centred frames with zero padding (librosa center=True, pad_mode="constant"). Two FFT backends:
//   wasm — PFFFT compiled to WASM with SIMD (@echogarden/pffft-wasm 0.4.2, BSD-3-Clause)
//   js   — fourier-transform 2.5.1 (MIT, split-radix real FFT in plain JS)
// Output: uint8 dB (u8 = round((dB + 120) / 0.5), the tile encoding) frame-major [frames x 257], and on request
// the float dB values for the librosa comparison.
import PFFFT from "@pffft/pffft.js";
import wasmUrl from "@pffft/pffft.wasm?url";
import { fft as jsFft } from "fourier-transform";

const WIN = 400;
const HOP = 160;
const N = 512;
const BINS = N / 2 + 1;
const FLOOR = -120;
const STEP = 0.5;

export type StftRequest = { id: number; pcm: Float32Array; backend: "wasm" | "js"; wantFloat?: boolean };
export type StftResponse = { id: number; frames: number; u8: Uint8Array; db?: Float32Array; ms: number; backend: string };

const window512 = new Float32Array(N);
let winSum = 0;
for (let n = 0; n < WIN; n++) {
  const w = 0.5 - 0.5 * Math.cos((2 * Math.PI * n) / WIN);
  window512[(N - WIN) / 2 + n] = w;
  winSum += w;
}
const scale = 2 / winSum;

type Pffft = {
  _pffft_new_setup(n: number, kind: number): number;
  _pffft_aligned_malloc(bytes: number): number;
  _pffft_transform_ordered(setup: number, input: number, output: number, work: number, dir: number): void;
  HEAPF32: Float32Array;
};
let pffft: Promise<{ m: Pffft; setup: number; inp: number; out: number; work: number }> | undefined;
function loadPffft() {
  pffft ??= (PFFFT as (o: object) => Promise<Pffft>)({
    instantiateWasm(imports: WebAssembly.Imports, done: (i: WebAssembly.Instance) => void) {
      void WebAssembly.instantiateStreaming(fetch(wasmUrl), imports).then((r) => done(r.instance));
      return {};
    },
  }).then((m) => {
    const setup = m._pffft_new_setup(N, 0 /* PFFFT_REAL */);
    return { m, setup, inp: m._pffft_aligned_malloc(N * 4), out: m._pffft_aligned_malloc(N * 4), work: m._pffft_aligned_malloc(N * 4) };
  });
  return pffft;
}

function toDb(re: number, im: number): number {
  const mag = Math.sqrt(re * re + im * im) * scale;
  return 20 * Math.log10(mag > 1e-7 ? mag : 1e-7);
}

async function stft(req: StftRequest): Promise<StftResponse> {
  const { pcm } = req;
  const frames = 1 + Math.floor(pcm.length / HOP);
  const u8 = new Uint8Array(frames * BINS);
  const db = req.wantFloat ? new Float32Array(frames * BINS) : undefined;
  const frame = new Float32Array(N);
  const emit = (f: number, k: number, d: number) => {
    const q = Math.round((d - FLOOR) / STEP);
    u8[f * BINS + k] = q < 0 ? 0 : q > 255 ? 255 : q;
    if (db) db[f * BINS + k] = d;
  };
  const fill = (f: number) => {
    const c = f * HOP - N / 2; // centred frame start
    for (let n = 0; n < N; n++) {
      const i = c + n;
      frame[n] = i >= 0 && i < pcm.length ? pcm[i]! * window512[n]! : 0;
    }
  };
  const t0 = performance.now();
  if (req.backend === "wasm") {
    const p = await loadPffft();
    const t1 = performance.now();
    for (let f = 0; f < frames; f++) {
      fill(f);
      p.m.HEAPF32.set(frame, p.inp >> 2);
      p.m._pffft_transform_ordered(p.setup, p.inp, p.out, p.work, 0);
      const o = p.m.HEAPF32.subarray(p.out >> 2, (p.out >> 2) + N);
      // Ordered real output: [X0, X(N/2), re1, im1, re2, im2, ...]
      emit(f, 0, toDb(o[0]!, 0));
      emit(f, N / 2, toDb(o[1]!, 0));
      for (let k = 1; k < N / 2; k++) emit(f, k, toDb(o[2 * k]!, o[2 * k + 1]!));
    }
    return { id: req.id, frames, u8, db, ms: performance.now() - t1, backend: "wasm" };
  }
  const re = new Float64Array(BINS);
  const im = new Float64Array(BINS);
  const out: [Float64Array, Float64Array] = [re, im];
  for (let f = 0; f < frames; f++) {
    fill(f);
    jsFft(frame, out);
    for (let k = 0; k < BINS; k++) emit(f, k, toDb(re[k]!, im[k]!));
  }
  return { id: req.id, frames, u8, db, ms: performance.now() - t0, backend: "js" };
}

self.onmessage = (e: MessageEvent<StftRequest>) => {
  void stft(e.data).then((r) => {
    const transfer: Transferable[] = [r.u8.buffer];
    if (r.db) transfer.push(r.db.buffer);
    (self as unknown as DedicatedWorkerGlobalScope).postMessage(r, transfer);
  });
};
