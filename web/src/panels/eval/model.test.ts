import { describe, expect, it } from "vitest";
import { baselineOf, bucketBars, bucketLabel, buildMatrix, cellTitle, decodingLabel, defaultCell, deltaForest, deltaHeatmap, progressOf, sdiBars } from "./model";
import { EVAL } from "./testdata";

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
