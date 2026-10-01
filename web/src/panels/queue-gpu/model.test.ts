import { describe, expect, it } from "vitest";
import type { QueueEntry } from "@/api/gen/types.gen";
import { appendTelemetry, formatDuration, groupEntries, reorderPriority, sortEntries, splitMemory, trainingSlot, WINDOW_SECONDS, type PriorityEdit, type Sample } from "./model";

const entry = (jobId: string, over: Partial<QueueEntry> = {}): QueueEntry => ({
  jobId,
  kind: "echo",
  kindVersion: "1",
  jobKind: "data",
  state: "waiting",
  priority: 0,
  enqueuedAt: "2026-09-30T10:00:00Z",
  attempt: 1,
  ...over,
});
const lease = (host: string, card: number) => ({ id: "lse_1", workerId: "wrk_1", host, card, memoryCapMb: 24576, startedAt: "", heartbeatAt: "" });

describe("queue order", () => {
  it("puts running and stopping first, then priority, then first in", () => {
    const items = [
      entry("a", { priority: 1, enqueuedAt: "2026-09-30T10:02:00Z" }),
      entry("b", { state: "paused", priority: 9 }),
      entry("c", { priority: 1, enqueuedAt: "2026-09-30T10:01:00Z" }),
      entry("d", { state: "running", lease: lease("staging", 0) }),
      entry("e", { priority: 5 }),
    ];
    expect(sortEntries(items).map((e) => e.jobId)).toEqual(["d", "e", "c", "a", "b"]);
  });

  it("groups leased entries by card and keeps the queue for the rest", () => {
    const g = groupEntries([entry("w"), entry("r", { state: "running", lease: lease("staging", 0) }), entry("n", { state: "running", lease: lease("staging", -1) })]);
    expect(g.byCard.get("staging#0")?.map((e) => e.jobId)).toEqual(["r"]);
    expect(g.waiting.map((e) => e.jobId)).toEqual(["w"]);
    expect(g.noCard.map((e) => e.jobId)).toEqual(["n"]);
  });

  it("names the training slot holder", () => {
    const t = entry("t", { jobKind: "training", state: "running", lease: lease("staging", 0) });
    expect(trainingSlot([entry("e", { jobKind: "eval", state: "running" }), t])).toBe(t);
    expect(trainingSlot([entry("w", { jobKind: "training" })])).toBeUndefined();
  });

  it("moves an entry past its neighbour, within the contract's range", () => {
    const q = sortEntries([entry("a", { priority: 3 }), entry("b", { priority: 3, enqueuedAt: "2026-09-30T10:05:00Z" }), entry("c", { priority: 1000 })]);
    expect(q.map((e) => e.jobId)).toEqual(["c", "a", "b"]);
    expect(reorderPriority(q, "b", -1)).toEqual([{ jobId: "b", priority: 4 }]);
    // c sits at the top of the range; a ties with it and wins on the job id.
    expect(reorderPriority(q, "a", -1)).toEqual([{ jobId: "a", priority: 1000 }]);
    expect(reorderPriority(q, "a", 1)).toEqual([{ jobId: "a", priority: 2 }]);
    expect(reorderPriority(q, "c", -1)).toBeUndefined();
    expect(reorderPriority(q, "b", 1)).toBeUndefined();
  });

  const at = (min: number) => `2026-09-30T10:${String(min).padStart(2, "0")}:00Z`;
  const moved = (q: QueueEntry[], edits: PriorityEdit[] | undefined) => {
    const next = new Map((edits ?? []).map((e) => [e.jobId, e.priority]));
    return sortEntries(q.map((e) => ({ ...e, priority: next.get(e.jobId) ?? e.priority }))).map((e) => e.jobId);
  };

  it("takes the neighbour's priority when FIFO already puts the entry past it", () => {
    // b (priority 1) was enqueued before a (priority 2): priority 2 puts it right above a, not above x.
    const q = sortEntries([entry("x", { priority: 3 }), entry("a", { priority: 2, enqueuedAt: at(5) }), entry("b", { priority: 1, enqueuedAt: at(1) })]);
    expect(reorderPriority(q, "b", -1)).toEqual([{ jobId: "b", priority: 2 }]);
  });

  it("moves exactly one place among equal priorities", () => {
    const q = sortEntries(["w", "x", "a", "b", "y"].map((id, n) => entry(id, { priority: 5, enqueuedAt: at(n) })));
    // One above a alone would put b above w and x too. Raising b with w and x takes three edits; sinking a with y
    // (the one entry behind b) takes two.
    const up = reorderPriority(q, "b", -1);
    expect(up).toEqual([
      { jobId: "a", priority: 4 },
      { jobId: "y", priority: 4 },
    ]);
    expect(moved(q, up)).toEqual(["w", "x", "b", "a", "y"]);
    const down = reorderPriority(q, "x", 1);
    expect(down).toEqual([
      { jobId: "a", priority: 6 },
      { jobId: "w", priority: 6 },
    ]);
    expect(moved(q, down)).toEqual(["w", "a", "x", "b", "y"]);
    // At either end of the run one edit does it.
    expect(reorderPriority(q, "w", 1)).toEqual([{ jobId: "x", priority: 6 }]);
    expect(reorderPriority(q, "y", -1)).toEqual([{ jobId: "b", priority: 4 }]);
  });

  it("keeps the order of the entries it shifts, and prefers the moved entry's own edit on a tie", () => {
    // v (priority 6) was enqueued last: raising x to 6 would put x above it, so v rises too.
    const q = sortEntries([
      entry("v", { priority: 6, enqueuedAt: at(9) }),
      ...["x", "a", "b", "c", "d"].map((id, n) => entry(id, { priority: 5, enqueuedAt: at(n + 1) })),
    ]);
    expect(q.map((e) => e.jobId)).toEqual(["v", "x", "a", "b", "c", "d"]);
    const edits = reorderPriority(q, "b", -1);
    expect(edits).toEqual([
      { jobId: "b", priority: 6 },
      { jobId: "v", priority: 7 },
      { jobId: "x", priority: 6 },
    ]);
    expect(moved(q, edits)).toEqual(["v", "x", "b", "a", "c", "d"]);
  });
});

