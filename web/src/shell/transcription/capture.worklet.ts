// The microphone capture processor (R50), loaded into the AudioContext's worklet scope by capture.ts: channel 0 of
// its input at the context's (device) rate, framed as PCM16 by Framer and posted to the page with each frame's peak
// and clipped-sample count. No resampling and no processing here: the worker resamples with the training resampler.
import { Framer, frameSamples } from "./pcm";

// The AudioWorkletGlobalScope names TypeScript's DOM library does not declare.
declare const sampleRate: number;
declare class AudioWorkletProcessor {
  readonly port: MessagePort;
  constructor(options?: { processorOptions?: unknown });
}
declare function registerProcessor(name: string, ctor: new (options: { processorOptions?: unknown }) => AudioWorkletProcessor): void;

class CaptureProcessor extends AudioWorkletProcessor {
  private framer: Framer;

  constructor(options: { processorOptions?: unknown }) {
    super(options);
    const frameMs = Number((options.processorOptions as { frameMs?: number } | undefined)?.frameMs ?? 20);
    this.framer = new Framer(frameSamples(sampleRate, frameMs));
  }

  process(inputs: Float32Array[][]): boolean {
    // Channel 0 only: Safari gives a stereo track with the audio on the left when echo cancellation is off.
    const ch = inputs[0]?.[0];
    if (!ch) return true;
    for (const f of this.framer.push(ch)) this.port.postMessage(f, [f.pcm]);
    return true;
  }
}

registerProcessor("cadence-capture", CaptureProcessor);
