import type { BranchCompareFile, MergeHunk } from "@/api/gen/types.gen";

// The three-way layout behind Session changes (docs/spec/05-agents.md "Worktree, drafts and merge"): branches.compare
// cuts a conflicting file into hunks that cover every line of the merge base, main and the branch, each marked with
// the side that changed it. This turns them into blocks both views draw — side by side (base · main · branch, stacked
// when narrow) and unified (context, what each side brings, conflicts as three labelled parts) — with long unchanged
// runs folded to a few lines of context.

export type Side = "base" | "main" | "branch";
export const SIDES: readonly Side[] = ["base", "main", "branch"];

export interface Line {
  /** 1-based line number in its version. */
  n: number;
  text: string;
}

export type Block =
  | { type: "gap"; lines: number }
  | {
      type: "hunk";
      kind: MergeHunk["kind"];
      /** Index of the hunk in the file's list. */
      index: number;
      /** 0-based ordinal among the file's conflict hunks. */
      conflict?: number;
      base: Line[];
      main: Line[];
      branch: Line[];
    };

/** Splits text the way the server does: a final newline ends the last line rather than starting an empty one. */
export function splitLines(text: string | undefined): string[] {
  if (!text) return [];
  return (text.endsWith("\n") ? text.slice(0, -1) : text).split("\n");
}

const pick = (lines: string[], r: { start: number; count: number }): Line[] =>
  lines.slice(r.start, r.start + r.count).map((text, i) => ({ n: r.start + i + 1, text }));

/**
 * Lays a file out in blocks. Unchanged hunks keep `context` lines next to a change (all of a short one) and fold
 * the rest into gaps; a file without texts or hunks has no blocks.
 */
export function layout(file: BranchCompareFile, context = 3): Block[] {
  const hunks = file.hunks ?? [];
  const text = { base: splitLines(file.base.text), main: splitLines(file.main.text), branch: splitLines(file.branch.text) };
  const out: Block[] = [];
  let conflicts = 0;
  hunks.forEach((h, index) => {
    if (h.kind !== "same") {
      out.push({
        type: "hunk",
        kind: h.kind,
        index,
        ...(h.kind === "conflict" ? { conflict: conflicts++ } : {}),
        base: pick(text.base, h.base),
        main: pick(text.main, h.main),
        branch: pick(text.branch, h.branch),
      });
      return;
    }
    const n = h.main.count;
    const lead = index > 0 ? context : 0; // lines after the previous change
    const trail = index < hunks.length - 1 ? context : 0; // lines before the next change
    const slice = (from: number, count: number) => {
      const r = (s: { start: number }) => ({ start: s.start + from, count });
      out.push({ type: "hunk", kind: "same", index, base: pick(text.base, r(h.base)), main: pick(text.main, r(h.main)), branch: pick(text.branch, r(h.branch)) });
    };
    if (n <= lead + trail + 1) {
      if (n > 0) slice(0, n);
      return;
    }
    if (lead > 0) slice(0, lead);
    out.push({ type: "gap", lines: n - lead - trail });
    if (trail > 0) slice(n - trail, trail);
  });
  return out;
}

/** How many conflict hunks a file has. */
export function conflictCount(file: BranchCompareFile): number {
  return (file.hunks ?? []).filter((h) => h.kind === "conflict").length;
}

/** Why a conflicting file has no three-way view, or undefined when it has one. */
export function missingReason(file: BranchCompareFile): string | undefined {
  if (file.clean) return undefined;
  if (file.conflict === "binary" || file.base.binary || file.main.binary || file.branch.binary) return "Binary file: compare the versions outside Cadence.";
  if (file.base.cut || file.main.cut || file.branch.cut) return "Too large to show here (over 128 KiB, or past the 1 MiB a comparison carries).";
  if (!file.hunks) return "No texts to compare.";
  return undefined;
}

export type UnifiedTone = "context" | "main" | "added" | "removed" | "both";

export interface UnifiedLine {
  tone: UnifiedTone;
  /** Line number in the version the line comes from. */
  n: number;
  side: Side;
  text: string;
}

export type UnifiedPart =
  | { type: "gap"; lines: number }
  | { type: "lines"; lines: UnifiedLine[] }
  | { type: "conflict"; conflict: number; main: Line[]; base: Line[]; branch: Line[] };

/**
 * The unified view: unchanged context from main; main's own changes as they are on main; the branch's changes as
 * removed base lines and added branch lines (what a merge takes from the session); identical changes once; and every
 * conflict as its main, base and branch parts.
 */
export function unified(blocks: Block[]): UnifiedPart[] {
  const out: UnifiedPart[] = [];
  const push = (lines: UnifiedLine[]) => {
    if (lines.length === 0) return;
    const last = out.at(-1);
    if (last?.type === "lines") last.lines.push(...lines);
    else out.push({ type: "lines", lines });
  };
  const as = (tone: UnifiedTone, side: Side, lines: Line[]): UnifiedLine[] => lines.map((l) => ({ tone, side, n: l.n, text: l.text }));
  for (const b of blocks) {
    if (b.type === "gap") {
      out.push(b);
      continue;
    }
    switch (b.kind) {
      case "same":
        push(as("context", "main", b.main));
        break;
      case "main":
        push(as("main", "main", b.main));
        break;
      case "both":
        push(as("both", "main", b.main));
        break;
      case "branch":
        push([...as("removed", "base", b.base), ...as("added", "branch", b.branch)]);
        break;
      case "conflict":
        out.push({ type: "conflict", conflict: b.conflict ?? 0, main: b.main, base: b.base, branch: b.branch });
        break;
    }
  }
  return out;
}
