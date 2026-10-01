// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { categoricalFromTheme, CHART_CATEGORICAL, check, checkCvd, deltaE, ratio, sequentialRamps, simulate } from "./contrast.mjs";

const css = readFileSync(new URL("../src/styles/theme.css", import.meta.url), "utf8");

describe("contrast script", () => {
  it("computes WCAG ratios", () => {
    expect(ratio("#000000", "#ffffff", "#ffffff")).toBeCloseTo(21, 0);
    expect(ratio("#ffffff", "#ffffff", "#ffffff")).toBeCloseTo(1, 5);
  });
  it("every Theming pairing passes in both modes", () => {
    const failed = (check() as { ok: boolean; label: string; mode: string }[]).filter((r) => !r.ok);
    expect(failed).toEqual([]);
  });
  it("checks every categorical chart colour on both panel backgrounds in both modes", () => {
    const rows = (check() as { label: string }[]).filter((r) => r.label.startsWith("Chart series"));
    expect(rows).toHaveLength(8 * 2 * 2);
  });
});

describe("colour-vision-deficiency simulation", () => {
  it("leaves grey unchanged and merges red and green under deuteranopia", () => {
    const grey = simulate("#777777", "deutan");
    expect(grey[0]).toBeCloseTo(grey[1], 2);
    expect(grey[1]).toBeCloseTo(grey[2], 2);
    // A red/green pair far apart in normal vision collapses under protanopia and deuteranopia.
    expect(deltaE("#d62728", "#2ca02c")).toBeGreaterThan(20);
    expect(deltaE("#c8553d", "#6b8e23", "deutan")).toBeLessThan(deltaE("#c8553d", "#6b8e23") / 2);
  });
  it("keeps neighbouring series apart, the diverging poles apart and the ramps ordered", () => {
    const failed = (checkCvd(css) as { ok: boolean }[]).filter((r) => !r.ok);
    expect(failed).toEqual([]);
  });
  it("reads nine-stop sequential ramps from theme.css", () => {
    const ramps = sequentialRamps(css) as Record<string, string[]>;
    expect(Object.keys(ramps).sort()).toEqual(["magma", "viridis"]);
    expect(ramps.magma).toHaveLength(9);
  });
  it("theme.css categorical tokens equal the script's list", () => {
    expect(categoricalFromTheme(css)).toEqual(CHART_CATEGORICAL);
  });
});
