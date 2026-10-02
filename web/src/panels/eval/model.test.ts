import { describe, expect, it } from "vitest";
import {
  augmentationLabel,
  baselineOf,
  bucketBars,
  bucketLabel,
  buildMatrix,
  cellTitle,
  decodingLabel,
  defaultCell,
  deltaForest,
  deltaHeatmap,
  ecdfPoints,
  entityBars,
  latencyBars,
  progressOf,
  robustnessHeatmap,
  sdiBars,
  stabilityBars,
  unavailableReasons,
  werEcdf,
  werLatency,
} from "./model";
import { EVAL, EVAL_STREAMING, WORST } from "./testdata";

describe("Eval report model", () => {
  it("builds the golden sets × profiles matrix with the primary cell, tones and gate checks", () => {
    const m = buildMatrix(EVAL);
    expect(m.rows.map((r) => r.name)).toEqual(["golden-set/fleurs-he", "golden-set/replay-golden-sr"]);
    expect(m.cols.map((c) => c.name)).toEqual(["80ms", "160ms"]);
    const he160 = m.cells[0]![1]!;
    expect(he160.subject?.id).toBe("evc_s_he_160");
    expect(he160.baseline?.id).toBe("evc_b_he_160");
    expect(he160.primary).toBe(true);
    expect(he160.tone).toBe("better");
    expect(he160.gate).toBe("passed");
    expect(m.cells[0]![0]!.tone).toBe("same");
    expect(m.cells[0]![0]!.primary).toBe(false);
    // The replay set runs at the primary profile only.
    expect(m.cells[1]![0]).toBeUndefined();
    expect(m.cells[1]![1]!.tone).toBe("worse");
    // Another decoding variant has no cells and no gate.
    expect(buildMatrix(EVAL, 1).cells.flat().every((c) => c === undefined)).toBe(true);
  });

  it("orders the worst gate state first for a cell", () => {
    const ev = { ...EVAL, gate: { ...EVAL.gate!, checks: [...EVAL.gate!.checks, { kind: "deletionsInsertions" as const, goldenSetVersionId: "ver_gs_he", state: "failed" as const, message: "deletions fell, insertions rose" }] } };
    const c = buildMatrix(ev).cells[0]![1]!;
    expect(c.gate).toBe("failed");
    expect(c.checks.map((x) => x.kind)).toEqual(["deletionsInsertions", "target"]);
  });

  it("opens on the first golden set's primary cell and pairs baselines", () => {
    const c = defaultCell(EVAL)!;
    expect(c.id).toBe("evc_s_he_160");
    expect(baselineOf(EVAL, c)?.id).toBe("evc_b_he_160");
    expect(cellTitle(EVAL, c)).toBe("fleurs-he · 160ms");
  });

  it("builds the heatmap of deltas in percentage points", () => {
    const h = deltaHeatmap(buildMatrix(EVAL));
    expect(h.x).toEqual(["80ms", "160ms ★"]);
    expect(h.y).toEqual(["fleurs-he", "replay-golden-sr"]);
    expect(h.colormap).toBe("diverging");
    expect(h.cells).toContainEqual({ x: 1, y: 0, value: -2 });
    expect(h.cells).toContainEqual({ x: 0, y: 1, value: null });
  });

  it("builds the forest plot from subject deltas, golden set then profile", () => {
    const f = deltaForest(EVAL);
    expect(f.rows.map((r) => r.label)).toEqual(["fleurs-he · 80ms", "fleurs-he · 160ms ★", "replay-golden-sr · 160ms ★"]);
    expect(f.rows[1]).toMatchObject({ estimate: -2, low: -3, high: -1 });
    expect(f.reference).toBe(0);
  });

  it("stacks S/D/I rates of subject and baseline at the primary profile", () => {
    const b = sdiBars(EVAL);
    expect(b.categories).toEqual(["fleurs-he · subject", "fleurs-he · baseline", "replay-golden-sr · subject", "replay-golden-sr · baseline"]);
    expect(b.series.map((s) => s.values[0])).toEqual([5, 3, 2]);
    expect(b.stacked).toBe(true);
  });

  it("draws WER by duration bucket, empty buckets as gaps", () => {
    const s = EVAL.cells![0]!.summary;
    const b = EVAL.cells![1]!.summary;
    const spec = bucketBars("fleurs-he · 160ms", s, b);
    expect(spec.categories).toEqual(["0–2 s (4)", "2–5 s (6)", "20+ s (0)"]);
    expect(spec.series[0]!.values).toEqual([15, 10, null]);
    expect(spec.series[1]!.values).toEqual([17, 12, null]);
    expect(bucketLabel({ lo: 5, hi: 10 })).toBe("5–10 s");
  });

  it("labels decoding variants and progress", () => {
    expect(decodingLabel({ index: 0, boost: "none" })).toBe("no boosting");
    expect(decodingLabel({ index: 1, boost: "lang/he-IL/boost/names.txt@0123456789abcdef", weight: 2, terms: 40 })).toBe("boost names.txt @0123456 × 2 (40 terms)");
    expect(progressOf({ progress: { cellsTotal: 4, cellsDone: 1, cellsCached: 0 } })).toBe(0.25);
    expect(progressOf({ progress: { cellsTotal: 0, cellsDone: 0, cellsCached: 0 } })).toBe(0);
  });
});

