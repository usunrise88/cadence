import { isScalar, isSeq, parseDocument } from "yaml";
import { getAt, type ParamSchema, type Values } from "./schema";

// The augmentation profile (docs/spec/03-pipelines-defaults.md "Augmentation", "Profile file"): a versioned file
// `augment/<name>.yaml` in the project repository that training applies on the fly. The schema only names the
// fields; every value, description, source and safe range comes from defaults.yaml (`augment.*`), so the form, the
// agents and the step kinds read one set of numbers.
//
//   name: telephony
//   seed: 1234
//   transforms:
//     codec:      { probability: 0.5, codecs: [g711-ulaw, g711-alaw, gsm-fr, amr-nb, opus] }
//     band_limit: { probability: 0.5, cutoff_hz: 3400 }
//     level:      { probability: 0.3, gain_db: [-10, 6] }
//     speed:      { probability: 0.3, factor: [0.9, 1.1] }

export const AUGMENT_DIR = "augment/";

/** Whether a repository path is an augmentation profile. */
export function isAugmentationProfile(path: string): boolean {
  return /^augment\/[^/]+\.ya?ml$/.test(path);
}

const probability = (ref: string, title = "Probability"): ParamSchema => ({ type: "number", title, minimum: 0, maximum: 1, "x-cadence": { defaultRef: ref } });
const pair = (ref: string, title: string, step: number): ParamSchema => ({ type: "array", title, minItems: 2, maxItems: 2, items: { type: "number", multipleOf: step }, "x-cadence": { defaultRef: ref } });

export const AUGMENTATION_PROFILE_SCHEMA: ParamSchema = {
  type: "object",
  properties: {
    seed: { type: "integer", title: "Seed", minimum: 0, "x-cadence": { defaultRef: "augment.seed" } },
    transforms: {
      type: "object",
      title: "Transforms",
      properties: {
        codec: {
          type: "object",
          title: "Codec",
          description: "A lossy telephony codec, one drawn per utterance.",
          properties: {
            probability: probability("augment.codec_probability"),
            codecs: { type: "array", title: "Codecs", items: { type: "string", enum: ["g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus"] }, "x-cadence": { defaultRef: "augment.codecs" } },
          },
        },
        band_limit: {
          type: "object",
          title: "Band-limit",
          description: "Narrowband telephony: low-pass, resample to 8 kHz and back.",
          properties: {
            probability: probability("augment.band_limit_probability"),
            cutoff_hz: { type: "integer", title: "Cut-off", minimum: 1000, maximum: 8000, multipleOf: 100, "x-cadence": { defaultRef: "augment.band_limit_hz" } },
          },
        },
        level: {
          type: "object",
          title: "Level",
          description: "A random gain, drawn per utterance.",
          properties: {
            probability: probability("augment.level_probability"),
            gain_db: pair("augment.level_gain_db", "Gain range", 0.5),
          },
        },
        speed: {
          type: "object",
          title: "Speed",
          description: "Speed perturbation (tempo and pitch together).",
          properties: {
            probability: probability("augment.speed_probability"),
            factor: pair("augment.speed_factor", "Factor range", 0.01),
          },
        },
      },
    },
  },
};

/** Parses a profile file: its values, or the YAML error. */
export function parseProfile(text: string): { values: Values; error?: undefined } | { values?: undefined; error: string } {
  const doc = parseDocument(text);
  if (doc.errors.length) return { error: doc.errors[0]!.message };
  const v: unknown = doc.toJS();
  if (v === null || v === undefined) return { values: {} };
  if (typeof v !== "object" || Array.isArray(v)) return { error: "An augmentation profile is a mapping (name, seed, transforms)" };
  return { values: v as Values };
}

/**
 * The file with the form's values written into it. Comments, key order and keys the form does not know survive:
 * only the leaves the schema describes are set.
 */
export function writeProfile(text: string, values: Values, schema: ParamSchema = AUGMENTATION_PROFILE_SCHEMA): string {
  const doc = parseDocument(text.trim() ? text : "{}");
  const visit = (s: ParamSchema, path: string[]) => {
    for (const [k, p] of Object.entries(s.properties ?? {})) {
      const at = [...path, k];
      if (p.properties) {
        visit(p, at);
        continue;
      }
      const v = getAt(values, at);
      if (v === undefined) continue;
      const cur: unknown = doc.getIn(at, true);
      if (isScalar(cur) && !Array.isArray(v)) {
        cur.value = v; // keeps the scalar's comments and style
        continue;
      }
      const node = doc.createNode(v);
      if (isSeq(node)) {
        node.flow = isSeq(cur) ? !!cur.flow : true; // a new list is written inline; an existing one keeps its style
        if (isSeq(cur)) {
          node.comment = cur.comment;
          node.commentBefore = cur.commentBefore;
        }
      }
      doc.setIn(at, node);
    }
  };
  visit(schema, []);
  return doc.toString({ lineWidth: 0 });
}

/** A new profile file at the given (recommended) values. */
export function newProfile(name: string, values: Values): string {
  const header =
    '# Augmentation profile (docs/spec/03-pipelines-defaults.md "Augmentation"), applied on the fly during training.\n' +
    "# Values start from defaults.yaml (augment.*); the Recipe document edits them as a form.\n";
  return header + writeProfile(`name: ${name}\n`, values);
}
