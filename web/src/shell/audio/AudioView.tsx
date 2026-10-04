import { useQuery } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { wordsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AudioLink, UtteranceWords } from "@/api/gen/types.gen";
import { usePortalContainer } from "@/lib/portal";
import { useDefaults } from "@/shell/entity/defaults";
import { errorMessage } from "@/shell/panel/commands";
import { narrowbandCap, useTracks, type AnalysisData } from "./analysis";
import { useAudioAxis, type AudioAxis } from "./axis";
import { useAudioLink, usePeaks, useSpectrogram } from "./data";
import { AudioEngine, type SpecSettings } from "./engine";
import { audioDefaults, type AudioDefaults } from "./settings";
import type { TrackWord, WordTrackData } from "./words";
import "./audio.css";

// The React face of the audio view (R51): one utterance, its signed audio, peaks, spectrogram and word tracks against
// one AudioAxis. The engine is imperative; this component creates it in the document that holds the container and
// rebuilds it when the panel moves to another window (popout), so views always draw in their own window's frame loop.

export type AudioViewProps = {
  /**
   * Utterance id (utt_…) or the audio's content hash (b3:…); an annotation item (bit_…) or a triage item (tri_…) plays
   * its segment's window of the source file, every channel.
   */
  utterance: string;
  /** The channel to play (a call's caller or bot); the waveform dims the others. Default: every channel. */
  channel?: number;
  /** Show the level and voice activity lane (tracks.get); narrowband audio caps the spectrogram at 4 kHz. */
  analysis?: boolean;
  onAnalysis?: (a: AnalysisData | undefined) => void;
  /** A shared axis (Compare, Diff rows); default: the view's own. */
  axis?: AudioAxis;
  title?: string;
  compact?: boolean;
  /** Extra word tracks (hypotheses of other targets). */
  tracks?: WordTrackData[];
  /** Hypothesis and scores artifacts: the hypothesis word track, marked against the reference. */
  hypotheses?: string;
  scores?: string;
  /** BCP 47 language of the words. */
  lang?: string;
  /** Spectrogram settings over views.audio's defaults. */
  settings?: Partial<SpecSettings>;
  showSpectrogram?: boolean;
  /** Initial span: the view opens zoomed to it with the loop set. */
  span?: { start?: number; end?: number };
  channelLabels?: string[];
  onEngine?: (e: AudioEngine | null) => void;
  onSpan?: (start: number, end: number) => void;
  onLink?: (link: AudioLink | undefined) => void;
  onPlayState?: (playing: boolean) => void;
};

/** The hypothesis word track of a words.get answer. */
export function hypothesisTrack(w: UtteranceWords, label = "Hypothesis", lang?: string): WordTrackData {
  const words: TrackWord[] = w.words.map((x) => ({ word: x.word, start: x.start, end: x.end, confidence: x.confidence, op: x.op, ref: x.ref }));
  const end = words.length ? words[words.length - 1]!.end : 0;
  return {
    id: "hyp",
    label,
    lang,
    words,
    deletions: w.deletions.map((d) => ({ at: words[d.before]?.start ?? end, ref: d.ref })),
  };
}

export function useWords(utterance: string | undefined, hypotheses: string | undefined, scores: string | undefined) {
  return useQuery({
    ...wordsGetOptions({ path: { id: utterance ?? "" }, query: { hypotheses: hypotheses ?? "", ...(scores ? { scores } : {}) } }),
    enabled: !!utterance && !!hypotheses,
    staleTime: Infinity,
    retry: false,
  });
}

export function useAudioDefaults(): AudioDefaults | undefined {
  const { data } = useDefaults();
  return useMemo(() => audioDefaults(data), [data]);
}

