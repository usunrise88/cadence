import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Forward, Pause, Play, Restart, SoundHigh, WarningCircle } from "iconoir-react";
import { authGetOptions, batchItemsListOptions, batchItemsListQueryKey, batchesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AnnotationStatus, BatchItem } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { AudioView, type AnalysisData, type AudioEngine } from "@/shell/audio";
import { errorMessage, runCommand } from "@/shell/panel/commands";
import { GuidelinesPane } from "./Guidelines";
import { channelCycle, channelLabel, ENTITY_CLASSES, formOf, KEYS, keepSpans, seconds, segmentSpan, shownItem, spanOf, TAGS, toggleTag, type AnnotationForm } from "./model";

// The Annotate mode (docs/spec/04-blocks.md "Annotation workflow" step 3; docs/spec/11-ui-panels.md Triage queue): one
// batch item at a time — its window of the call with a channel switch (the target first, then the other party, then
// both), the level and voice activity lanes, the other party's turns, a transcript prefilled with the best machine
// hypothesis, tags and entity spans — and done, flag or skip, keyboard-first. Blind: the server shows a reviewer
// only their own annotations. The Triage panel and the reviewer's page both compose it.

export type AnnotateViewProps = {
  batchId: string;
  /** Hide the item list beside the form (the reviewer's page shows it). */
  compact?: boolean;
};

export function AnnotateView({ batchId, compact }: AnnotateViewProps) {
  const qc = useQueryClient();
  const auth = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const userId = auth.data?.actor?.id;
  const listOpts = batchItemsListOptions({ path: { id: batchId }, query: { queue: "mine" } });
  const list = useQuery({ ...listOpts, refetchOnWindowFocus: false });
  const [current, setCurrent] = useState<string | undefined>();
  const items = useMemo(() => list.data?.items ?? [], [list.data]);
  const item = shownItem(items, list.data?.next, current);
  const itemId = item?.id;
  const mine = items.filter((i) => i.annotations.some((a) => a.annotator.id === userId));
  const open = items.length - mine.length;

  const advance = async () => {
    setCurrent(undefined);
    await qc.invalidateQueries({ queryKey: batchItemsListQueryKey({ path: { id: batchId }, query: { queue: "mine" } }) });
    void qc.invalidateQueries({ queryKey: batchesGetQueryKey({ path: { id: batchId } }) });
  };

  if (list.isLoading) return <p className="p-4 text-xs text-muted-foreground">Loading your queue…</p>;
  if (list.error) return <p role="alert" className="p-4 text-xs text-destructive">{errorMessage(list.error)}</p>;
  return (
    <div className="flex h-full min-h-0" data-slot="annotate-view" data-batch={batchId}>
      {!compact ? (
        <nav aria-label="Your queue" className="flex w-56 shrink-0 flex-col overflow-auto border-r text-xs">
          <p className="px-2 py-1.5 text-muted-foreground">
            {open} to annotate · {mine.length} done by you
          </p>
          <ul>
            {items.map((it) => {
              const done = it.annotations.some((a) => a.annotator.id === userId);
              return (
                <li key={it.id}>
                  <button
                    type="button"
                    className={cn("flex w-full items-center gap-2 px-2 py-1 text-left hover:bg-hover", it.id === itemId && "bg-selected")}
                    onClick={() => setCurrent(it.id)}
                    aria-current={it.id === itemId ? "true" : undefined}
                  >
                    <span className="w-8 tabular-nums text-muted-foreground">#{it.position}</span>
                    <span className="flex-1 truncate">{seconds(it.segment.duration)}</span>
                    {it.double ? <span title="Annotated twice, blind">×2</span> : null}
                    <span className={cn("text-[10px]", done ? "text-muted-foreground" : "font-medium")}>{done ? "done" : "open"}</span>
                  </button>
                </li>
              );
            })}
          </ul>
        </nav>
      ) : null}
      <div className="min-w-0 flex-1 overflow-auto">
        <GuidelinesPane batchId={batchId} />
        {item ? (
          <ItemForm key={item.id} batchId={batchId} item={item} userId={userId} onDone={advance} />
        ) : (
          <p className="p-6 text-center text-xs text-muted-foreground">
            Nothing left to annotate in this batch. {mine.length ? "Your annotations are listed on the left." : ""}
          </p>
        )}
      </div>
    </div>
  );
}

