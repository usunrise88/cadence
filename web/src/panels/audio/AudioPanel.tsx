import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChatBubble, Download, Pause, Play, Repeat, ZoomIn, ZoomOut } from "iconoir-react";
import { evalsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AudioLink } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import {
  AudioView,
  COLORMAPS,
  formatTime,
  spanReference,
  toCtm,
  toTextGrid,
  toWebVtt,
  useAudioAxis,
  useAudioDefaults,
  useAudioItem,
  useAxisState,
  useWords,
  type AudioEngine,
  type AudioTarget,
  type Colormap,
  type ExportWord,
  type SpecSettings,
} from "@/shell/audio";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { attachToChat, evalIdOfDoc, useFollowedDoc, type PanelProps } from "@/shell/panel";

// Audio (docs/spec/11-ui-panels.md "Panel catalogue", Audio; R51, R52): the audio view of the span last opened from an
// Eval report row, Diff or a reference — waveform, spectrogram and the hypothesis word track marked S/I with
// deletions — played through a short-lived signed link. Play, loop a span, change speed, zoom, spectrogram colormap,
// axis and range, attach the span to Chat, export the words as TextGrid, CTM or WebVTT. Floating by default.

const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2];

export function AudioEmpty() {
  return <EmptyState step="review" title="No audio selected" hint="Open a row of an Eval report's utterance table, or Diff's audio, to hear it here." />;
}

export function AudioPanel({ instanceId }: PanelProps) {
  const target = useAudioItem();
  const { doc } = useFollowedDoc(instanceId);
  if (!target) return <AudioEmpty />;
  return <AudioTargetView key={`${target.utterance}#${target.start ?? ""},${target.end ?? ""}`} target={target} evalDoc={doc ?? undefined} />;
}

/** The hypothesis and scores artifacts of a target: given directly, or the eval cell's in the followed Eval report. */
function useArtifacts(target: AudioTarget, evalDoc: string | undefined): { hypotheses?: string; scores?: string } {
  const evalId = target.cell ? evalIdOfDoc(evalDoc) : undefined;
  const q = useQuery({ ...evalsGetOptions({ path: { id: evalId ?? "" } }), enabled: !!evalId && !target.hypotheses });
  if (target.hypotheses) return { hypotheses: target.hypotheses, scores: target.scores };
  const cell = q.data?.cells?.find((c) => c.id === target.cell);
  return { hypotheses: cell?.hypotheses, scores: cell?.scores };
}

function download(el: HTMLElement, name: string, text: string, type: string) {
  const d = el.ownerDocument;
  const win = d.defaultView ?? window;
  const url = win.URL.createObjectURL(new win.Blob([text], { type }));
  const a = d.createElement("a");
  a.href = url;
  a.download = name;
  d.body.append(a);
  a.click();
  a.remove();
  win.setTimeout(() => win.URL.revokeObjectURL(url), 1000);
}

