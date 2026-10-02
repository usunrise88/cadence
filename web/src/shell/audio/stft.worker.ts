// The browser STFT runs here, off the main thread (R52; S5: ≈ 45 ms per minute of 16 kHz audio).
import { stftU8, type StftParams } from "./stft";

export type StftRequest = { id: number; pcm: Float32Array; params: StftParams };
export type StftResponse = { id: number; frames: number; u8: Uint8Array; ms: number };

type WorkerScope = {
  onmessage: ((e: MessageEvent<StftRequest>) => void) | null;
  postMessage(message: StftResponse, transfer: Transferable[]): void;
};

const scope = self as unknown as WorkerScope;
scope.onmessage = (e) => {
  const t0 = performance.now();
  const r = stftU8(e.data.pcm, e.data.params);
  scope.postMessage({ id: e.data.id, frames: r.frames, u8: r.u8, ms: performance.now() - t0 }, [r.u8.buffer]);
};