describe("telemetry", () => {
  it("appends in time order, replaces a repeated timestamp and drops samples older than the window", () => {
    let t = appendTelemetry({}, "staging", [{ index: 0, memoryUsedMb: 1000, memoryTotalMb: 49152, utilization: 0.5 }], 100, () => false);
    t = appendTelemetry(t, "staging", [{ index: 0, memoryUsedMb: 900, utilization: 0.1 }], 50, () => false);
    t = appendTelemetry(t, "staging", [{ index: 0, memoryUsedMb: 1100 }], 100, () => true);
    expect(t["staging#0"]!.map((s) => [s.t, s.usedMb, s.cadence])).toEqual([
      [50, 900, false],
      [100, 1100, true],
    ]);
    t = appendTelemetry(t, "staging", [{ index: 0, memoryUsedMb: 1200 }], 100 + WINDOW_SECONDS, () => true);
    expect(t["staging#0"]!.map((s) => s.t)).toEqual([100 + WINDOW_SECONDS]);
    // A card without readings adds nothing.
    expect(appendTelemetry({}, "staging", [{ index: 1 }], 1, () => false)).toEqual({});
  });

  it("splits memory between resident services and Cadence", () => {
    const s = (usedMb: number, cadence: boolean, t = 0): Sample => ({ t, usedMb, totalMb: 49152, util: null, cadence });
    expect(splitMemory([], 24576)).toBeUndefined();
    expect(splitMemory([s(24000, false)], 24576)).toEqual({ residentMb: 24000, cadenceMb: 0, estimated: false });
    // The last idle reading is resident.
    expect(splitMemory([s(24000, false), s(44000, true)], 24576)).toEqual({ residentMb: 24000, cadenceMb: 20000, estimated: true });
    // Without one, the running job is taken at its cap.
    expect(splitMemory([s(44000, true)], 24576)).toEqual({ residentMb: 44000 - 24576, cadenceMb: 24576, estimated: true });
  });
});

describe("formatDuration", () => {
  it.each([
    [undefined, "unknown"],
    [45, "45 s"],
    [600, "10 min"],
    [3600, "1 h"],
    [4800, "1 h 20 min"],
  ])("%s → %s", (s, out) => expect(formatDuration(s)).toBe(out));
});
