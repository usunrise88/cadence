import { describe, expect, it } from "vitest";
import type { BranchCompareFile, MergeHunk } from "@/api/gen/types.gen";
import { conflictCount, layout, missingReason, splitLines, unified } from "./threeway";

const r = (start: number, count: number) => ({ start, count });
const h = (kind: MergeHunk["kind"], base: [number, number], main: [number, number], branch: [number, number]): MergeHunk => ({
  kind,
  base: r(...base),
  main: r(...main),
  branch: r(...branch),
});

function file(base: string, main: string, branch: string, hunks: MergeHunk[]): BranchCompareFile {
  return {
    path: "a.txt",
    clean: false,
    conflict: "content",
    base: { exists: true, text: base },
    main: { exists: true, text: main },
    branch: { exists: true, text: branch },
    hunks,
  };
}

const lines = (n: number, prefix = "l") => Array.from({ length: n }, (_, i) => `${prefix}${i + 1}`).join("\n") + "\n";

describe("splitLines", () => {
  it("treats a final newline as the end of the last line", () => {
    expect(splitLines("a\nb\n")).toEqual(["a", "b"]);
    expect(splitLines("a\nb")).toEqual(["a", "b"]);
    expect(splitLines("")).toEqual([]);
    expect(splitLines(undefined)).toEqual([]);
    expect(splitLines("\n")).toEqual([""]);
  });
});

describe("layout", () => {
  // Ten common lines, a conflict on line 11, ten more, a branch-only change on line 22.
  const base = lines(10) + "x\n" + lines(10, "m") + "y\n";
  const main = lines(10) + "MAIN\n" + lines(10, "m") + "y\n";
  const branch = lines(10) + "BRANCH\n" + lines(10, "m") + "Y\n";
  const f = file(base, main, branch, [
    h("same", [0, 10], [0, 10], [0, 10]),
    h("conflict", [10, 1], [10, 1], [10, 1]),
    h("same", [11, 10], [11, 10], [11, 10]),
    h("branch", [21, 1], [21, 1], [21, 1]),
  ]);

  it("folds unchanged runs to three lines of context around each change", () => {
    const blocks = layout(f);
    expect(blocks.map((b) => (b.type === "gap" ? `gap ${b.lines}` : `${b.kind} ${b.main.map((l) => l.n).join(",")}`))).toEqual([
      "gap 7",
      "same 8,9,10",
      "conflict 11",
      "same 12,13,14",
      "gap 4",
      "same 19,20,21",
      "branch 22",
    ]);
  });

  it("numbers every side's lines from its own version and counts conflicts", () => {
    const c = layout(f).find((b) => b.type === "hunk" && b.kind === "conflict");
    expect(c).toMatchObject({ conflict: 0, index: 1, base: [{ n: 11, text: "x" }], main: [{ n: 11, text: "MAIN" }], branch: [{ n: 11, text: "BRANCH" }] });
    expect(conflictCount(f)).toBe(1);
  });

  it("keeps a short unchanged run whole", () => {
    const g = file("a\nb\nc\n", "a\nB\nc\n", "a\nb2\nc\n", [h("same", [0, 1], [0, 1], [0, 1]), h("conflict", [1, 1], [1, 1], [1, 1]), h("same", [2, 1], [2, 1], [2, 1])]);
    expect(layout(g).map((b) => b.type)).toEqual(["hunk", "hunk", "hunk"]);
    expect(layout(g, 0).map((b) => (b.type === "gap" ? "gap" : b.kind)), "one line is never folded").toEqual(["same", "conflict", "same"]);
    const k = file("a\nb\nc\nd\n", "a\nb\nC\nd\n", "a\nb\nc2\nd\n", [h("same", [0, 2], [0, 2], [0, 2]), h("conflict", [2, 1], [2, 1], [2, 1]), h("same", [3, 1], [3, 1], [3, 1])]);
    expect(layout(k, 0).map((b) => (b.type === "gap" ? `gap ${b.lines}` : b.kind))).toEqual(["gap 2", "conflict", "same"]);
  });

  it("lays out a file one side deleted", () => {
    const g: BranchCompareFile = { ...file("a\n", "A\n", "", [h("conflict", [0, 1], [0, 1], [0, 0])]), conflict: "modify/delete", branch: { exists: false } };
    const [b] = layout(g);
    expect(b).toMatchObject({ kind: "conflict", base: [{ text: "a" }], main: [{ text: "A" }], branch: [] });
  });
});

describe("unified", () => {
  it("shows context from main, the branch's change as removed and added lines, conflicts in three parts", () => {
    const f = file("a\nb\nc\nd\n", "a\nMAIN\nc\nd\n", "a\nBRANCH\nc\nD\n", [
      h("same", [0, 1], [0, 1], [0, 1]),
      h("conflict", [1, 1], [1, 1], [1, 1]),
      h("same", [2, 1], [2, 1], [2, 1]),
      h("branch", [3, 1], [3, 1], [3, 1]),
    ]);
    const parts = unified(layout(f));
    expect(parts).toEqual([
      { type: "lines", lines: [{ tone: "context", side: "main", n: 1, text: "a" }] },
      { type: "conflict", conflict: 0, main: [{ n: 2, text: "MAIN" }], base: [{ n: 2, text: "b" }], branch: [{ n: 2, text: "BRANCH" }] },
      {
        type: "lines",
        lines: [
          { tone: "context", side: "main", n: 3, text: "c" },
          { tone: "removed", side: "base", n: 4, text: "d" },
          { tone: "added", side: "branch", n: 4, text: "D" },
        ],
      },
    ]);
  });
});

describe("missingReason", () => {
  const base = file("a\n", "b\n", "c\n", [h("conflict", [0, 1], [0, 1], [0, 1])]);
  it("explains why a conflicting file has no three-way view", () => {
    expect(missingReason(base)).toBeUndefined();
    expect(missingReason({ ...base, conflict: "binary", hunks: undefined })).toMatch(/Binary/);
    expect(missingReason({ ...base, main: { exists: true, cut: true }, hunks: undefined })).toMatch(/Too large/);
    expect(missingReason({ ...base, clean: true })).toBeUndefined();
  });
});
