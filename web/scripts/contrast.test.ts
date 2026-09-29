// @vitest-environment node
import { describe, expect, it } from "vitest";
import { check, ratio } from "./contrast.mjs";

describe("contrast script", () => {
  it("computes WCAG ratios", () => {
    expect(ratio("#000000", "#ffffff", "#ffffff")).toBeCloseTo(21, 0);
    expect(ratio("#ffffff", "#ffffff", "#ffffff")).toBeCloseTo(1, 5);
  });
  it("every Theming pairing passes in both modes", () => {
    const failed = (check() as { ok: boolean; label: string; mode: string }[]).filter((r) => !r.ok);
    expect(failed).toEqual([]);
  });
});
