import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { usePortalContainer } from "@/lib/portal";
import { useAudioDefaults } from "./AudioView";
import { useAudioAxis, type AudioAxis } from "./axis";
import { computeStft, to16k } from "./data";
import { AudioEngine, type SpecSettings } from "./engine";
import { peaksFromPcm } from "./peaks";
import { stftShape } from "./settings";
import { MemorySource, type SpecSource } from "./tiles";
import { encodeWav16 } from "./wav";
import type { WordTrackData } from "./words";

// The audio view of audio the page holds itself (R51): a live session's microphone recording or a chosen file, shown
// after the session with every target's words. Nothing is uploaded or kept: the samples live in the page's memory,
// play from a blob URL that is revoked with the view, and the waveform and spectrogram are computed here.

export type LocalAudioViewProps = {
  /** Mono samples (channel 0) at sampleRate. */
  samples: Float32Array;
  sampleRate: number;
  /** A URL to play instead of the samples encoded as WAV (a chosen file's object URL). */
  mediaUrl?: string;
  tracks?: WordTrackData[];
  title: string;
  axis?: AudioAxis;
  settings?: Partial<SpecSettings>;
  showSpectrogram?: boolean;
  onEngine?: (e: AudioEngine | null) => void;
  onPlayState?: (playing: boolean) => void;
};

/** Decodes a file the person chose into channel 0 (the browser's decoders; for the page's view only). */
export async function decodeAudioFile(data: ArrayBuffer, win: Window & typeof globalThis = window): Promise<{ samples: Float32Array; sampleRate: number }> {
  const ctx = new win.AudioContext();
  try {
    const buf = await ctx.decodeAudioData(data.slice(0));
    return { samples: buf.getChannelData(0).slice(), sampleRate: buf.sampleRate };
  } finally {
    void ctx.close();
  }
}

export function LocalAudioView(props: LocalAudioViewProps) {
  const d = useAudioDefaults();
  const ownAxis = useAudioAxis(0);
  const axis = props.axis ?? ownAxis;
  const box = useRef<HTMLDivElement>(null);
  const [engine, setEngine] = useState<AudioEngine | null>(null);
  const [doc, setDoc] = useState<Document | null>(null);
  const [spec, setSpec] = useState<{ source: SpecSource | null; note: string }>({ source: null, note: "" });
  const cb = useRef({ onEngine: props.onEngine, onPlayState: props.onPlayState });
  useEffect(() => {
    cb.current = { onEngine: props.onEngine, onPlayState: props.onPlayState };
  });
  const portal = usePortalContainer();
  useLayoutEffect(() => {
    const el = box.current;
    if (el) setDoc((cur) => (cur === el.ownerDocument ? cur : el.ownerDocument));
  }, [portal]);
  const settings: SpecSettings | undefined = useMemo(
    () => (d ? { gainDb: d.gainDb, rangeDb: d.rangeDb, colormap: d.colormap, axis: d.axis, fmaxHz: d.fmaxHz, ...props.settings } : undefined),
    [d, props.settings],
  );
  const { samples, sampleRate, mediaUrl, title } = props;
  const duration = samples.length / Math.max(1, sampleRate);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el || !doc || !d || !settings) return;
    const e = new AudioEngine(el, {
      axis,
      title,
      tileSlots: d.tileSlots,
      maxVisibleWords: d.wordsMaxVisible,
      settings,
      onPlayState: (p) => cb.current.onPlayState?.(p),
    });
    setEngine(e);
    cb.current.onEngine?.(e);
    return () => {
      cb.current.onEngine?.(null);
      e.destroy();
      setEngine(null);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, d, axis]);

  useEffect(() => engine?.setSettings(settings ?? {}), [engine, settings]);
  useEffect(() => {
    if (!engine) return;
    axis.setDuration(duration);
    const url = mediaUrl ?? URL.createObjectURL(new Blob([encodeWav16(samples, sampleRate)], { type: "audio/wav" }));
    engine.setMedia(url);
    engine.setPeaks(peaksFromPcm([samples], sampleRate));
    return () => {
      engine.setMedia(null);
      if (!mediaUrl) URL.revokeObjectURL(url);
    };
  }, [engine, samples, sampleRate, mediaUrl, duration, axis]);

  const showSpec = props.showSpectrogram ?? true;
  useEffect(() => {
    if (!showSpec || !d) return;
    if (duration > d.browserStftMaxS) {
      setSpec({ source: null, note: `No spectrogram for audio longer than ${Math.round(d.browserStftMaxS)} s on the page.` });
      return;
    }
    let cancelled = false;
    const shape = stftShape(d, sampleRate);
    void computeStft(to16k(samples, sampleRate).slice(), { win: shape.win, hop: shape.hop, nFft: shape.nFft, bins: shape.bins }).then((c) => {
      if (cancelled) return;
      setSpec({ source: new MemorySource(`local:${title}`, [{ frameMajor: c.u8, frames: c.frames }], shape.bins, shape.binHz, shape.hop / 16000, shape.fmaxHz), note: "" });
    });
    return () => {
      cancelled = true;
    };
  }, [samples, sampleRate, d, duration, showSpec, title]);
  useEffect(() => engine?.setSpec(showSpec ? spec.source : null, showSpec ? spec.note : ""), [engine, spec, showSpec]);
  const tracks = props.tracks;
  useEffect(() => engine?.setWords(tracks ?? []), [engine, tracks]);

  return (
    <div className="cadence-audio-host" data-slot="audio-view" data-local="true">
      <div ref={box} className="cadence-audio-box" />
      {!d ? <p className="cadence-audio-status">Loading the audio view…</p> : null}
    </div>
  );
}