describe("Eval report: streaming, distribution and robustness charts", () => {
  const he = "ver_gs_he";
  const cellOf = (id: string) => EVAL_STREAMING.cells!.find((c) => c.id === id)!;

  it("keeps augmented cells out of the matrix and the per-profile views", () => {
    expect(buildMatrix(EVAL_STREAMING).cells[0]![1]!.subject?.id).toBe("evc_s_he_160");
    expect(defaultCell(EVAL_STREAMING)?.id).toBe("evc_s_he_160");
    expect(sdiBars(EVAL_STREAMING).series[0]!.values[0]).toBe(5);
  });

  it("builds ECDF step points in percent, ties collapsed", () => {
    expect(ecdfPoints([0.5, 0, 1, 0.5])).toEqual([
      { x: 0, y: 0 },
      { x: 0, y: 25 },
      { x: 50, y: 75 },
      { x: 100, y: 100 },
    ]);
    expect(ecdfPoints([])).toEqual([{ x: 0, y: 0 }]);
  });

  it("draws the whole distribution when the rows are every utterance, and labels a tail otherwise", () => {
    const whole = werEcdf("fleurs-he · 160ms", { rows: WORST, total: 3 });
    expect(whole.title).toBe("Per-utterance WER · fleurs-he · 160ms");
    expect(whole.yLabel).toBe("Utterances at or below (%)");
    expect(whole.series).toHaveLength(1);
    expect(whole.series[0]).toMatchObject({ label: "Subject (3)", step: true });
    expect(whole.series[0]!.points.map((p) => p.x)).toEqual([0, 0, 50, 100]);
    expect(whole.note).toBe("Subject: median 50.0 %, 33 % without errors.");
    const tail = werEcdf("fleurs-he · 160ms", { rows: WORST, total: 3 }, { rows: WORST.slice(0, 2), total: 1200 });
    expect(tail.title).toBe("Per-utterance WER (worst rows only) · fleurs-he · 160ms");
    expect(tail.yLabel).toBe("Of the rows returned (%)");
    expect(tail.series.map((s) => s.label)).toEqual(["Subject (3)", "Baseline (worst 2 of 1,200)"]);
    expect(tail.note).toContain("Baseline: the 2 worst of 1,200 utterances (evals.get returns at most 200)");
    expect(werEcdf("x", { rows: [], total: 10 }).series).toEqual([]);
  });

  it("draws WER against latency per model with the primary marked and subject intervals from the delta", () => {
    const s = werLatency(EVAL_STREAMING, he);
    expect(s.title).toBe("WER against latency · fleurs-he");
    expect(s.xType).toBe("value");
    expect(s.xMarks).toEqual([{ label: "primary 160ms", value: 160 }]);
    expect(s.series[0]!.points).toEqual([
      { x: 80, label: "80ms", y: 15, low: 13.5, high: 16 },
      { x: 160, label: "160ms", marked: true, y: 10, low: 9, high: 11 },
    ]);
    expect(s.series[1]!.points).toEqual([
      { x: 80, label: "80ms", y: 14 },
      { x: 160, label: "160ms", marked: true, y: 12 },
    ]);
    expect(s.note).toContain("baseline's WER plus the 95 % interval");
    // A wide latency range switches to a log axis; another decoding has no primary mark.
    const wide = { ...EVAL_STREAMING, profiles: [{ name: "80ms", latencyMs: 80 }, { name: "160ms", latencyMs: 1120 }] };
    expect(werLatency(wide, he).xType).toBe("log");
    expect(werLatency(EVAL_STREAMING, he, 1).series.every((x) => x.points.length === 0)).toBe(true);
  });

  it("charts latency to final per profile and model, and says why a golden set has none", () => {
    const b = latencyBars(EVAL_STREAMING, he)!;
    expect(b.categories).toEqual(["80ms · subject (n=10)", "80ms · baseline (n=10)", "160ms ★ · subject (n=10)", "160ms ★ · baseline (n=10)"]);
    expect(b.series.map((x) => x.values)).toEqual([
      [120, 130, 180, 200],
      [210, 220, 320, 360],
      [250, 260, 360, 400],
    ]);
    expect(b.note).toBe("Pace: simulated; speech end from the frame VAD.");
    expect(latencyBars(EVAL_STREAMING, "ver_gs_sr")).toBeUndefined();
    const sr = EVAL_STREAMING.cells!.filter((c) => c.goldenSetVersionId === "ver_gs_sr");
    expect(unavailableReasons(EVAL_STREAMING, "latency", sr)).toEqual([{ reason: "no VAD for sr", cells: ["replay-golden-sr · 160ms · subject", "replay-golden-sr · 160ms · baseline"] }]);
    const off = { ...cellOf("evc_s_he_80"), metrics: { latency: { scorer: "latency_score@1", available: false, reason: "no partials", measured: 0 } } };
    expect(unavailableReasons(EVAL_STREAMING, "latency", [off])).toEqual([{ reason: "no partials", cells: ["fleurs-he · 80ms · subject"] }]);
  });

  it("charts partial stability per profile, only where the scores have it", () => {
    const st = stabilityBars(EVAL_STREAMING, he)!;
    expect(st.ratio.categories).toEqual(["80ms", "160ms ★"]);
    expect(st.ratio.series.map((x) => x.values)).toEqual([
      [8, 8],
      [12, 12],
    ]);
    expect(st.edits.series[1]!.values).toEqual([0.75, 0.75]);
    expect(stabilityBars(EVAL_STREAMING, "ver_gs_sr")).toBeUndefined();
  });

  it("charts entity accuracy per class, subject against baseline, all first", () => {
    const e = entityBars("fleurs-he · 160ms", cellOf("evc_s_he_160").metrics?.entities, cellOf("evc_b_he_160").metrics?.entities)!;
    expect(e.categories).toEqual(["all (12)", "number (8)", "phone (4)"]);
    expect(e.series.map((x) => x.values)).toEqual([
      [75, 87.5, 50],
      [50, 62.5, null],
    ]);
    expect(entityBars("x", undefined, { scorer: "s", available: false, refEntities: 0, correct: 0 })).toBeUndefined();
  });

  it("builds the robustness matrix of degradations, one row per golden set, augmentation and model", () => {
    expect(augmentationLabel(EVAL_STREAMING, 1)).toBe("telephony");
    const h = robustnessHeatmap(EVAL_STREAMING)!;
    expect(h.x).toEqual(["80ms", "160ms ★"]);
    expect(h.y).toEqual(["fleurs-he · telephony · subject", "fleurs-he · telephony · baseline"]);
    expect(h.cells).toEqual([
      { x: 0, y: 0, value: null },
      { x: 1, y: 0, value: 20 },
      { x: 0, y: 1, value: null },
      { x: 1, y: 1, value: 25 },
    ]);
    expect(h.colormap).toBe("diverging");
    expect(robustnessHeatmap(EVAL)).toBeUndefined();
    expect(robustnessHeatmap(EVAL_STREAMING, 1)).toBeUndefined();
  });
});
