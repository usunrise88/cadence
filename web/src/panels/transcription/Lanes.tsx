import { useMemo, useState } from "react";
import { Copy } from "iconoir-react";
import type { LiveWord, TranscriptionLane } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { compareText, finalText, formatRate, OP_GLYPH, OP_LABEL, percentile, textDirection, type FinalSegment, type Lane, type LaneTarget, type LiveState } from "@/shell/panel";

// The lanes of a live session (R50 "Display"): grey partials replaced in place, solid finals with endpoint marks,
// confidence shading, timestamps on hover; Hebrew right to left with every word bidi-isolated (digits and Latin
// keep their order); live p50/p95 time to final and the real-time factor under the lanes. A typed reference gives a
// WER and a diff per lane, on the page only.

const ENDPOINT: Readonly<Record<FinalSegment["endpoint"], { glyph: string; label: string }>> = {
  eou: { glyph: "⏎", label: "end of utterance (the model's)" },
  finalize: { glyph: "⇥", label: "finalized" },
  end: { glyph: "■", label: "end of the session" },
};

/** Confidence shading: lower confidence reads fainter (and is named on hover, never colour alone). */
function shade(c: number | undefined): string {
  if (c === undefined) return "";
  if (c < 0.5) return "opacity-50";
  if (c < 0.8) return "opacity-75";
  return "";
}

function wordTitle(w: LiveWord): string {
  const t = w.start !== undefined && w.end !== undefined ? `${w.start.toFixed(2)}–${w.end.toFixed(2)} s` : "no timing";
  return w.confidence !== undefined ? `${t} · confidence ${w.confidence.toFixed(2)}` : t;
}

function FinalRun({ f, first }: { f: FinalSegment; first: boolean }) {
  const words = f.words.length ? f.words : f.text.split(/\s+/).filter(Boolean).map((w) => ({ word: w }) as LiveWord);
  const ep = ENDPOINT[f.endpoint];
  return (
    <>
      {!first && f.space ? " " : null}
      {words.map((w, i) => (
        <span key={i}>
          {i > 0 ? " " : null}
          <bdi className={cn("rounded-sm", shade(w.confidence))} title={wordTitle(w)}>
            {w.word}
          </bdi>
        </span>
      ))}
      {f.text ? (
        <span className="mx-0.5 text-[10px] text-muted-foreground" title={ep.label} aria-label={ep.label}>
          {ep.glyph}
        </span>
      ) : null}
    </>
  );
}

export function LaneText({ lane, language }: { lane: Lane | undefined; language: string }) {
  const finals = (lane?.finals ?? []).filter((f) => f.text);
  return (
    <p dir={textDirection(language)} lang={language} className="min-h-6 text-[14px] leading-6 [unicode-bidi:plaintext]" data-slot="transcription-lane-text">
      {finals.map((f, i) => (
        <FinalRun key={f.seq} f={f} first={i === 0} />
      ))}
      {lane?.partial?.text ? (
        <span className="text-muted-foreground" data-slot="transcription-partial">
          {finals.length && lane.partial.space ? " " : null}
          <bdi>{lane.partial.text}</bdi>
        </span>
      ) : null}
    </p>
  );
}

function ReferenceDiff({ reference, text, language }: { reference: string; text: string; language: string }) {
  const c = useMemo(() => compareText(reference, text), [reference, text]);
  return (
    <div className="mt-1 text-xs" data-slot="transcription-diff">
      <span className="font-medium tabular-nums">WER {formatRate(c.wer)}</span>
      <span className="ml-2 text-muted-foreground tabular-nums">
        {c.sub} S · {c.del} D · {c.ins} I of {c.refWords} words
      </span>
      <p dir={textDirection(language)} lang={language} className="mt-0.5 leading-6 [unicode-bidi:plaintext]">
        {c.words.map((w, i) => (
          <span key={i}>
            {i > 0 ? " " : null}
            <bdi
              className={cn(w.op === "S" && "underline decoration-wavy", w.op === "D" && "text-muted-foreground line-through", w.op === "I" && "underline")}
              title={w.op === "=" ? "match" : w.op === "S" ? `${OP_LABEL.S}: “${w.ref}” → “${w.hyp}”` : OP_LABEL[w.op]}
            >
              {OP_GLYPH[w.op]}
              {w.op === "D" ? w.ref : w.hyp}
            </bdi>
          </span>
        ))}
      </p>
    </div>
  );
}

