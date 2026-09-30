import { describe, expect, it } from "vitest";
import type { MetricSeriesSet } from "@/api/gen/types.gen";
import { afterStepFor, appendDelta, chartGroups, checkpointMarks, minEventStep, seriesFor, xKeyOf } from "./model";

const pt = (step: number, value: number, extra: object = {}) => ({ x: step, value, min: value, max: value, count: 1, step, ...extra });
const set = (over: Partial<MetricSeriesSet> = {}): MetricSeriesSet => ({
  runId: "run_1",
  x: "step",
  maxPoints: 1000,
  lastStep: 20,
  series: [
    { name: "loss", total: 2, binned: false, points: [pt(10, 2.5), pt(20, 2.1)] },
    { name: "val_wer", total: 1, binned: false, points: [pt(20, 0.31)] },
    { name: "custom", total: 1, binned: false, points: [pt(20, 1)] },
  ],
  checkpoints: [
    { id: "ckp_a", step: 10, valWer: 0.35, kept: true, kind: "trained" },
    { id: "ckp_b", step: 20, valWer: 0.31, kept: true, kind: "trained" },
  ],
  ...over,
});

describe("metrics model", () => {
  it("orders charts: known metrics first, others by name", () => {
    expect(chartGroups(["custom", "val_wer", "lr", "train_loss", "loss"]).map((g) => [g.key, g.names])).toEqual([
      ["loss", ["loss", "train_loss"]],
      ["val_wer", ["val_wer"]],
      ["lr", ["lr"]],
      ["custom", ["custom"]],
    ]);
    expect(chartGroups(["lr"])[0]!.log).toBe(true);
  });

  it("builds one series per run and name, keeping each run's slot", () => {
    const g = chartGroups(["loss"])[0]!;
    const s = seriesFor(g, [
      { runId: "run_1", label: "a", slot: 0, set: set() },
      { runId: "run_2", label: "b", slot: 1, set: set({ runId: "run_2" }) },
    ]);
    expect(s.map((x) => [x.id, x.label, x.slot])).toEqual([
      ["run_1:loss", "a", 0],
      ["run_2:loss", "b", 1],
    ]);
    expect(Array.from(s[0]!.x.step!)).toEqual([10, 20]);
    // Wall time is drawn as Unix seconds.
    const wall = set({ x: "wall", series: [{ name: "loss", total: 1, binned: false, points: [pt(10, 1, { x: 5, wallTime: "2026-09-30T10:00:00Z" })] }] });
    expect(Array.from(seriesFor(g, [{ runId: "r", label: "r", slot: 0, set: wall }])[0]!.x.wallTime!)).toEqual([Date.parse("2026-09-30T10:00:00Z") / 1000]);
    expect(xKeyOf("gpuHours")).toBe("gpuHours");
  });

  it("marks checkpoints at their step, the best by validation WER", () => {
    expect(checkpointMarks(set()).map((m) => [m.id, m.kind, m.x.step])).toEqual([
      ["ckp_a", "checkpoint", 10],
      ["ckp_b", "best", 20],
    ]);
    const epochs = set({ x: "epoch", series: [{ name: "loss", total: 2, binned: false, points: [pt(10, 1, { x: 0.5 }), pt(20, 1, { x: 1 })] }] });
    expect(checkpointMarks(epochs).map((m) => m.x.epoch)).toEqual([0.5, 1]);
  });

  it("appends a live delta, or asks for a full read when binned", () => {
    const delta: MetricSeriesSet = { ...set(), lastStep: 30, series: [{ name: "loss", total: 2, binned: false, points: [pt(20, 2.1), pt(30, 1.9)] }], checkpoints: [{ id: "ckp_c", step: 30, kept: false }] };
    const next = appendDelta(set(), delta)!;
    expect(next.series.find((s) => s.name === "loss")!.points.map((p) => p.step)).toEqual([10, 20, 30]);
    expect(next.lastStep).toBe(30);
    expect(next.checkpoints.map((c) => c.id)).toEqual(["ckp_a", "ckp_b", "ckp_c"]);
    expect(appendDelta(set({ series: [{ name: "loss", total: 5000, binned: true, points: [] }] }), delta)).toBeUndefined();
    expect(appendDelta(set(), { ...delta, x: "epoch" })).toBeUndefined();
    expect(appendDelta(set({ maxPoints: 2 }), delta)).toBeUndefined();
  });

  it("reads live points from before the earliest step an event carried", () => {
    expect(minEventStep([{ points: [{ step: 7 }, { step: 5 }] }, { points: [{ step: 6 }] }, undefined], undefined)).toBe(5);
    expect(minEventStep([{ points: [{ step: 9 }] }], 4)).toBe(4);
    expect(afterStepFor(set(), undefined)).toBe(20);
    expect(afterStepFor(set(), 20)).toBe(19);
    expect(afterStepFor(set(), 30)).toBe(20);
    expect(afterStepFor(set(), 0)).toBe(0);
  });
});