export function AudioView(props: AudioViewProps) {
  const d = useAudioDefaults();
  const ownAxis = useAudioAxis(0);
  const axis = props.axis ?? ownAxis;
  const box = useRef<HTMLDivElement>(null);
  const [engine, setEngine] = useState<AudioEngine | null>(null);
  const [doc, setDoc] = useState<Document | null>(null);
  const link = useAudioLink(props.utterance, props.channel);
  const tracks = useTracks(props.utterance, !!props.analysis);
  const peaks = usePeaks(props.utterance);
  const showSpec = props.showSpectrogram ?? true;
  const spec = useSpectrogram(props.utterance, link.data, d, showSpec);
  const words = useWords(props.utterance, props.hypotheses, props.scores);
  const { onEngine, onSpan, onLink, onPlayState } = props;
  const cb = useRef({ onSpan, onEngine, onPlayState });
  useEffect(() => {
    cb.current = { onSpan, onEngine, onPlayState };
  });

  // The document the container lives in: the panel frame's portal container changes when the panel pops out or
  // returns (outside a panel it is undefined and the view stays in its first document).
  const portal = usePortalContainer();
  useLayoutEffect(() => {
    const el = box.current;
    if (el) setDoc((cur) => (cur === el.ownerDocument ? cur : el.ownerDocument));
  }, [portal]);

  const cap = narrowbandCap(tracks.data);
  const settings: SpecSettings | undefined = useMemo(() => {
    if (!d) return undefined;
    const s = { gainDb: d.gainDb, rangeDb: d.rangeDb, colormap: d.colormap, axis: d.axis, fmaxHz: d.fmaxHz, ...props.settings };
    // R52: audio of 8 kHz origin stops at 4 kHz (the bins above hold nothing but the resampler's floor).
    return cap ? { ...s, fmaxHz: Math.min(s.fmaxHz, cap) } : s;
  }, [d, props.settings, cap]);
  const title = props.title ?? `Audio of ${props.utterance}`;

  useLayoutEffect(() => {
    const el = box.current;
    if (!el || !doc || !d || !settings) return;
    const e = new AudioEngine(el, {
      axis,
      title,
      compact: props.compact,
      channelLabels: props.channelLabels,
      tileSlots: d.tileSlots,
      maxVisibleWords: d.wordsMaxVisible,
      settings,
      onSpan: (a, b) => cb.current.onSpan?.(a, b),
      onMediaError: () => void link.refetch(),
      onPlayState: (p) => cb.current.onPlayState?.(p),
    });
    setEngine(e);
    cb.current.onEngine?.(e);
    return () => {
      cb.current.onEngine?.(null);
      e.destroy();
      setEngine(null);
    };
    // Rebuilt only for a new window, axis or the defaults arriving; data flows in through the effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, d, axis]);

  useEffect(() => engine?.setSettings(settings ?? {}), [engine, settings]);
  useEffect(() => {
    if (!engine) return;
    if (link.data) {
      axis.setDuration(link.data.durationS);
      engine.setMedia(link.data.url);
    }
    onLink?.(link.data);
  }, [engine, link.data, axis, onLink]);
  const spanStart = props.span?.start;
  const spanEnd = props.span?.end;
  const duration = link.data?.durationS;
  useEffect(() => {
    if (!engine || !duration || (spanStart === undefined && spanEnd === undefined)) return;
    const a = spanStart ?? 0;
    const b = Math.min(duration, spanEnd ?? duration);
    if (!(b > a)) return;
    const pad = (b - a) * 0.15;
    axis.setRange(a - pad, b - a + 2 * pad);
    axis.setLoop(a, b);
    axis.setPlayhead(a);
  }, [engine, duration, spanStart, spanEnd, axis]);
  useEffect(() => engine?.setPeaks(peaks.data ?? null), [engine, peaks.data]);
  const { onAnalysis } = props;
  useEffect(() => {
    engine?.setAnalysis(props.analysis ? (tracks.data ?? null) : null);
    onAnalysis?.(tracks.data);
  }, [engine, tracks.data, props.analysis, onAnalysis]);
  const channel = props.channel;
  useEffect(() => engine?.setActiveChannel(channel ?? null), [engine, channel]);
  useEffect(() => {
    if (!engine) return;
    const note = !showSpec ? "" : spec.loading ? "Computing the spectrogram…" : spec.note;
    engine.setSpec(showSpec ? spec.source : null, note);
  }, [engine, spec.source, spec.note, spec.loading, showSpec]);
  const extra = props.tracks;
  const lang = props.lang;
  useEffect(() => {
    if (!engine) return;
    const list: WordTrackData[] = [];
    if (words.data) list.push(hypothesisTrack(words.data, "Hypothesis", lang));
    if (extra) list.push(...extra);
    engine.setWords(list);
  }, [engine, words.data, extra, lang]);

  const error = link.error ?? peaks.error;
  return (
    <div className="cadence-audio-host" data-slot="audio-view" data-utterance={props.utterance}>
      <div ref={box} className="cadence-audio-box" />
      {!d ? <p className="cadence-audio-status">Loading the audio view…</p> : null}
      {error ? (
        <p className="cadence-audio-status" role="alert">
          {errorMessage(error)}
        </p>
      ) : null}
    </div>
  );
}
