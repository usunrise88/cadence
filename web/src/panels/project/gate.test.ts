import { describe, expect, it } from "vitest";
import { departureLine, evaluationSummary, gateETag, gateRows, gateSource } from "./gate";

describe("gate", () => {
  it("reads the effective gate as rows", () => {
    const rows = Object.fromEntries(
      gateRows({
        primaryProfile: "160ms",
        target: { goldenSets: ["golden-set/he-calls"], rule: "beat-baseline" },
        replay: { goldenSets: ["golden-set/replay-*"], maxRegression: 0.005 },
        deletionsInsertions: true,
        significance: { samples: 1000, level: 0.95, seed: 7 },
      }),
    );
    expect(rows["Target"]).toBe("golden-set/he-calls · beat-baseline");
    expect(rows["Replay (no regression)"]).toBe("golden-set/replay-* · at most +0.5 pp");
    expect(rows["Significance"]).toBe("1000 resamples · 95 % interval · seed 7");
  });

  it("splits unnamed sets by language, and names no replay once a target is named", () => {
    const unnamed = Object.fromEntries(gateRows({ replay: { maxRegression: 0.01 } }));
    expect(unnamed["Target"]).toContain("project's languages");
    expect(unnamed["Replay (no regression)"]).toBe("golden sets in other languages · at most +1 pp");
    expect(Object.fromEntries(gateRows({ target: { goldenSets: ["golden-set/a"] } }))["Replay (no regression)"]).toBe("none");
  });

  it("knows its If-Match and source", () => {
    expect(gateETag({ exists: false })).toBe("defaults");
    expect(gateETag({ exists: true, commit: "abc123" })).toBe("abc123");
    expect(gateSource({ exists: false, path: "gates.yaml" })).toBe("No gates.yaml: the defaults apply");
    expect(gateSource({ exists: true, commit: "abcdef0123", path: "gates.yaml" })).toBe("gates.yaml at abcdef0");
    expect(departureLine({ param: "replay.maxRegression", value: 0.01, default: 0.005 })).toBe("replay.maxRegression: 0.01 (default 0.005)");
  });

  it("summarises the Evaluation block", () => {
    expect(evaluationSummary(0, [])).toEqual({ count: 0, text: "0 golden sets adopted · 0 evals", verdict: undefined });
    const s = evaluationSummary(1, [{ status: "running" }, { status: "done", gate: { verdict: "failed" } }, { status: "done", gate: { verdict: "passed" } }]);
    expect(s).toEqual({ count: 3, text: "1 golden set adopted · 3 evals · 1 running", verdict: "failed" });
  });
});
