import { describe, expect, it } from "vitest";
import type { AgentModels, Defaults } from "@/api/gen/types.gen";
import { departures, formatTokens, projectNewBody, recommended, slugify, templateNames } from "./wizard";

const param = (value: unknown) => ({ value, description: "d", source: "s" });
const defaults = {
  version: 1,
  wizard: {
    locale: param("he-IL"),
    domain: param("telephony"),
    base_model: param("base-model/nemotron-3.5-asr-streaming-0.6b"),
    driver: param("claude-code"),
    claude_code_model: param("sonnet"),
    opencode_model: param("minimax/MiniMax-M2"),
    permission_preset: param("guardrails-default"),
    instructions_template: param("default"),
    repository: param("internal"),
  },
  budgets: { gpu_hours_per_project_per_day: param(8), agent_tokens_per_project_per_day: param(20000000) },
} as unknown as Defaults;
const catalogue: AgentModels[] = [
  { driver: "claude-code", default: "sonnet", models: [{ id: "sonnet", name: "Sonnet" }], freeForm: false, description: "", source: "" },
  { driver: "opencode", default: "minimax/MiniMax-M2", models: [], freeForm: true, description: "", source: "" },
];

describe("project wizard", () => {
  const rec = recommended(defaults, catalogue);

  it("fills Recommended mode from defaults.yaml", () => {
    expect(rec).toMatchObject({ locale: "he-IL", driver: "claude-code", model: "sonnet", repoKind: "internal", gpuHoursPerDay: "8" });
  });

  it("sends only name, slug and locale when nothing was customised", () => {
    expect(projectNewBody({ ...rec, name: "Hebrew telephony" }, rec)).toEqual({ name: "Hebrew telephony", slug: "hebrew-telephony", locales: ["he-IL"] });
    expect(departures({ ...rec, name: "x" }, rec)).toEqual([]);
  });

  it("sends each departure from the defaults", () => {
    const v = { ...rec, name: "Balkans", locale: "sr-RS", driver: "opencode" as const, model: "minimax/MiniMax-M2", repoKind: "url" as const, repoUrl: "https://example.org/r.git", gpuHoursPerDay: "4" };
    expect(projectNewBody(v, rec)).toEqual({
      name: "Balkans",
      slug: "balkans",
      locales: ["sr-RS"],
      agent: { driver: "opencode", model: "minimax/MiniMax-M2", permissionPreset: "guardrails-default" },
      repository: { kind: "url", url: "https://example.org/r.git" },
      budgets: { gpuHoursPerDay: 4 },
    });
    expect(departures(v, rec)).toEqual(["driver", "model", "repoKind", "gpuHoursPerDay"]);
  });

  it("derives a valid slug", () => {
    expect(slugify("  Hebrew — Telephony 2026 ")).toBe("hebrew-telephony-2026");
    expect(slugify("2026 calls")).toBe("calls");
  });

  it("names templates from their collections", () => {
    expect(templateNames(["template/preset-read-only", "template/preset-guardrails-default", "template/preset-read-only", "template/skill-x"], "preset")).toEqual([
      "guardrails-default",
      "read-only",
    ]);
    expect(formatTokens(20000000)).toBe("20M");
  });
});
