import { create } from "zustand";
import { openPanel } from "@/shell/dock/layout";
import { useSelection } from "@/shell/selection/store";

// A text pair Diff shows without an eval (phase 5 · stream D4): a shadow replay's segment, the comparison model's
// transcript over this deployment's. The Shadow panel opens one with openDiffSegment; Diff shows it until the active
// document or its selection changes (an eval utterance: whichever came last wins). The words are aligned in the
// browser after the basic normalisation shadow_score applies (NFC, case-folded, punctuation stripped): no scores rows
// exist for them.

export const DIFF_PANEL = "diff";

export type DiffSegment = {
  /** The segment's audio (b3:…): audio.get plays it while its night's texts are kept. */
  audio: string;
  /** The reference line: the comparison model's transcript. */
  ref: string;
  /** The hypothesis line: this deployment's transcript. */
  hyp: string;
  refLabel: string;
  hypLabel: string;
  /** What the segment is: the call and the night. */
  label: string;
  wer?: number;
  start?: number;
  end?: number;
  duration?: number;
};

type State = { segment: DiffSegment | null; at: number; set(segment: DiffSegment | null): void };

export const useDiffSegment = create<State>((set) => ({
  segment: null,
  at: 0,
  set: (segment) => set({ segment, at: segment ? Date.now() : 0 }),
}));

// A new active document or selection takes Diff back from the segment.
useSelection.subscribe((s, prev) => {
  if ((s.activeDoc !== prev.activeDoc || s.selections !== prev.selections) && useDiffSegment.getState().segment) {
    useDiffSegment.getState().set(null);
  }
});

/** Shows a text pair in Diff (opening it). */
export function openDiffSegment(segment: DiffSegment): void {
  useDiffSegment.getState().set(segment);
  try {
    openPanel(DIFF_PANEL);
  } catch {
    // No dock (tests, the reviewer page): the store still holds the segment.
  }
}

/** The words shadow_score compares: NFC, case-folded, punctuation and symbols stripped, whitespace collapsed. */
export function basicWords(text: string): string[] {
  return text
    .normalize("NFC")
    .toLocaleLowerCase()
    .replace(/[\p{P}\p{S}]+/gu, " ")
    .split(/\s+/)
    .filter(Boolean);
}

/** A word-level Levenshtein alignment of hyp against ref as `[op, ref, hyp]` triples (=, S, D, I). */
export function alignTexts(ref: string, hyp: string): [string, string, string][] {
  const r = basicWords(ref);
  const h = basicWords(hyp);
  const n = r.length;
  const m = h.length;
  const d: number[][] = Array.from({ length: n + 1 }, (_, i) => Array.from({ length: m + 1 }, (_, j) => (i === 0 ? j : j === 0 ? i : 0)));
  for (let i = 1; i <= n; i++) {
    for (let j = 1; j <= m; j++) {
      const sub = d[i - 1]![j - 1]! + (r[i - 1] === h[j - 1] ? 0 : 1);
      d[i]![j] = Math.min(sub, d[i - 1]![j]! + 1, d[i]![j - 1]! + 1);
    }
  }
  const out: [string, string, string][] = [];
  let i = n;
  let j = m;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && d[i]![j] === d[i - 1]![j - 1]! + (r[i - 1] === h[j - 1] ? 0 : 1)) {
      out.push([r[i - 1] === h[j - 1] ? "=" : "S", r[i - 1]!, h[j - 1]!]);
      i--;
      j--;
    } else if (i > 0 && d[i]![j] === d[i - 1]![j]! + 1) {
      out.push(["D", r[i - 1]!, ""]);
      i--;
    } else {
      out.push(["I", "", h[j - 1]!]);
      j--;
    }
  }
  return out.reverse();
}
