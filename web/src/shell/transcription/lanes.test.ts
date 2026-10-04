import { describe, expect, it } from "vitest";
import type { LiveServerMessage } from "@/api/gen/types.gen";
import { compareText, alignWords } from "./align";
import { finalText, initialLive, laneText, laneWords, liveReducer, percentile, sentAt, type LiveAction, type LiveState } from "./lanes";
import { Framer, frameSamples, pcm16ToFloat, toInt16 } from "./pcm";

const msg = (m: LiveServerMessage, at = 0, sentTime?: number): LiveAction => ({ type: "message", msg: m, at, sentTime });
const run = (actions: LiveAction[], s: LiveState = initialLive) => actions.reduce(liveReducer, s);

describe("liveReducer", () => {
  it("moves through waiting (queued, loading) to live", () => {
    let s = run([{ type: "connecting" }, msg({ type: "waiting", state: "queued", position: 2, reason: "no room" })]);
    expect(s.phase).toBe("queued");
    expect(s.waiting?.position).toBe(2);
    s = run([msg({ type: "waiting", state: "loading" }), msg({ type: "started", targets: [], resampler: "p" })], s);
    expect(s.phase).toBe("live");
    expect(s.waiting).toBeUndefined();
    s = liveReducer(s, { type: "closed", code: 1000, reason: "" });
    expect(s.phase).toBe("ended");
  });

  it("replaces partials in place, appends finals that never change, per target", () => {
    const s = run([
      msg({ type: "partial", target: "A", segment: 0, seq: 1, text: "של", audioEnd: 0.16 }),
      msg({ type: "partial", target: "A", segment: 0, seq: 2, text: "שלום", audioEnd: 0.32 }),
      msg({ type: "partial", target: "B", segment: 0, seq: 1, text: "hello", audioEnd: 1.12 }),
      msg({ type: "partial", target: "A", segment: 0, seq: 1, text: "stale", audioEnd: 0.1 }), // older seq: dropped
      msg({ type: "final", target: "A", segment: 0, seq: 3, text: "שלום עולם", words: [{ word: "שלום", start: 0, end: 0.3 }, { word: "עולם", start: 0.3, end: 0.6 }], endpoint: "eou", audioEnd: 0.64, space: true }),
      msg({ type: "partial", target: "A", segment: 1, seq: 4, text: "מה", audioEnd: 0.8 }),
    ]);
    expect(s.lanes.A!.finals).toHaveLength(1);
    expect(s.lanes.A!.partial?.text).toBe("מה");
    expect(laneText(s.lanes.A)).toBe("שלום עולם מה");
    expect(finalText(s.lanes.A)).toBe("שלום עולם");
    expect(laneText(s.lanes.B)).toBe("hello");
    expect(s.lanes.B!.finals).toHaveLength(0);
  });

  it("clears the partial of the segment a final closes", () => {
    const s = run([
      msg({ type: "partial", target: "A", segment: 0, seq: 1, text: "ab", audioEnd: 0.1 }),
      msg({ type: "final", target: "A", segment: 0, seq: 2, text: "abc", words: [], endpoint: "finalize", audioEnd: 0.2, space: true }),
    ]);
    expect(s.lanes.A!.partial).toBeUndefined();
    expect(laneText(s.lanes.A)).toBe("abc");
  });

  it("joins a final with space false to the previous word (an end of utterance inside a word)", () => {
    const s = run([
      msg({ type: "final", target: "A", segment: 0, seq: 1, text: "Ма", words: [{ word: "Ма", start: 0, end: 0.08 }], endpoint: "eou", audioEnd: 0.1, space: true }),
      msg({ type: "final", target: "A", segment: 1, seq: 2, text: "дагаскар это", words: [{ word: "дагаскар", start: 0.1, end: 0.5, confidence: 0.6 }, { word: "это", start: 0.5, end: 0.7 }], endpoint: "eou", audioEnd: 0.8, space: false }),
    ]);
    expect(finalText(s.lanes.A)).toBe("Мадагаскар это");
    // Before the final: a partial with space false continues the word too (not "Ма дагаскар")
    const p = run([
      msg({ type: "final", target: "A", segment: 0, seq: 1, text: "Ма", words: [], endpoint: "eou", audioEnd: 0.1, space: true }),
      msg({ type: "partial", target: "A", segment: 1, seq: 2, text: "дагаскар", audioEnd: 0.5, space: false }),
    ]);
    expect(laneText(p.lanes.A)).toBe("Мадагаскар");
    expect(laneWords(s.lanes.A, 1)).toEqual([
      { word: "Мадагаскар", start: 1, end: 1.5, confidence: 0.6 },
      { word: "это", start: 1.5, end: 1.7, confidence: undefined },
    ]);
  });

  it("measures time to final from the send log and finalize → final", () => {
    let s = run([
      { type: "sent", audioS: 0.02, at: 1000 },
      { type: "sent", audioS: 0.04, at: 1020 },
      { type: "sent", audioS: 0.06, at: 1040 },
    ]);
    expect(sentAt(s.sent, 0.03)).toBe(1020);
    expect(sentAt(s.sent, 0.5)).toBe(1040); // past the last send: the right context was padded
    s = run([
      { type: "finalize", at: 1050 },
      msg({ type: "final", target: "A", segment: 0, seq: 1, text: "a", words: [], endpoint: "finalize", audioEnd: 0.04, space: true }, 1090),
      msg({ type: "final", target: "B", segment: 0, seq: 1, text: "b", words: [], endpoint: "finalize", audioEnd: 0.06, space: true }, 1100, 1000),
    ], s);
    expect(s.latencies).toEqual([70, 100]);
    expect(s.finalizeLatencies).toEqual([40, 50]);
  });

  it("keeps stats, round trips, errors and the summary", () => {
    const s = run([
      msg({ type: "stats", source: "worker", rtf: 0.1, audioS: 3 }),
      msg({ type: "stats", source: "relay", upP50Us: 20, downP50Us: 15 }),
      msg({ type: "pong", source: "relay", t: 100 }, 101),
      msg({ type: "pong", source: "worker", t: 100 }, 107),
      msg({ type: "error", problem: { type: "https://cadence.local/help/errors/transcription-input-invalid", title: "x", status: 422 }, fatal: true }),
      msg({ type: "summary", audioS: 4, rtf: 0.12, targets: {} }),
    ]);
    expect(s.rtt).toEqual({ relay: 1, worker: 7 });
    expect(s.relayUpP50Us).toBe(20);
    expect(s.errors).toHaveLength(1);
    expect(s.rtf).toBe(0.12);
    expect(s.audioS).toBe(4);
  });
});

