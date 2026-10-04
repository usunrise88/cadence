import { useMemo, useState, type KeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Prohibition, TextSquare } from "iconoir-react";
import { batchesListOptions, triageListOptions, triageListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { TriageItem } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { AudioView } from "@/shell/audio";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { AnnotateView, errorMessage, openDocument, runCommand, useProject, useTopic, type PanelProps } from "@/shell/panel";

// Triage (docs/spec/11-ui-panels.md "Panel catalogue", Triage queue; docs/spec/04-blocks.md "Annotation workflow").
// Queue: the project's disputed pseudo-labels (triage.list) — the segment's window of its call with every channel, each
// member's text, why it is disputed — resolved by Accept (the best candidate becomes the human transcript, Enter),
// Correct (your text, E then Ctrl+Enter) or Reject (Backspace). Annotate: an open annotation batch, item by item
// (the shell's Annotate view: channel switch, prefilled transcript, tags, done / flag / skip, keyboard-first).

const REASONS: Record<string, string> = {
  disagreement: "the members disagree",
  "lid-mismatch": "the language is not the source's",
  "lid-unknown": "the language is uncertain",
  "no-speech": "no member heard speech",
  "too-few-members": "too few members answered",
};

export function TriageEmpty() {
  return <EmptyState step="review" title="No project open" hint="Open a project: its disputed pseudo-labels and annotation batches are worked here." />;
}

export function TriagePanel(_: PanelProps) {
  const project = useProject();
  const [mode, setMode] = useState<"queue" | "annotate">("queue");
  if (!project) return <TriageEmpty />;
  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="triage-panel">
      <PanelToolbar>
        <div role="tablist" aria-label="Mode" className="flex gap-1">
          {(["queue", "annotate"] as const).map((m) => (
            <Button key={m} role="tab" aria-selected={mode === m} size="xs" variant={mode === m ? "secondary" : "ghost"} onClick={() => setMode(m)}>
              {m === "queue" ? "Queue" : "Annotate"}
            </Button>
          ))}
        </div>
      </PanelToolbar>
      <div className="min-h-0 flex-1">{mode === "queue" ? <Queue project={project} /> : <Annotate project={project} />}</div>
    </div>
  );
}

function Queue({ project }: { project: string }) {
  const qc = useQueryClient();
  const opts = triageListOptions({ path: { p: project }, query: { state: "open" } });
  const q = useQuery(opts);
  useTopic(["triage.new", "entity.triage_item.*"], () => void qc.invalidateQueries({ queryKey: triageListQueryKey({ path: { p: project }, query: { state: "open" } }) }));
  const items = useMemo(() => q.data?.items ?? [], [q.data]);
  const [sel, setSel] = useState<string | undefined>();
  const item = items.find((i) => i.id === sel) ?? items[0];
  if (q.isLoading) return <p className="p-4 text-xs text-muted-foreground">Loading the queue…</p>;
  if (q.error) return <p role="alert" className="p-4 text-xs text-destructive">{errorMessage(q.error)}</p>;
  if (items.length === 0) return <EmptyState step="review" title="Nothing to triage" hint="Segments whose pseudo-label members disagree land here after a pseudo-label run." />;
  const idx = item ? items.indexOf(item) : -1;
  const onKey = (e: KeyboardEvent<HTMLUListElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const n = items[Math.max(0, Math.min(items.length - 1, idx + (e.key === "ArrowDown" ? 1 : -1)))];
      if (n) setSel(n.id);
    }
  };
  return (
    <div className="flex h-full min-h-0">
      <ul role="listbox" aria-label="Disputed segments" tabIndex={0} onKeyDown={onKey} className="w-64 shrink-0 overflow-auto border-r text-xs outline-none">
        {items.map((it) => (
          <li
            key={it.id}
            role="option"
            aria-selected={it.id === item?.id}
            className={cn("flex cursor-pointer flex-col gap-0.5 px-2 py-1.5 hover:bg-hover", it.id === item?.id && "bg-selected")}
            onClick={() => setSel(it.id)}
          >
            <span className="truncate" dir="auto">
              {it.best || "—"}
            </span>
            <span className="text-[10px] text-muted-foreground">
              {REASONS[it.reason] ?? it.reason} · {it.segment.role ?? "segment"}
            </span>
          </li>
        ))}
      </ul>
      <div className="min-w-0 flex-1 overflow-auto">{item ? <TriageItemView key={item.id} item={item} onResolved={() => setSel(items[idx + 1]?.id)} /> : null}</div>
    </div>
  );
}

