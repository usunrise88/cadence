import { describe, expect, it } from "vitest";
import type { Defaults } from "@/api/gen/types.gen";
import { AUGMENTATION_PROFILE_SCHEMA, isAugmentationProfile, newProfile, parseProfile, writeProfile } from "./augmentation";
import { defaultInfo, departuresOf, displayValue, fieldKind, labelOf, outOfRange, recommended, setAt, type ParamSchema } from "./schema";

const defaults = {
  training: { steps: { value: 3000, unit: "steps", description: "Optimiser steps", source: "Community kit", range: { min: 1, max: 200000 } } },
  augment: {
    seed: { value: 1234, description: "Seed", source: "Cadence recommendation", range: { min: 0, max: 2147483647 } },
    codec_probability: { value: 0.5, description: "Codec share", source: "Cadence recommendation", range: { min: 0, max: 1 } },
    codecs: { value: ["g711-ulaw", "opus"], description: "Codecs", source: "docs/spec/03", range: { values: ["g711-ulaw", "g711-alaw", "opus"] } },
    band_limit_probability: { value: 0.5, description: "", source: "x", range: { min: 0, max: 1 } },
    band_limit_hz: { value: 3400, unit: "Hz", description: "", source: "ITU-T G.712", range: { min: 3000, max: 8000 } },
    level_probability: { value: 0.3, description: "", source: "x", range: { min: 0, max: 1 } },
    level_gain_db: { value: [-10, 6], unit: "dB", description: "", source: "x", range: { min: -30, max: 20 } },
    speed_probability: { value: 0.3, description: "", source: "x", range: { min: 0, max: 1 } },
    speed_factor: { value: [0.9, 1.1], description: "", source: "Ko et al. 2015", range: { min: 0.8, max: 1.2 } },
  },
} as unknown as Defaults;

const step: ParamSchema = {
  type: "object",
  properties: {
    steps: { type: "integer", "x-cadence": { defaultRef: "training.steps", default: 10, description: "inline", source: "inline" } },
    prefix: { type: "string", "x-cadence": { default: "", description: "Text before", source: "Cadence recommendation", range: { maxLength: 64 } } },
    precision: { type: "string", enum: ["bf16", "fp16"], "x-cadence": { default: "bf16", description: "Precision", source: "x" } },
    blob: { type: "object" },
  },
};

describe("parameter schemas", () => {
  it("tells how each property renders", () => {
    expect(fieldKind({ type: "integer" })).toBe("integer");
    expect(fieldKind({ type: ["number", "null"] })).toBe("number");
    expect(fieldKind({ type: "string", enum: ["a"] })).toBe("enum");
    expect(fieldKind({ type: "array", items: { type: "string", enum: ["a"] } })).toBe("multi");
    expect(fieldKind({ type: "array", minItems: 2, maxItems: 2, items: { type: "number" } })).toBe("pair");
    expect(fieldKind({ type: "object" })).toBe("json");
    expect(fieldKind(AUGMENTATION_PROFILE_SCHEMA)).toBe("object");
  });

  it("takes value, description, source and range from defaults.yaml when a defaultRef resolves", () => {
    expect(defaultInfo(step.properties!.steps!, defaults)).toEqual({ value: 3000, unit: "steps", description: "Optimiser steps", source: "Community kit", range: { min: 1, max: 200000 } });
    // Without defaults.yaml loaded the inline default stands in.
    expect(defaultInfo(step.properties!.steps!, undefined)?.value).toBe(10);
    expect(defaultInfo(step.properties!.precision!, defaults)?.range).toEqual({ values: ["bf16", "fp16"] });
  });

  it("builds the recommended values and lists departures", () => {
    expect(recommended(step, defaults)).toEqual({ steps: 3000, prefix: "", precision: "bf16" });
    expect(departuresOf(step, { steps: 500, prefix: "", precision: "fp16" }, defaults)).toEqual(["steps", "precision"]);
    expect(departuresOf(AUGMENTATION_PROFILE_SCHEMA, { transforms: { level: { gain_db: [-10, 6] }, speed: { factor: [0.95, 1.05] } } }, defaults)).toEqual(["transforms.speed.factor"]);
  });

  it("warns outside the safe range without blocking", () => {
    const hz = AUGMENTATION_PROFILE_SCHEMA.properties!.transforms!.properties!.band_limit!.properties!.cutoff_hz!;
    expect(outOfRange(hz, 2500, defaults)).toMatch(/Below/);
    expect(outOfRange(hz, 3400, defaults)).toBeUndefined();
    const gain = AUGMENTATION_PROFILE_SCHEMA.properties!.transforms!.properties!.level!.properties!.gain_db!;
    expect(outOfRange(gain, [6, -10], defaults)).toMatch(/low end/);
    expect(outOfRange(gain, [-40, 0], defaults)).toMatch(/Below/);
    const codecs = AUGMENTATION_PROFILE_SCHEMA.properties!.transforms!.properties!.codec!.properties!.codecs!;
    expect(outOfRange(codecs, ["mp3"], defaults)).toMatch(/Outside/);
  });

  it("sets nested values immutably and labels keys", () => {
    const v = { a: { b: 1 } };
    const next = setAt(v, ["a", "c", "d"], 2);
    expect(next).toEqual({ a: { b: 1, c: { d: 2 } } });
    expect(v).toEqual({ a: { b: 1 } });
    expect(labelOf("cutoff_hz", {})).toBe("Cutoff hz");
    expect(labelOf("batchSize", {})).toBe("Batch Size");
    expect(displayValue([0.9, 1.1])).toBe("0.9, 1.1");
    expect(displayValue(3400, "Hz")).toBe("3400 Hz");
  });
});

describe("augmentation profile files", () => {
  it("recognises augment/*.yaml", () => {
    expect(isAugmentationProfile("augment/telephony.yaml")).toBe(true);
    expect(isAugmentationProfile("augment/clean.yml")).toBe(true);
    expect(isAugmentationProfile("augment/sub/x.yaml")).toBe(false);
    expect(isAugmentationProfile("pipelines/train-stage.yaml")).toBe(false);
  });

  it("writes the form's values and keeps comments, order and unknown keys", () => {
    const text = "# my profile\nname: calls\nseed: 7 # fixed\nnotes: keep me\ntransforms:\n  codec:\n    probability: 0.2\n    extra: 1\n";
    const out = writeProfile(text, { seed: 8, transforms: { codec: { probability: 0.4, codecs: ["opus"] } } });
    expect(out).toContain("# my profile");
    expect(out).toContain("seed: 8 # fixed");
    expect(out).toContain("notes: keep me");
    expect(out).toContain("extra: 1");
    expect(out).toContain("codecs: [ opus ]");
    expect(parseProfile(out).values).toMatchObject({ name: "calls", seed: 8, transforms: { codec: { probability: 0.4, codecs: ["opus"], extra: 1 } } });
  });

  it("creates a new profile at the recommended values and rejects what is not a mapping", () => {
    const text = newProfile("telephony", recommended(AUGMENTATION_PROFILE_SCHEMA, defaults));
    const v = parseProfile(text).values!;
    expect(v).toMatchObject({ name: "telephony", seed: 1234, transforms: { band_limit: { probability: 0.5, cutoff_hz: 3400 }, speed: { factor: [0.9, 1.1] } } });
    expect(departuresOf(AUGMENTATION_PROFILE_SCHEMA, v, defaults)).toEqual([]);
    expect(parseProfile("- a\n- b\n").error).toMatch(/mapping/);
    expect(parseProfile("a: [").error).toBeTruthy();
    expect(parseProfile("").values).toEqual({});
  });
});
