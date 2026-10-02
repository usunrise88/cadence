import { describe, expect, it } from "vitest";
import { alignedWords, alignmentCounts, deltaTone, evalIdOfDoc, evalItem, formatDelta, formatInterval, formatRate, parseEvalItem, registrable, textDirection } from "./format";

describe("evaluation format", () => {
  it("reads locales right to left for Hebrew and Arabic only", () => {
    expect(textDirection("he-IL")).toBe("rtl");
    expect(textDirection("ar")).toBe("rtl");
    expect(textDirection("sr-Latn")).toBe("ltr");
    expect(textDirection(undefined)).toBe("ltr");
  });

  it("formats rates and signed deltas in percentage points", () => {
    expect(formatRate(0.1234, 1)).toBe("12.3 %");
    expect(formatRate(undefined)).toBe("—");
    expect(formatDelta(-0.012)).toBe("−1.2 pp");
    expect(formatDelta(0.0004)).toBe("0.0 pp");
    expect(formatDelta(0.03)).toBe("+3.0 pp");
    expect(formatInterval({ value: -0.012, low: -0.02, high: -0.004 })).toBe("−1.2 pp [−2.0, −0.4]");
  });

  it("tones a delta by its interval, not its point estimate", () => {
    expect(deltaTone({ value: -0.01, low: -0.02, high: -0.001 })).toBe("better");
    expect(deltaTone({ value: 0.01, low: 0.001, high: 0.02 })).toBe("worse");
    expect(deltaTone({ value: -0.01, low: -0.02, high: 0.003 })).toBe("same");
    expect(deltaTone(undefined)).toBe("none");
  });

  it("turns scores ops into aligned words and counts them", () => {
    const words = alignedWords([
      ["=", "שלום", "שלום"],
      ["S", "עולם", "עולמי"],
      ["D", "גדול", null],
      ["I", null, "מאוד"],
      ["?", "x", "y"],
    ]);
    expect(words).toEqual([
      { op: "=", ref: "שלום", hyp: "שלום" },
      { op: "S", ref: "עולם", hyp: "עולמי" },
      { op: "D", ref: "גדול", hyp: "" },
      { op: "I", ref: "", hyp: "מאוד" },
      { op: "S", ref: "x", hyp: "y" },
    ]);
    expect(alignmentCounts(words)).toEqual({ match: 1, sub: 2, del: 1, ins: 1, refWords: 4, wer: 1 });
    expect(alignmentCounts([]).wer).toBe(0);
    expect(alignmentCounts([{ op: "I", ref: "", hyp: "a" }]).wer).toBe(1);
  });

  it("round-trips selection items and eval documents", () => {
    expect(evalItem("evc_1")).toBe("cell:evc_1");
    expect(evalItem("evc_1", 7)).toBe("cell:evc_1/utt:7");
    expect(parseEvalItem("cell:evc_1/utt:7")).toEqual({ cellId: "evc_1", utterance: 7 });
    expect(parseEvalItem("cell:evc_1")).toEqual({ cellId: "evc_1" });
    expect(parseEvalItem("stage:ckp_1")).toEqual({});
    expect(evalIdOfDoc("eval:evl_9")).toBe("evl_9");
    expect(evalIdOfDoc("run:run_1")).toBeUndefined();
  });

  it("registers only a checkpoint whose gate passed", () => {
    const subject = { kind: "checkpoint" as const, id: "ckp_1", label: "c", modelKey: "b3:x", family: "f" };
    const gate = { verdict: "passed" as const, gatesSha: "", checks: [], config: {}, at: "2026-10-02T00:00:00Z" };
    expect(registrable({ subject, gate })).toBe(true);
    expect(registrable({ subject })).toBe("Run the gate first");
    expect(registrable({ subject, gate: { ...gate, verdict: "failed" } })).toBe("The gate did not pass");
    expect(registrable({ subject: { ...subject, kind: "base_model" }, gate })).toMatch(/Only a checkpoint/);
  });
});