function ms(v: number | undefined): string {
  return v === undefined ? "—" : `${Math.round(v)} ms`;
}

export function LiveStats({ state }: { state: LiveState }) {
  return (
    <p className="flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-muted-foreground tabular-nums" data-slot="transcription-stats" aria-live="off">
      <span title="Wall time from sending the audio up to a final's end to receiving the final">
        Time to final p50 {ms(percentile(state.latencies, 50))} · p95 {ms(percentile(state.latencies, 95))}
      </span>
      {state.finalizeLatencies.length ? (
        <span title="From pressing Finalize to each target's final">
          Finalize → final p50 {ms(percentile(state.finalizeLatencies, 50))} · p95 {ms(percentile(state.finalizeLatencies, 95))}
        </span>
      ) : null}
      <span title="Decode time over audio time, every target together (the worker's)">RTF {state.rtf === undefined ? "—" : state.rtf.toFixed(3)}</span>
      {state.audioS !== undefined ? <span>{state.audioS.toFixed(1)} s of audio</span> : null}
      {state.rtt.relay !== undefined || state.rtt.worker !== undefined ? (
        <span title="Round trips: to the control plane's relay (ping) and to the worker behind any queued audio (keepalive)">
          RTT relay {ms(state.rtt.relay)} · worker {ms(state.rtt.worker)}
        </span>
      ) : null}
    </p>
  );
}

export function Lanes({ lanes, state, blind, reference }: { lanes: TranscriptionLane[]; state: LiveState; blind: boolean; reference: string }) {
  const [picked, setPicked] = useState<LaneTarget | null>(null);
  const [copied, setCopied] = useState<LaneTarget | null>(null);
  const hidden = blind && picked === null;
  const copy = async (t: LaneTarget, text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(t);
    } catch {
      setCopied(null);
    }
  };
  return (
    <div className="flex flex-col gap-2" data-slot="transcription-lanes">
      {lanes.map((l) => {
        const lane = state.lanes[l.target];
        const text = finalText(lane);
        const started = state.started?.targets.find((t) => t.target === l.target);
        return (
          <section key={l.target} className={cn("rounded-md border p-2", picked === l.target && "border-primary")} aria-label={`Lane ${l.target}`} data-slot="transcription-lane">
            <div className="mb-1 flex flex-wrap items-center gap-2 text-xs">
              <span className="font-medium">Lane {l.target}</span>
              {hidden ? (
                <span className="text-muted-foreground">hidden until you pick the better lane</span>
              ) : (
                <span className="text-muted-foreground">
                  {l.label} · {l.profile} · {l.language}
                  {l.boost ? ` · boost ${l.boost.list.split("/").pop()} ×${l.boost.weight}` : ""}
                  {started ? ` · loaded in ${started.loadS.toFixed(1)} s` : ""}
                </span>
              )}
              <span className="ml-auto flex items-center gap-1">
                {blind && picked === null ? (
                  <Button size="xs" variant="outline" disabled={!text} onClick={() => setPicked(l.target)}>
                    This one is better
                  </Button>
                ) : null}
                {picked === l.target ? <span className="text-xs text-primary">your pick</span> : null}
                <Button size="icon-xs" variant="ghost" aria-label={`Copy lane ${l.target}'s text`} disabled={!text} onClick={() => void copy(l.target, text)}>
                  <Copy aria-hidden />
                </Button>
                {copied === l.target ? <span className="text-[11px] text-muted-foreground">copied</span> : null}
              </span>
            </div>
            <LaneText lane={lane} language={l.language} />
            {reference.trim() && text ? <ReferenceDiff reference={reference} text={text} language={l.language} /> : null}
          </section>
        );
      })}
      <LiveStats state={state} />
    </div>
  );
}
