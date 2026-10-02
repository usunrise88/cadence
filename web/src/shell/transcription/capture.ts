import type { PcmFrame } from "./pcm";

// Microphone capture for the live channel (R50): getUserMedia with echo cancellation, noise suppression and automatic
// gain off by default ("raw microphone"; a toggle turns them on to hear what a call stack does), an AudioWorklet at
// the device rate (the AudioContext's own rate: no resampling in the browser), channel 0, PCM16 frames of frameMs.
// MediaRecorder's lossy formats are never used. A secure context is required (HTTPS through Caddy, or localhost).

export type CaptureOptions = {
  deviceId?: string;
  /** Echo cancellation, noise suppression and automatic gain off (default true). */
  raw?: boolean;
  frameMs: number;
  onFrame: (f: PcmFrame) => void;
};

export type Capture = {
  sampleRate: number;
  /** The track's getSettings(): what the browser actually applied. */
  settings: Record<string, unknown>;
  label: string;
  stop: () => void;
};

/** Why live capture cannot run on this page, or undefined when it can. */
export function captureUnavailable(win: Window & typeof globalThis = window): string | undefined {
  if (!win.isSecureContext) return "The microphone needs a secure context: open Cadence over HTTPS (through Caddy) or on localhost. Files and utterance spans still work.";
  if (!win.navigator.mediaDevices?.getUserMedia) return "This browser offers no microphone access (navigator.mediaDevices). Files and utterance spans still work.";
  if (typeof win.AudioWorkletNode === "undefined") return "This browser has no AudioWorklet, which capture needs. Files and utterance spans still work.";
  return undefined;
}

/** The audio inputs the browser lists (labels appear once a permission was granted). */
export async function audioInputs(win: Window & typeof globalThis = window): Promise<{ deviceId: string; label: string }[]> {
  const md = win.navigator.mediaDevices;
  if (!md?.enumerateDevices) return [];
  const list = await md.enumerateDevices();
  return list.filter((d) => d.kind === "audioinput").map((d, i) => ({ deviceId: d.deviceId, label: d.label || `Microphone ${i + 1}` }));
}

/** The getUserMedia constraints for a device and the raw setting. */
export function captureConstraints(deviceId: string | undefined, raw: boolean): MediaStreamConstraints {
  return {
    audio: {
      echoCancellation: !raw,
      noiseSuppression: !raw,
      autoGainControl: !raw,
      channelCount: { ideal: 1 },
      ...(deviceId ? { deviceId: { exact: deviceId } } : {}),
    },
  };
}

/**
 * Starts the microphone. Call it inside the click that starts the session: Safari keeps a context created outside a
 * gesture suspended, so the context is resumed here too.
 */
export async function startCapture(o: CaptureOptions, win: Window & typeof globalThis = window): Promise<Capture> {
  const why = captureUnavailable(win);
  if (why) throw new Error(why);
  // The context is created and resumed while the click's activation still holds (before the permission prompt).
  const ctx = new win.AudioContext();
  const resumed = ctx.resume();
  let stream: MediaStream;
  try {
    stream = await win.navigator.mediaDevices.getUserMedia(captureConstraints(o.deviceId, o.raw ?? true));
  } catch (e) {
    void ctx.close();
    throw e;
  }
  const track = stream.getAudioTracks()[0];
  try {
    await resumed;
    // The worklet is bundled as its own module (Vite's worker build) and loaded only when capture starts.
    const { default: workletUrl } = await import("./capture.worklet.ts?worker&url");
    await ctx.audioWorklet.addModule(workletUrl);
    const src = ctx.createMediaStreamSource(stream);
    const node = new win.AudioWorkletNode(ctx, "cadence-capture", {
      numberOfInputs: 1,
      numberOfOutputs: 0,
      // "max": the processor sees the track's own channels and takes channel 0 (an explicit mono input would mix a
      // stereo track's channels down instead).
      channelCountMode: "max",
      processorOptions: { frameMs: o.frameMs },
    });
    node.port.onmessage = (ev: MessageEvent<PcmFrame>) => o.onFrame(ev.data);
    src.connect(node);
    const c = ctx;
    return {
      sampleRate: c.sampleRate,
      settings: (track?.getSettings() ?? {}) as Record<string, unknown>,
      label: track?.label ?? "",
      stop: () => {
        node.port.onmessage = null;
        src.disconnect();
        node.disconnect();
        stream.getTracks().forEach((t) => t.stop());
        void c.close();
      },
    };
  } catch (e) {
    stream.getTracks().forEach((t) => t.stop());
    void ctx.close();
    throw e;
  }
}
