import type { AlignedWord } from "@/shell/evaluation/format";

// A typed reference against a lane's text, on the page only (R47: nothing is stored). Word-level Levenshtein
// alignment over lightly normalised words (case folded, punctuation dropped, Unicode NFC) — a page-side check, not
// an eval's scoring normalizer: evals are where numbers are kept and compared.

/** Words of a text for alignment: NFC, lower case, punctuation removed, split on whitespace. */
export function alignmentWords(text: string): string[] {
  return text
    .normalize("NFC")
    .toLowerCase()
    .replace(/[\p{P}\p{S}]+/gu, " ")
    .split(/\s+/)
    .filter(Boolean);
}

/**
 * The minimum-edit alignment of hyp against ref: = match, S substitution, D deletion (a reference word the hypothesis
 * lacks), I insertion. Ties prefer a match or substitution over a deletion and a deletion over an insertion.
 */
export function alignWords(ref: string[], hyp: string[]): AlignedWord[] {
  const n = ref.length;
  const m = hyp.length;
  const w = m + 1;
  const d = new Uint32Array((n + 1) * w);
  for (let i = 0; i <= n; i++) d[i * w] = i;
  for (let j = 0; j <= m; j++) d[j] = j;
  for (let i = 1; i <= n; i++) {
    for (let j = 1; j <= m; j++) {
      const sub = d[(i - 1) * w + j - 1]! + (ref[i - 1] === hyp[j - 1] ? 0 : 1);
      const del = d[(i - 1) * w + j]! + 1;
      const ins = d[i * w + j - 1]! + 1;
      d[i * w + j] = Math.min(sub, del, ins);
    }
  }
  const out: AlignedWord[] = [];
  let i = n;
  let j = m;
  while (i > 0 || j > 0) {
    const cur = d[i * w + j]!;
    if (i > 0 && j > 0 && cur === d[(i - 1) * w + j - 1]! + (ref[i - 1] === hyp[j - 1] ? 0 : 1)) {
      out.push({ op: ref[i - 1] === hyp[j - 1] ? "=" : "S", ref: ref[i - 1]!, hyp: hyp[j - 1]! });
      i--;
      j--;
    } else if (i > 0 && cur === d[(i - 1) * w + j]! + 1) {
      out.push({ op: "D", ref: ref[i - 1]!, hyp: "" });
      i--;
    } else {
      out.push({ op: "I", ref: "", hyp: hyp[j - 1]! });
      j--;
    }
  }
  return out.reverse();
}

/** The alignment of a typed reference and a lane's text, with the counts and WER. */
export function compareText(reference: string, hypothesis: string): { words: AlignedWord[]; sub: number; del: number; ins: number; refWords: number; wer: number } {
  const words = alignWords(alignmentWords(reference), alignmentWords(hypothesis));
  let sub = 0;
  let del = 0;
  let ins = 0;
  let match = 0;
  for (const a of words) {
    if (a.op === "=") match++;
    else if (a.op === "S") sub++;
    else if (a.op === "D") del++;
    else ins++;
  }
  const refWords = match + sub + del;
  const errors = sub + del + ins;
  return { words, sub, del, ins, refWords, wer: refWords > 0 ? errors / refWords : errors > 0 ? 1 : 0 };
}
