import { describe, expect, it } from "vitest";
import type { Defaults } from "@/api/gen/types.gen";
import { formatDefault, formatRange, lookupDefault, rangeWarning } from "./defaults";

const d = {
  budgets: { agent_turns_per_session: { value: 200, unit: "turns", description: "d", source: "s", range: { min: 1, max: 2000 } } },
  wizard: { driver: { value: "claude-code", description: "d", source: "s", range: { values: ["claude-code", "opencode"] } } },
} as unknown as Defaults;

describe("defaults", () => {
  it("looks up <section>.<key>", () => {
    expect(lookupDefault(d, "budgets.agent_turns_per_session")?.value).toBe(200);
    expect(lookupDefault(d, "budgets.nope")).toBeUndefined();
    expect(lookupDefault(d, "nope.x")).toBeUndefined();
    expect(lookupDefault(undefined, "budgets.agent_turns_per_session")).toBeUndefined();
  });
  it("formats defaults and ranges", () => {
    expect(formatDefault(lookupDefault(d, "budgets.agent_turns_per_session")!)).toBe("200 turns");
    expect(formatRange({ min: 1, max: 2000 }, "turns")).toBe("1–2000 turns");
    expect(formatRange({ min: 1 })).toBe("≥ 1");
    expect(formatRange({ values: ["a", "b"] })).toBe("a · b");
  });
  it("warns outside the safe range instead of blocking", () => {
    expect(rangeWarning(0, { min: 1, max: 2000 })).toBe("Below the safe range (min 1)");
    expect(rangeWarning(2001, { min: 1, max: 2000 })).toBe("Above the safe range (max 2000)");
    expect(rangeWarning(20, { min: 1, max: 2000 })).toBeUndefined();
    expect(rangeWarning("codex", { values: ["claude-code", "opencode"] })).toContain("Outside");
    expect(rangeWarning(5, undefined)).toBeUndefined();
  });
});