describe("percentile", () => {
  it("interpolates between closest ranks", () => {
    expect(percentile([], 50)).toBeUndefined();
    expect(percentile([30, 10, 20], 50)).toBe(20);
    expect(percentile([10, 20, 30, 40], 95)).toBeCloseTo(38.5);
    expect(percentile([5], 95)).toBe(5);
  });
});

describe("compareText", () => {
  it("aligns words and counts S, D, I", () => {
    expect(compareText("A, b c!", "a x c").words.map((w) => w.op).join("")).toBe("=S=");
    expect(compareText("a b c d", "a c d e").words.map((w) => w.op).join("")).toBe("=D==I");
    const c = compareText("The cat sat on the mat.", "the big cat sit on mat");
    expect(c.sub + c.del + c.ins).toBe(3);
    expect(c.refWords).toBe(6);
    expect(c.wer).toBeCloseTo(0.5);
  });
  it("handles empty sides", () => {
    expect(compareText("", "").wer).toBe(0);
    expect(compareText("", "x").wer).toBe(1);
    expect(alignWords(["a"], []).map((w) => w.op)).toEqual(["D"]);
  });
});

describe("PCM16 framing", () => {
  it("frames channel 0 into fixed-size PCM16 frames with peak and clipping", () => {
    expect(frameSamples(48000, 20)).toBe(960);
    expect(frameSamples(44100, 20)).toBe(882);
    const f = new Framer(4);
    expect(f.push([0.5, -0.5, 1])).toEqual([]);
    const [frame] = f.push([-1, 0.25]);
    expect(Array.from(new Int16Array(frame!.pcm))).toEqual([toInt16(0.5), toInt16(-0.5), 32767, -32768]);
    expect(frame!.peak).toBe(1);
    expect(frame!.clipped).toBe(2);
    expect(toInt16(2)).toBe(32767);
    expect(Array.from(pcm16ToFloat(new Int16Array([-32768, 16384]).buffer))).toEqual([-1, 0.5]);
  });
});