function AudioTargetView({ target, evalDoc }: { target: AudioTarget; evalDoc?: string }) {
  const d = useAudioDefaults();
  const axis = useAudioAxis(0);
  const state = useAxisState(axis);
  const [engine, setEngine] = useState<AudioEngine | null>(null);
  const [link, setLink] = useState<AudioLink | undefined>();
  const [speed, setSpeed] = useState(1);
  const [showSpec, setShowSpec] = useState(true);
  const [over, setOver] = useState<Partial<SpecSettings>>({});
  const [playing, setPlaying] = useState(false);
  const [hover, setHover] = useState(-1);
  const art = useArtifacts(target, evalDoc);
  const words = useWords(target.utterance, art.hypotheses, art.scores);
  const settings = useMemo(() => over, [over]);
  const span = useMemo(() => ({ start: target.start, end: target.end }), [target.start, target.end]);

  const exportWords: ExportWord[] = useMemo(() => (words.data?.words ?? []).map((w) => ({ word: w.word, start: w.start, end: w.end, confidence: w.confidence })), [words.data]);
  const utteranceId = link?.utteranceId ?? (target.utterance.startsWith("utt_") ? target.utterance : undefined);
  const base = utteranceId ?? "audio";
  const loop = state.loop;
  const ref = utteranceId ? spanReference({ utterance: utteranceId, start: loop?.[0], end: loop?.[1] }) : undefined;

  const togglePlay = () => engine?.togglePlay();

  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="audio-panel">
      <PanelToolbar className="flex-wrap gap-1">
        <Button size="icon-xs" variant="ghost" aria-label={playing ? "Pause (Space)" : "Play (Space)"} onClick={togglePlay} disabled={!engine || !link}>
          {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
        </Button>
        <span className="text-xs tabular-nums text-muted-foreground" aria-label="Playhead and duration">
          {formatTime(state.playhead, state.span)} / {formatTime(state.duration, 100)}
        </span>
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          Speed
          <NativeSelect
            className="h-6 w-16"
            value={speed}
            onChange={(e) => {
              const r = Number(e.target.value);
              setSpeed(r);
              engine?.setRate(r);
            }}
          >
            {SPEEDS.map((s) => (
              <option key={s} value={s}>
                {s}×
              </option>
            ))}
          </NativeSelect>
        </label>
        <Button size="icon-xs" variant="ghost" aria-label="Zoom in (=)" onClick={() => engine?.zoomBy(0.5)} disabled={!engine}>
          <ZoomIn aria-hidden />
        </Button>
        <Button size="icon-xs" variant="ghost" aria-label="Zoom out (−)" onClick={() => engine?.zoomBy(2)} disabled={!engine}>
          <ZoomOut aria-hidden />
        </Button>
        <Button size="xs" variant="ghost" aria-pressed={!!loop} onClick={() => axis.setLoop(null)} disabled={!loop} title="Drag across the waveform to loop a span; [ and ] set its ends">
          <Repeat aria-hidden />
          {loop ? `${formatTime(loop[0], 1)}–${formatTime(loop[1], 1)}` : "No loop"}
        </Button>
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        <label className="flex items-center gap-1 text-xs text-muted-foreground">
          <input type="checkbox" className="size-4 accent-primary" checked={showSpec} onChange={(e) => setShowSpec(e.target.checked)} />
          Spectrogram
        </label>
        {showSpec && d ? (
          <>
            <NativeSelect className="h-6 w-24" aria-label="Colormap" value={over.colormap ?? d.colormap} onChange={(e) => setOver((o) => ({ ...o, colormap: e.target.value as Colormap }))}>
              {COLORMAPS.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </NativeSelect>
            <NativeSelect className="h-6 w-16" aria-label="Frequency axis" value={over.axis ?? d.axis} onChange={(e) => setOver((o) => ({ ...o, axis: e.target.value as "mel" | "hz" }))}>
              <option value="mel">mel</option>
              <option value="hz">Hz</option>
            </NativeSelect>
            <NativeSelect className="h-6 w-20" aria-label="Dynamic range" value={over.rangeDb ?? d.rangeDb} onChange={(e) => setOver((o) => ({ ...o, rangeDb: Number(e.target.value) }))}>
              {[50, 60, 70, 80, 100].map((r) => (
                <option key={r} value={r}>
                  {r} dB
                </option>
              ))}
            </NativeSelect>
            <NativeSelect className="h-6 w-20" aria-label="Gain" value={over.gainDb ?? d.gainDb} onChange={(e) => setOver((o) => ({ ...o, gainDb: Number(e.target.value) }))}>
              {[-12, -6, 0, 6, 12, 18].map((g) => (
                <option key={g} value={g}>
                  {g > 0 ? `+${g}` : g} dB
                </option>
              ))}
            </NativeSelect>
          </>
        ) : null}
        <span className="ml-auto flex items-center gap-1">
          <Button size="xs" variant="ghost" disabled={!ref} title={ref ? ref.ref : "The utterance id is not known yet"} onClick={() => ref && attachToChat([{ ref: ref.ref, label: ref.label }])}>
            <ChatBubble aria-hidden />
            Attach to Chat
          </Button>
          <NativeSelect
            className="h-6 w-28"
            aria-label="Export words"
            value=""
            disabled={exportWords.length === 0}
            onChange={(e) => {
              const f = e.target.value;
              e.target.value = "";
              if (f === "textgrid") download(e.target, `${base}.TextGrid`, toTextGrid(exportWords, state.duration), "text/plain");
              else if (f === "ctm") download(e.target, `${base}.ctm`, toCtm(exportWords, base), "text/plain");
              else if (f === "vtt") download(e.target, `${base}.vtt`, toWebVtt(exportWords), "text/vtt");
            }}
          >
            <option value="">Export…</option>
            <option value="textgrid">TextGrid</option>
            <option value="ctm">CTM</option>
            <option value="vtt">WebVTT</option>
          </NativeSelect>
          <Download aria-hidden className="size-4 text-muted-foreground" />
        </span>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto p-2">
        <AudioView
          utterance={target.utterance}
          axis={axis}
          title={`Audio of ${utteranceId ?? target.utterance.slice(0, 12)}`}
          hypotheses={art.hypotheses}
          scores={art.scores}
          settings={settings}
          showSpectrogram={showSpec}
          span={span}
          onEngine={setEngine}
          onLink={setLink}
          onPlayState={setPlaying}
        />
        {words.data ? (
          <p dir="auto" className="mt-2 flex flex-wrap gap-x-1 text-[13px] leading-6" aria-label="Hypothesis transcript; a word moves the playhead" data-slot="audio-transcript">
            {words.data.words.map((w, i) => (
              <button
                key={i}
                type="button"
                className={cn("rounded px-0.5 hover:bg-hover", hover === i && "bg-selected", w.op === "S" && "underline decoration-wavy", w.op === "I" && "underline")}
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover(-1)}
                onClick={() => engine?.seek(w.start)}
                title={`${w.start.toFixed(2)}–${w.end.toFixed(2)} s${w.op === "S" && w.ref ? ` · substituted for “${w.ref}”` : w.op === "I" ? " · inserted" : ""}`}
              >
                <bdi>{w.word}</bdi>
              </button>
            ))}
          </p>
        ) : art.hypotheses && words.isError ? (
          <p className="mt-2 text-xs text-muted-foreground">No words for this utterance in the cell's hypotheses.</p>
        ) : null}
      </div>
    </div>
  );
}
