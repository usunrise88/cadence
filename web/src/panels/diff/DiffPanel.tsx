import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, NavArrowLeft, NavArrowRight, SoundHigh } from "iconoir-react";
import { evalsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { Eval, EvalCell, EvalUtterance } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { AudioView, openAudio } from "@/shell/audio";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import {
  alignedWords,
  errorMessage,
  evalIdOfDoc,
  evalItem,
  formatRate,
  OP_GLYPH,
  OP_LABEL,
  openDocument,
  parseEvalItem,
  textDirection,
  useFollowedDoc,
  useSelection,
  WORST_N,
  type AlignedWord,
  type AlignOp,
  type PanelProps,
} from "@/shell/panel";

// Diff (docs/spec/11-ui-panels.md "Panel catalogue", Diff): reference against hypothesis for the utterance selected in
// the active Eval report (cell:<id>/utt:<n>), word by word from the scores rows' alignment ops, after the scoring
// normalizer. Substitutions, deletions and insertions are marked by glyph, a word and colour (colour is never the
// only channel); every word is bidi-isolated and the line runs in the golden set's direction (Hebrew right to left).
// Previous / next step through the cell's worst utterances. The texts on request; copy puts both on the clipboard. The
// utterance's audio view (R51) carries the hypothesis word track; Open in Audio shows it in the floating Audio panel.

export function DiffEmpty() {
  return <EmptyState step="review" title="No utterance selected" hint="Select a cell in an Eval report, then an utterance in its table." />;
}

export function DiffPanel({ instanceId }: PanelProps) {
  const { doc } = useFollowedDoc(instanceId);
  const evalId = evalIdOfDoc(doc);
  const item = useSelection((s) => (doc ? s.selections[doc] : undefined));
  const { cellId, utterance } = parseEvalItem(item);
  if (!doc || !evalId || !cellId) return <DiffEmpty />;
  return <CellDiff doc={doc} evalId={evalId} cellId={cellId} utterance={utterance} />;
}

function CellDiff({ doc, evalId, cellId, utterance }: { doc: string; evalId: string; cellId: string; utterance?: number }) {
  const q = useQuery(evalsGetOptions({ path: { id: evalId }, query: { worst: WORST_N, cell: cellId } }));
  const select = useSelection((s) => s.select);
  const ev = q.data;
  const cell = ev?.cells?.find((c) => c.id === cellId);
  const rows = cell?.worst ?? [];
  const at = Math.max(0, utterance === undefined ? 0 : rows.findIndex((r) => r.index === utterance));
  const row = rows[at];
  if (q.isLoading) return <p className="p-3 text-xs text-muted-foreground">Loading…</p>;
  if (q.error) return <p className="p-3 text-xs text-destructive">{errorMessage(q.error)}</p>;
  if (!ev || !cell) return <DiffEmpty />;
  const gs = ev.goldenSets.find((g) => g.versionId === cell.goldenSetVersionId);
  const go = (i: number) => {
    const r = rows[i];
    if (r) select(doc, evalItem(cellId, r.index));
  };
  return (
    <div
      className="flex h-full min-h-0 flex-col"
      data-slot="diff"
      onKeyDown={(e) => {
        // Alt+↑/↓ step through the cell's utterances wherever focus is inside the panel.
        if (!e.altKey || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
        e.preventDefault();
        go(at + (e.key === "ArrowDown" ? 1 : -1));
      }}
    >
      <PanelToolbar>
        <button type="button" className="min-w-0 truncate text-[13px] font-medium underline-offset-2 hover:underline" onClick={() => openDocument(doc)} title="Open the Eval report">
          {(gs?.name ?? cell.goldenSetVersionId).replace(/^golden-set\//, "")} · {cell.profile}
        </button>
        <span className="text-xs text-muted-foreground">{cell.role}</span>
        <span className="ml-auto flex items-center gap-1 text-xs tabular-nums">
          <Button size="icon-xs" variant="ghost" aria-label="Previous utterance (Alt+↑)" disabled={at <= 0} onClick={() => go(at - 1)}>
            <NavArrowLeft aria-hidden />
          </Button>
          <span aria-live="polite">{rows.length ? `${at + 1} of ${rows.length}` : "—"}</span>
          <Button size="icon-xs" variant="ghost" aria-label="Next utterance (Alt+↓)" disabled={at >= rows.length - 1} onClick={() => go(at + 1)}>
            <NavArrowRight aria-hidden />
          </Button>
        </span>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto">
        {row ? (
          <UtteranceDiff row={row} ev={ev} cell={cell} locale={gs?.locale} />
        ) : (
          <EmptyState step="review" title="No utterance rows" hint={cell.evicted ? cell.evicted.note : "The cell has no scored utterances yet."} />
        )}
      </div>
    </div>
  );
}

const OP_CLASS: Readonly<Record<AlignOp, string>> = {
  "=": "",
  S: "text-status-warning-foreground underline decoration-wavy decoration-1 underline-offset-4",
  D: "bg-diff-removed text-diff-removed-foreground line-through",
  I: "bg-diff-added text-diff-added-foreground underline underline-offset-4",
};

function UtteranceDiff({ row, ev, cell, locale }: { row: EvalUtterance; ev: Eval; cell: EvalCell; locale?: string }) {
  const [texts, setTexts] = useState(false);
  const [copied, setCopied] = useState(false);
  const words = alignedWords(row.ops);
  const dir = textDirection(locale);
  const copy = async (el: HTMLElement) => {
    // The clipboard of the window the panel is in (popouts have their own).
    const win = el.ownerDocument.defaultView ?? window;
    try {
      await win.navigator.clipboard.writeText(`REF: ${row.ref}\nHYP: ${row.hyp}`);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="flex flex-col gap-3 p-3 text-xs" data-utterance={row.index}>
      <dl className="flex flex-wrap gap-x-4 gap-y-1 tabular-nums" aria-label="Utterance numbers">
        <Num label="Utterance" value={`#${row.index}`} />
        <Num label="WER" value={formatRate(row.wer, 1)} />
        <Num label="Substitutions" value={String(row.sub)} glyph={OP_GLYPH.S} />
        <Num label="Deletions" value={String(row.del)} glyph={OP_GLYPH.D} />
        <Num label="Insertions" value={String(row.ins)} glyph={OP_GLYPH.I} />
        <Num label="Reference words" value={String(row.refWords)} />
        {row.durationS !== undefined ? <Num label="Duration" value={`${row.durationS.toFixed(1)} s`} /> : null}
        {row.speaker ? <Num label="Speaker" value={row.speaker} /> : null}
      </dl>
      <Alignment words={words} dir={dir} />
      <AudioView key={row.audio} utterance={row.audio} compact title={`Audio of utterance #${row.index}`} hypotheses={cell.hypotheses} scores={cell.scores} lang={locale} />
      <p className="flex flex-wrap gap-x-3 text-[11px] text-muted-foreground" data-slot="diff-legend">
        {(["S", "D", "I"] as const).map((op) => (
          <span key={op}>
            <span aria-hidden className={cn("rounded px-0.5", OP_CLASS[op])}>
              {OP_GLYPH[op]}
            </span>{" "}
            {OP_LABEL[op]}
          </span>
        ))}
        <span>Upper line reference, lower line hypothesis; after the scoring normalizer.</span>
      </p>
      <div className="flex flex-wrap gap-1">
        <Button size="xs" variant="outline" aria-pressed={texts} onClick={() => setTexts((t) => !t)}>
          {texts ? "Hide texts" : "Show texts"}
        </Button>
        <Button size="xs" variant="outline" onClick={() => openAudio({ utterance: row.audio, cell: cell.id, hypotheses: cell.hypotheses, scores: cell.scores })}>
          <SoundHigh aria-hidden />
          Open in Audio
        </Button>
        <Button size="xs" variant="ghost" onClick={(e) => void copy(e.currentTarget)}>
          <Copy aria-hidden />
          {copied ? "Copied" : "Copy"}
        </Button>
        <span className="ml-auto self-center text-muted-foreground">{ev.subject.label}</span>
      </div>
      {texts ? (
        <dl className="grid grid-cols-[6rem_1fr] gap-x-2 gap-y-1" data-slot="diff-texts">
          <dt className="text-muted-foreground">Reference</dt>
          <dd dir={dir} className="break-words">
            {row.ref}
          </dd>
          <dt className="text-muted-foreground">Hypothesis</dt>
          <dd dir={dir} className="break-words">
            {row.hyp}
          </dd>
        </dl>
      ) : null}
    </div>
  );
}

function Num({ label, value, glyph }: { label: string; value: string; glyph?: string }) {
  return (
    <div className="flex items-baseline gap-1">
      <dt className="text-muted-foreground">
        {glyph ? <span aria-hidden>{glyph} </span> : null}
        {label}
      </dt>
      <dd className="font-medium">{value}</dd>
    </div>
  );
}

/** The aligned words: one column per pair (reference over hypothesis), wrapping in the text's direction. */
export function Alignment({ words, dir }: { words: AlignedWord[]; dir: "rtl" | "ltr" }) {
  if (words.length === 0) return <p className="text-muted-foreground">Both texts are empty.</p>;
  return (
    <ol dir={dir} className="flex flex-wrap gap-x-1.5 gap-y-2 text-[13px] leading-tight" aria-label="Alignment, reference over hypothesis" data-slot="alignment">
      {words.map((w, i) => (
        <li
          key={i}
          className="flex flex-col items-start rounded px-0.5"
          data-op={w.op}
          aria-label={w.op === "=" ? w.ref : w.op === "S" ? `${OP_LABEL.S}: ${w.ref} → ${w.hyp}` : w.op === "D" ? `${OP_LABEL.D}: ${w.ref}` : `${OP_LABEL.I}: ${w.hyp}`}
        >
          <span className={cn("min-h-5", w.op === "D" || w.op === "S" ? OP_CLASS[w.op] : w.op === "I" ? "text-muted-foreground" : "")}>{w.op === "I" ? <span aria-hidden>·</span> : <bdi>{w.ref}</bdi>}</span>
          <span className={cn("min-h-5", w.op === "I" || w.op === "S" ? OP_CLASS[w.op] : w.op === "D" ? "text-muted-foreground" : "text-muted-foreground")}>
            {w.op === "D" ? <span aria-hidden>·</span> : <bdi>{w.hyp}</bdi>}
          </span>
          {w.op !== "=" ? (
            <span aria-hidden className="text-[10px] text-muted-foreground">
              {OP_GLYPH[w.op]} {w.op}
            </span>
          ) : null}
        </li>
      ))}
    </ol>
  );
}
