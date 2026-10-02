import { describe, expect, it } from "vitest";
import type { Experiment, ExperimentRun } from "@/api/gen/types.gen";
import { emptyRow, numericParams, parallelSpec, parseValues, scatterSpec, showValue, sweepBody } from "./model";

const runs = [
  { runId: "run_a1", status: "done", values: { peak_lr: 0.0001, augmentation: { profile: "clean" } }, departures: [], gpuHours: 1, best: false, createdAt: "", bestValWer: 0.3, sweepId: "swp_old" },
  { runId: "run_b2", status: "done", values: { peak_lr: 0.001, augmentation: { profile: "telephony" } }, departures: [], gpuHours: 1, best: true, createdAt: "", bestValWer: 0.25, sweepId: "swp_old" },
  { runId: "run_c3", status: "queued", values: { peak_lr: 0.0002, augmentation: { profile: "clean" } }, departures: [], gpuHours: 0, best: false, createdAt: "" },
] as unknown as ExperimentRun[];
const exp = {
  parameters: [
    { name: "peak_lr", swept: true, departs: true },
    { name: "augmentation", swept: true, departs: true },
  ],
  sweeps: [{ id: "swp_new" }, { id: "swp_old" }],
  runs,
} as unknown as Experiment;

describe("experiment charts", () => {
  it("offers only numeric parameters on the scatter and groups points by sweep", () => {
    expect(numericParams(exp, runs)).toEqual(["peak_lr"]);
    const s = scatterSpec(exp, runs, "peak_lr");
    // The oldest sweep is "sweep 1"; the queued run without a WER has no point.
    expect(s.series.map((x) => [x.label, x.points.length])).toEqual([["sweep 1", 2]]);
    expect(s.series[0]?.points[1]).toEqual({ x: 0.001, y: 0.25, label: "b2 ◆ best" });
  });
  it("draws parallel coordinates with a log axis for a wide numeric range and a category axis for objects", () => {
    const p = parallelSpec(exp, runs)!;
    expect(p.axes.map((a) => a.type)).toEqual(["log", "category", "value"]);
    expect(p.axes[1]?.categories).toEqual(['{"profile":"clean"}', '{"profile":"telephony"}']);
    expect(p.lines[1]).toMatchObject({ highlight: true, values: [0.001, '{"profile":"telephony"}', 0.25] });
    expect(p.lines[2]?.values[2]).toBeNull();
    expect(parallelSpec({ ...exp, parameters: [{ name: "x", swept: false, departs: true }] } as unknown as Experiment, runs)).toBeUndefined();
  });
});

describe("sweep form", () => {
  it("parses values as JSON, else as comma-separated words", () => {
    expect(parseValues("0.0001, 3e-4")).toEqual([0.0001, 0.0003]);
    expect(parseValues('{"profile": "clean"}, {"profile": "telephony"}')).toEqual([{ profile: "clean" }, { profile: "telephony" }]);
    expect(parseValues("clean, telephony, 2")).toEqual(["clean", "telephony", 2]);
    expect(parseValues("  ")).toEqual([]);
    expect(showValue(0.000123456)).toBe("0.0001235");
  });
  it("builds the sweeps.run body or names the first problem", () => {
    const row = { ...emptyRow(), name: "peak_lr", values: "0.0001, 0.0002" };
    expect(sweepBody({ mode: "grid", rows: [row], runs: "", cap: "4", seed: "", steps: "500" })).toEqual({
      body: { mode: "grid", parameters: [{ name: "peak_lr", values: [0.0001, 0.0002] }], gpuHourCap: 4, steps: 500 },
    });
    const ranged = { ...emptyRow(), name: "peak_lr", min: "0.00005", max: "0.001", scale: "log" as const };
    expect(sweepBody({ mode: "random", rows: [ranged], runs: "6", cap: "", seed: "7", steps: "" }).body).toEqual({
      mode: "random",
      parameters: [{ name: "peak_lr", min: 0.00005, max: 0.001, scale: "log" }],
      runs: 6,
      seed: 7,
    });
    expect(sweepBody({ mode: "grid", rows: [{ ...row, values: "" }], runs: "", cap: "", seed: "", steps: "" }).error).toContain("list the values");
    expect(sweepBody({ mode: "random", rows: [{ ...ranged, min: "0" }], runs: "", cap: "", seed: "", steps: "" }).error).toContain("log scale");
    expect(sweepBody({ mode: "grid", rows: [row], runs: "", cap: "-1", seed: "", steps: "" }).error).toContain("cap");
  });
});