function ItemForm({ batchId, item, userId, onDone }: { batchId: string; item: BatchItem; userId?: string; onDone: () => Promise<void> }) {
  const [form, setForm] = useState<AnnotationForm>(() => formOf(item, userId));
  const [cls, setCls] = useState<string>("name");
  const cycle = useMemo(() => channelCycle(item), [item]);
  const [chIdx, setChIdx] = useState(0);
  const channel = cycle[chIdx % cycle.length];
  const [engine, setEngine] = useState<AudioEngine | null>(null);
  const [playing, setPlaying] = useState(false);
  const [analysis, setAnalysis] = useState<AnalysisData | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const text = useRef<HTMLTextAreaElement>(null);
  const span = useMemo(() => segmentSpan(item), [item]);
  const mine = item.annotations.find((a) => a.annotator.id === userId);
  const labels = useMemo(() => Array.from({ length: Math.max(1, item.window.channels) }, (_, i) => item.window.roles?.[i] ?? `channel ${i}`), [item]);

  useEffect(() => {
    text.current?.focus();
  }, []);

  const submit = async (status: AnnotationStatus) => {
    setBusy(true);
    setError(undefined);
    try {
      const body =
        status === "skipped"
          ? { status }
          : { status, text: form.text.trim(), tags: form.tags, entities: form.entities.map(({ start, end, class: c }) => ({ start, end, class: c })) };
      await runCommand("annotations.new", { batch: batchId, item: item.id, body });
      await onDone();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const markEntity = () => {
    const el = text.current;
    if (!el) return;
    const s = spanOf(form.text, el.selectionStart, el.selectionEnd, cls);
    if (s) setForm((f) => ({ ...f, entities: [...f.entities.filter((e) => e.end <= s.start || e.start >= s.end), s].sort((a, b) => a.start - b.start) }));
  };

  const onKey = (e: KeyboardEvent<HTMLElement>) => {
    const k = e.key.toLowerCase();
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      void submit(e.shiftKey ? "flagged" : "done");
    } else if (e.altKey && !e.ctrlKey && !e.metaKey) {
      const tag = TAGS.find((t) => t.key === e.key);
      if (k === "s") void submit("skipped");
      else if (k === "p") engine?.togglePlay();
      else if (k === "r") {
        engine?.seek(span.start);
        if (!playing) engine?.togglePlay();
      } else if (k === "c") setChIdx((i) => i + 1);
      else if (k === "e") markEntity();
      else if (tag) setForm((f) => ({ ...f, tags: toggleTag(f.tags, tag.tag) }));
      else return;
      e.preventDefault();
    }
  };

  const bot = analysis?.eou;
  return (
    <div className="flex flex-col gap-3 p-3 text-xs" onKeyDown={onKey} data-slot="annotate-item" data-item={item.id}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">Item {item.position}</span>
        <span className="text-muted-foreground">
          {item.segment.role} · {seconds(item.segment.duration)} · {item.segment.language ?? ""}
          {item.double ? " · annotated twice, blind" : ""}
        </span>
        {mine ? <span className="rounded bg-accent-soft px-1.5 text-accent-text">your {mine.status} annotation</span> : null}
        <span className="ml-auto flex items-center gap-1">
          <Button size="icon-xs" variant="ghost" aria-label={`${playing ? "Pause" : "Play"} (${KEYS.play})`} onClick={() => engine?.togglePlay()} disabled={!engine}>
            {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
          </Button>
          <Button
            size="icon-xs"
            variant="ghost"
            aria-label={`Play the segment from its start (${KEYS.replay})`}
            onClick={() => {
              engine?.seek(span.start);
              if (!playing) engine?.togglePlay();
            }}
            disabled={!engine}
          >
            <Restart aria-hidden />
          </Button>
          <Button size="xs" variant="outline" onClick={() => setChIdx((i) => i + 1)} title={`Switch channel (${KEYS.channel})`} data-slot="channel-switch">
            <SoundHigh aria-hidden />
            {channelLabel(item, channel)}
          </Button>
        </span>
      </div>
      <AudioView
        utterance={item.id}
        channel={channel}
        analysis
        onAnalysis={setAnalysis}
        span={span}
        channelLabels={labels}
        title={`Item ${item.position}: the segment with ${item.window.start.toFixed(1)}–${item.window.end.toFixed(1)} s of its call`}
        onEngine={setEngine}
        onPlayState={setPlaying}
      />
      {analysis ? (
        <p className="text-muted-foreground" data-slot="tracks-summary">
          {analysis.narrowband ? "8 kHz origin: the spectrogram stops at 4 kHz. " : ""}
          {bot?.gapS !== undefined ? `End of utterance: the other party answers ${bot.gapS >= 0 ? `${bot.gapS.toFixed(2)} s after` : `${(-bot.gapS).toFixed(2)} s before`} the target stops.` : ""}
        </p>
      ) : null}
      {item.context.turns.length ? (
        <section aria-label="The other party around the segment" className="flex flex-col gap-0.5 rounded border p-2">
          {item.context.turns.map((t, i) => (
            <p key={i} dir="auto" className="text-muted-foreground">
              <span className="tabular-nums">
                {t.start.toFixed(1)}–{t.end.toFixed(1)} s
              </span>{" "}
              {t.role ?? `channel ${t.channel ?? ""}`}: <bdi className="text-foreground">{t.text}</bdi>
            </p>
          ))}
        </section>
      ) : null}
      <label className="flex flex-col gap-1">
        <span className="text-muted-foreground">
          Transcript of the target{" "}
          {item.prefill.text ? `— starts from the machine's guess (${item.prefill.origin}${item.prefill.confidence !== undefined ? `, confidence ${item.prefill.confidence.toFixed(2)}` : ""}): listen, then correct it` : "— no machine guess: type what you hear"}
        </span>
        <Textarea
          ref={text}
          dir="auto"
          rows={3}
          value={form.text}
          onChange={(e) => {
            const v = e.target.value;
            setForm((f) => ({ ...f, text: v, entities: keepSpans(v, f.entities) }));
          }}
          aria-describedby={`keys-${item.id}`}
          data-slot="transcript"
        />
      </label>
      <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Tags">
        {TAGS.map((t) => (
          <Button
            key={t.tag}
            size="xs"
            variant={form.tags.includes(t.tag) ? "secondary" : "ghost"}
            aria-pressed={form.tags.includes(t.tag)}
            title={`${t.hint} (Alt+${t.key})`}
            onClick={() => setForm((f) => ({ ...f, tags: toggleTag(f.tags, t.tag) }))}
          >
            {t.label}
          </Button>
        ))}
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        <NativeSelect className="h-6 w-24" aria-label="Entity class" value={cls} onChange={(e) => setCls(e.target.value)}>
          {ENTITY_CLASSES.map((c) => (
            <option key={c} value={c}>
              {c}
            </option>
          ))}
        </NativeSelect>
        <Button size="xs" variant="ghost" onClick={markEntity} title={`Select words in the transcript, then mark them (${KEYS.entity})`}>
          Mark entity
        </Button>
      </div>
      {form.entities.length ? (
        <ul aria-label="Entity spans" className="flex flex-wrap gap-1">
          {form.entities.map((e, i) => (
            <li key={`${e.start}-${e.end}`} className="flex items-center gap-1 rounded border px-1.5 py-0.5">
              <span className="text-muted-foreground">{e.class}</span>
              <bdi>{e.text}</bdi>
              <button type="button" aria-label={`Remove ${e.class} “${e.text}”`} className="px-0.5 text-muted-foreground hover:text-foreground" onClick={() => setForm((f) => ({ ...f, entities: f.entities.filter((_, j) => j !== i) }))}>
                ×
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-1">
        <Button size="xs" onClick={() => void submit("done")} disabled={busy || !form.text.trim()} data-command="annotations.new">
          <Check aria-hidden />
          Done
        </Button>
        <Button size="xs" variant="outline" onClick={() => void submit("flagged")} disabled={busy || !form.text.trim()} title="A transcript you are unsure of: a second annotator takes the item blind">
          <WarningCircle aria-hidden />
          Flag
        </Button>
        <Button size="xs" variant="ghost" onClick={() => void submit("skipped")} disabled={busy} title="Someone else should take it">
          <Forward aria-hidden />
          Skip
        </Button>
        <span id={`keys-${item.id}`} className="ml-auto text-[11px] text-muted-foreground">
          {KEYS.done} done · {KEYS.flag} flag · {KEYS.skip} skip · {KEYS.play} play · {KEYS.replay} replay · {KEYS.channel} channel · {KEYS.tag} tags · {KEYS.entity} entity
        </span>
      </div>
    </div>
  );
}
