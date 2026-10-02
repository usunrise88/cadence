import type { Eval, EvalInterval } from "@/api/gen/types.gen";

// How evaluation numbers, deltas and alignments read wherever they appear (Eval report, Diff, Golden set, Model,
// Checkpoints). Rates arrive as fractions (0.123) and deltas as subject − baseline (docs/review/2026-10-02-phase-3-plan.md
// "The scores artifact"); the UI shows rates in % and deltas in percentage points. Colour is never the only channel:
// every tone and alignment op has a glyph and a word as well (WCAG 1.4.1).

/** Scripts written right to left (BCP 47 primary language subtags). */
const RTL = new Set(["he", "iw", "ar", "fa", "ur", "yi", "ps", "dv", "ckb", "sd", "ug", "syr"]);

/** The writing direction of a locale's text (he-IL → rtl). Unknown or missing locales read left to right. */
export function textDirection(locale: string | undefined): "rtl" | "ltr" {
  const lang = (locale ?? "").split(/[-_]/)[0]?.toLowerCase() ?? "";
  return RTL.has(lang) ? "rtl" : "ltr";
}

const finite = (v: number | undefined | null): v is number => typeof v === "number" && Number.isFinite(v);
const MINUS = "−";

/** A rate as a percentage: 0.1234 → "12.3 %". */
export function formatRate(v: number | undefined | null, digits = 1): string {
  if (!finite(v)) return "—";
  return `${(v * 100).toFixed(digits)} %`;
}

/** A signed difference of rates in percentage points: −0.012 → "−1.2 pp", 0 → "0.0 pp". */
export function formatDelta(v: number | undefined | null, digits = 1): string {
  if (!finite(v)) return "—";
  const pp = v * 100;
  const s = Math.abs(pp).toFixed(digits);
  if (Number(s) === 0) return `${(0).toFixed(digits)} pp`;
  return `${pp < 0 ? MINUS : "+"}${s} pp`;
}

/** A delta with its interval: "−1.2 pp [−2.0, −0.4]". */
export function formatInterval(i: EvalInterval | undefined, digits = 1): string {
  if (!i) return "—";
  const b = (v: number) => {
    const pp = v * 100;
    const s = Math.abs(pp).toFixed(digits);
    return Number(s) === 0 ? (0).toFixed(digits) : `${pp < 0 ? MINUS : ""}${s}`;
  };
  return `${formatDelta(i.value, digits)} [${b(i.low)}, ${b(i.high)}]`;
}

/**
 * How a delta reads for an error rate (lower is better): better when the whole interval lies below zero, worse when
 * it lies above, same when it crosses zero, none without a delta.
 */
export type DeltaTone = "better" | "worse" | "same" | "none";

export function deltaTone(i: EvalInterval | undefined): DeltaTone {
  if (!i || !finite(i.low) || !finite(i.high)) return "none";
  if (i.high < 0) return "better";
  if (i.low > 0) return "worse";
  return "same";
}

export const TONE_GLYPH: Readonly<Record<DeltaTone, string>> = { better: "▼", worse: "▲", same: "≈", none: "·" };
export const TONE_LABEL: Readonly<Record<DeltaTone, string>> = {
  better: "better than the baseline",
  worse: "worse than the baseline",
  same: "no significant difference",
  none: "no delta yet",
};
/** Token classes for a tone (theme.css status roles). */
export const TONE_CLASS: Readonly<Record<DeltaTone, string>> = {
  better: "text-status-done-foreground",
  worse: "text-status-failed-foreground",
  same: "text-muted-foreground",
  none: "text-muted-foreground",
};

/** A gate check's or verdict's state: glyph, word and token class. */
export type GateState = "passed" | "failed" | "inconclusive";
export const GATE_GLYPH: Readonly<Record<GateState, string>> = { passed: "✓", failed: "✗", inconclusive: "?" };
export const GATE_CLASS: Readonly<Record<GateState, string>> = {
  passed: "text-status-done-foreground",
  failed: "text-status-failed-foreground",
  inconclusive: "text-status-warning-foreground",
};

// ---------------------------------------------------------------- alignment ops (the scores rows' ops)

/** One aligned pair: `=` match, `S` substitution, `D` deletion (reference word missing), `I` insertion. */
export type AlignOp = "=" | "S" | "D" | "I";
export type AlignedWord = { op: AlignOp; ref: string; hyp: string };

/** The scores rows' `[op, ref, hyp]` triples as typed pairs; unknown ops read as substitutions, missing words as "". */
export function alignedWords(ops: ReadonlyArray<ReadonlyArray<string | null | undefined>>): AlignedWord[] {
  return ops.map((o) => {
    const raw = o[0] ?? "";
    const op: AlignOp = raw === "=" || raw === "S" || raw === "D" || raw === "I" ? raw : "S";
    return { op, ref: op === "I" ? "" : (o[1] ?? ""), hyp: op === "D" ? "" : (o[2] ?? "") };
  });
}

/** Counts of each op and the reference length they imply (matches + substitutions + deletions). */
export function alignmentCounts(words: AlignedWord[]): { match: number; sub: number; del: number; ins: number; refWords: number; wer: number } {
  let match = 0;
  let sub = 0;
  let del = 0;
  let ins = 0;
  for (const w of words) {
    if (w.op === "=") match++;
    else if (w.op === "S") sub++;
    else if (w.op === "D") del++;
    else ins++;
  }
  const refWords = match + sub + del;
  const errors = sub + del + ins;
  return { match, sub, del, ins, refWords, wer: refWords > 0 ? errors / refWords : errors > 0 ? 1 : 0 };
}

export const OP_GLYPH: Readonly<Record<AlignOp, string>> = { "=": "", S: "≠", D: "−", I: "+" };
export const OP_LABEL: Readonly<Record<AlignOp, string>> = { "=": "match", S: "substitution", D: "deletion", I: "insertion" };

// ---------------------------------------------------------------- selection items inside an Eval report

/** How many worst utterances the report's table and Diff read for a cell (evals.get ?worst=, max 200). */
export const WORST_N = 50;

/** The selection item of a cell, or of one of its utterances (`cell:evc_1`, `cell:evc_1/utt:3`). */
export function evalItem(cellId: string, utterance?: number): string {
  return utterance === undefined ? `cell:${cellId}` : `cell:${cellId}/utt:${utterance}`;
}

/** The cell and utterance index a selection item names. */
export function parseEvalItem(item: string | undefined | null): { cellId?: string; utterance?: number } {
  const m = /^cell:([A-Za-z0-9_-]+)(?:\/utt:(\d+))?$/.exec(item ?? "");
  if (!m) return {};
  return { cellId: m[1], ...(m[2] !== undefined ? { utterance: Number(m[2]) } : {}) };
}

/** The eval a document reference names (`eval:<id>`), if it names one. */
export function evalIdOfDoc(doc: string | null | undefined): string | undefined {
  return doc?.startsWith("eval:") ? doc.slice(5) : undefined;
}

/** Whether a model version can be registered from an eval: a checkpoint subject whose gate passed (R22). */
export function registrable(e: Pick<Eval, "subject" | "gate"> | undefined): true | string {
  if (!e) return "Open an eval first";
  if (e.subject.kind !== "checkpoint") return "Only a checkpoint is registered as a model version";
  if (!e.gate) return "Run the gate first";
  if (e.gate.verdict !== "passed") return "The gate did not pass";
  return true;
}
