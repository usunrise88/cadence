import { describe, expect, it } from "vitest";
import { startCapture } from "./capture";
import type { PcmFrame } from "./pcm";

// Capture in real Chromium with its fake microphone (a tone, vite.config.ts): the worklet must receive the track's
// audio and post non-silent PCM16 frames of frameMs, which the level meter and the live channel read.
describe("startCapture", () => {
  it("posts frames with signal from the microphone", async () => {
    const frames: PcmFrame[] = [];
    const cap = await startCapture({ frameMs: 20, onFrame: (f) => frames.push(f) });
    try {
      const deadline = performance.now() + 5000;
      while (performance.now() < deadline && !frames.some((f) => f.peak > 0.01)) await new Promise((r) => setTimeout(r, 50));
      expect(frames.length).toBeGreaterThan(0);
      expect(Math.max(...frames.map((f) => f.peak))).toBeGreaterThan(0.01);
      expect(frames[0]!.pcm.byteLength).toBe(Math.round(cap.sampleRate * 0.02) * 2);
    } finally {
      cap.stop();
    }
  });
});
