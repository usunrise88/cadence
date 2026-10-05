import { afterEach, describe, expect, it } from "vitest";
import { useSelection } from "@/shell/selection/store";
import { alignTexts, basicWords, openDiffSegment, useDiffSegment } from "./segment";

afterEach(() => {
  useDiffSegment.getState().set(null);
});

describe("shadow segment diff", () => {
  it("normalises as shadow_score's basic mode", () => {
    expect(basicWords("Dobar  DAN, zovem se Ana!")).toEqual(["dobar", "dan", "zovem", "se", "ana"]);
  });

  it("aligns words with substitutions, deletions and insertions", () => {
    expect(alignTexts("a b c d", "a x c d e")).toEqual([
      ["=", "a", "a"],
      ["S", "b", "x"],
      ["=", "c", "c"],
      ["=", "d", "d"],
      ["I", "", "e"],
    ]);
    expect(alignTexts("one two", "two")).toEqual([
      ["D", "one", ""],
      ["=", "two", "two"],
    ]);
    expect(alignTexts("", "")).toEqual([]);
  });

  it("holds the segment until the active document or its selection changes", () => {
    openDiffSegment({ audio: "b3:x", ref: "a", hyp: "b", refLabel: "prod", hypLabel: "cand", label: "c1" });
    expect(useDiffSegment.getState().segment?.label).toBe("c1");
    useSelection.getState().select("eval:evl_1", "cell:evc_1/utt:2");
    expect(useDiffSegment.getState().segment).toBeNull();
  });
});
