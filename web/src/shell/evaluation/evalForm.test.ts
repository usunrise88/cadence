import { describe, expect, it } from "vitest";
import type { Eval, EvalPlan } from "@/api/gen/types.gen";
import { boostRef, emptyEvalForm, evalBody, formFromEval, formProblems, languagesForRun, planLine, shortAugment, shortBoost, subjectRefOf } from "./evalForm";

const subject = { checkpointId: "ckp_1" };

describe("evalBody", () => {
  it("sends only the subject while every axis is at its default", () => {
    expect(evalBody(subject, emptyEvalForm())).toEqual({ subject });
  });

  it("sends the chosen axes: boost lists with none first, augmentations after none, the languages map", () => {
    const body = evalBody(subject, {
      ...emptyEvalForm(),
      goldenSets: ["ver_g1"],
      profiles: ["160ms", "offline"],
      boosts: [
        { ref: "lang/he-IL/boost/banking.txt@abc1234", weight: "2.5" },
        { ref: "lang/he-IL/boost/names.txt@abc1234", weight: "" },
      ],
      augmentations: ["augment/telephony.yaml@abc1234"],
      languages: [
        ["sr-RS", "hr-HR"],
        ["", ""],
      ],
      baseline: " @baseline ",
    });
    expect(body).toEqual({
      subject,
      goldenSets: ["ver_g1"],
      profiles: ["160ms", "offline"],
      decoding: [{ boost: "none" }, { boost: "lang/he-IL/boost/banking.txt@abc1234", weight: 2.5 }, { boost: "lang/he-IL/boost/names.txt@abc1234" }],
      augmentations: [{ profile: "none" }, { profile: "augment/telephony.yaml@abc1234" }],
      languages: { "sr-RS": "hr-HR" },
      baseline: "@baseline",
    });
  });

  it("leaves none out when the person unticked it", () => {
    const body = evalBody(subject, { ...emptyEvalForm(), none: false, boosts: [{ ref: "lang/he-IL/boost/a.txt@abc1234", weight: "" }] });
    expect(body.decoding).toEqual([{ boost: "lang/he-IL/boost/a.txt@abc1234" }]);
  });
});

describe("formProblems", () => {
  it("catches a bad weight, a half-filled and a repeated language row", () => {
    expect(formProblems(emptyEvalForm())).toEqual([]);
    const p = formProblems({
      ...emptyEvalForm(),
      boosts: [{ ref: "lang/he-IL/boost/a.txt@abc1234", weight: "-1" }],
      languages: [
        ["sr-RS", ""],
        ["hr-HR", "sr-RS"],
        ["hr-HR", "bs-BA"],
      ],
    });
    expect(p).toHaveLength(3);
    expect(p[0]).toContain("he-IL a");
  });
});

describe("formFromEval", () => {
  it("fills the form with the eval's axes for Re-run missing cells", () => {
    const ev = {
      subject: { kind: "model", id: "ver_m", label: "m", modelKey: "k", family: "f" },
      baseline: { kind: "base_model", id: "ver_b", label: "b", modelKey: "base:ver_b", family: "f" },
      goldenSets: [
        { versionId: "ver_g1", locale: "sr-RS", decodeAs: "hr-HR" },
        { versionId: "ver_g2", locale: "sr-RS", decodeAs: "hr-HR" },
        { versionId: "ver_g3", locale: "he-IL" },
      ],
      profiles: [{ name: "160ms", latencyMs: 160 }],
      decoding: [
        { index: 0, boost: "none" },
        { index: 1, boost: "lang/sr-RS/boost/names.txt@abc1234", weight: 2 },
      ],
      augmentations: [
        { index: 0, profile: "none" },
        { index: 1, profile: "augment/noise.yaml@abc1234" },
      ],
    } as unknown as Eval;
    expect(subjectRefOf(ev)).toEqual({ modelVersionId: "ver_m" });
    const f = formFromEval(ev);
    expect(f).toEqual({
      goldenSets: ["ver_g1", "ver_g2", "ver_g3"],
      profiles: ["160ms"],
      none: true,
      boosts: [{ ref: "lang/sr-RS/boost/names.txt@abc1234", weight: "2" }],
      augmentations: ["augment/noise.yaml@abc1234"],
      languages: [["sr-RS", "hr-HR"]],
      baseline: "ver_b",
    });
    expect(evalBody(subjectRefOf(ev), f).decoding).toEqual([{ boost: "none" }, { boost: "lang/sr-RS/boost/names.txt@abc1234", weight: 2 }]);
  });
});

describe("helpers", () => {
  it("maps the project's other locales to the run's target_lang", () => {
    expect(languagesForRun(["sr-RS"], [{ param: "target_lang", value: "hr-HR" }])).toEqual([["sr-RS", "hr-HR"]]);
    expect(languagesForRun(["hr-HR"], [{ param: "target_lang", value: "hr-HR" }])).toEqual([]);
    expect(languagesForRun(["sr-RS"], [{ param: "peak_lr", value: 0.001 }])).toEqual([]);
    expect(languagesForRun(["sr-RS"], undefined)).toEqual([]);
  });

  it("names boost lists and augmentation profiles", () => {
    expect(boostRef({ path: "lang/he-IL", sha: "abcdef0123" }, "boost/banking.txt")).toBe("lang/he-IL/boost/banking.txt@abcdef0123");
    expect(shortBoost("lang/he-IL/boost/banking.txt@abcdef0123")).toBe("he-IL banking");
    expect(shortAugment("augment/telephony.yaml@abc")).toBe("telephony");
  });

  it("says what the plan computes", () => {
    const plan = {
      goldenSets: [{}, {}],
      profiles: [{}],
      decoding: [{}, {}],
      augmentations: [{}],
      baseline: { label: "base" },
      cells: [{}, {}, {}, {}, {}, {}, {}, {}],
      cellsCached: 6,
      cellsToCompute: 2,
      estimate: { gpuHours: 0.004, audioHours: 0.2 },
    } as unknown as EvalPlan;
    expect(planLine(plan)).toBe("2 golden sets × 1 profile × 2 decoding variants against base: 8 cells, 6 cached, 2 to compute · ~<0.01 GPU-h (0.20 h of audio)");
  });
});