function TriageItemView({ item, onResolved }: { item: TriageItem; onResolved: () => void }) {
  const qc = useQueryClient();
  const [text, setText] = useState(item.best);
  const [editing, setEditing] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await fn();
      onResolved();
      await qc.invalidateQueries({ queryKey: [{ _id: "triageList" }] });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const ref = { id: item.id, rev: item.rev };
  const accept = () => run(() => runCommand("triage.accept", { item: ref }));
  const correct = () => run(() => runCommand("triage.correct", { item: ref, body: { text: text.trim() } }));
  const reject = () => run(() => runCommand("triage.reject", { item: ref, body: {} }));
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const inText = (e.target as HTMLElement).tagName === "TEXTAREA";
    if (inText) {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault();
        void correct();
      }
      return;
    }
    if (e.key === "Enter") void accept();
    else if (e.key === "Backspace") void reject();
    else if (e.key.toLowerCase() === "e") setEditing(true);
    else return;
    e.preventDefault();
  };
  return (
    <div className="flex flex-col gap-3 p-3 text-xs" onKeyDown={onKey} tabIndex={-1} data-slot="triage-item" data-item={item.id}>
      <p className="text-muted-foreground">
        Disputed because {REASONS[item.reason] ?? item.reason}
        {item.lid?.language ? ` · language identified as ${item.lid.language}${item.lid.confidence !== undefined ? ` (${item.lid.confidence.toFixed(2)})` : ""}` : ""} · confidence{" "}
        {item.confidence.toFixed(2)}
      </p>
      <AudioView
        utterance={item.id}
        analysis
        span={item.segment.start !== undefined && item.segment.end !== undefined ? { start: Math.min(2, item.segment.start), end: Math.min(2, item.segment.start) + (item.segment.end - item.segment.start) } : undefined}
        title={`Disputed segment ${item.id}`}
      />
      <table className="w-full text-left">
        <thead className="text-muted-foreground">
          <tr>
            <th className="font-normal">Member</th>
            <th className="font-normal">Text</th>
            <th className="font-normal">Mean WER</th>
          </tr>
        </thead>
        <tbody>
          {item.candidates.map((c) => (
            <tr key={c.member} className="align-top">
              <td className="pr-2 text-muted-foreground">{c.member}</td>
              <td className="pr-2">
                <button type="button" dir="auto" className="text-left hover:underline" title="Start from this text" onClick={() => (setText(c.text), setEditing(true))}>
                  <bdi>{c.text || "—"}</bdi>
                </button>
              </td>
              <td className="tabular-nums">{c.meanWer !== undefined ? c.meanWer.toFixed(2) : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {editing ? <Textarea dir="auto" rows={3} value={text} onChange={(e) => setText(e.target.value)} autoFocus aria-label="Your transcript (Ctrl+Enter to save)" /> : null}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-1">
        <Button size="xs" onClick={() => void accept()} disabled={busy || !item.best} title="The best candidate becomes the human transcript (Enter)" data-command="triage.accept">
          <Check aria-hidden />
          Accept
        </Button>
        <Button
          size="xs"
          variant="outline"
          onClick={() => (editing ? void correct() : setEditing(true))}
          disabled={busy || (editing && !text.trim())}
          title="Your own transcript (E, then Ctrl+Enter)"
          data-command="triage.correct"
        >
          <TextSquare aria-hidden />
          {editing ? "Save correction" : "Correct"}
        </Button>
        <Button size="xs" variant="ghost" onClick={() => void reject()} disabled={busy} title="Drop the segment (Backspace)" data-command="triage.reject">
          <Prohibition aria-hidden />
          Reject
        </Button>
      </div>
    </div>
  );
}

function Annotate({ project }: { project: string }) {
  const q = useQuery(batchesListOptions({ path: { p: project }, query: { state: "open" } }));
  const [batch, setBatch] = useState<string | undefined>();
  const list = q.data?.items ?? [];
  const id = batch ?? list[0]?.id;
  if (q.isLoading) return <p className="p-4 text-xs text-muted-foreground">Loading the batches…</p>;
  if (list.length === 0)
    return (
      <EmptyState
        step="prepare"
        title="No open annotation batch"
        hint="Sample one from an ingested dataset in a new Annotation batch; then annotate it here."
        action={
          <Button size="xs" variant="outline" onClick={() => void runCommand("batches.new", undefined)} data-command="batches.new">
            New annotation batch
          </Button>
        }
      />
    );
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center gap-2 border-b px-2 py-1 text-xs">
        <label className="flex items-center gap-1 text-muted-foreground">
          Batch
          <NativeSelect className="h-6 w-48" value={id} onChange={(e) => setBatch(e.target.value)}>
            {list.map((b) => (
              <option key={b.id} value={b.id}>
                {b.name} ({b.progress.pending} open)
              </option>
            ))}
          </NativeSelect>
        </label>
        {id ? (
          <Button size="xs" variant="ghost" onClick={() => openDocument(`annotation_batch:${id}`)}>
            Open the batch
          </Button>
        ) : null}
      </div>
      <div className="min-h-0 flex-1">{id ? <AnnotateView key={id} batchId={id} /> : null}</div>
    </div>
  );
}
