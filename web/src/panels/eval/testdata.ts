import type { Eval, EvalCell, EvalUtterance } from "@/api/gen/types.gen";

// A small eval: two golden sets (fleurs-he as target, replay-golden-sr scored at the primary profile only) × two
// profiles; the subject beats the baseline on fleurs-he at 160ms and the gate passed.

const summary = (wer: number, sub: number, del: number, ins: number, refWords = 100) => ({
  utterances: 10,
  refWords,
  wer,
  cer: wer / 2,
  werNoPunct: wer - 0.01,
  sub,
  del,
  ins,
  buckets: [
    { lo: 0, hi: 2, utterances: 4, refWords: 20, wer: wer + 0.05 },
    { lo: 2, hi: 5, utterances: 6, refWords: 80, wer },
    { lo: 20, utterances: 0, refWords: 0, wer: 0 },
  ],
});

const cell = (id: string, role: EvalCell["role"], gs: string, profile: string, over: Partial<EvalCell> = {}): EvalCell => ({
  id,
  role,
  goldenSetVersionId: gs,
  profile,
  decodingIndex: 0,
  decodingHash: "h",
  modelKey: role === "subject" ? "b3:sub" : "base:ver_base",
  state: "done",
  ...over,
});

export const WORST: EvalUtterance[] = [
  {
    index: 4,
    audio: "b3:a4",
    speaker: "spk1",
    durationS: 3.2,
    ref: "שלום עולם גדול",
    hyp: "שלום עולמי מאוד",
    refWords: 3,
    sub: 1,
    del: 1,
    ins: 1,
    errors: 3,
    wer: 1,
    ops: [
      ["=", "שלום", "שלום"],
      ["S", "עולם", "עולמי"],
      ["D", "גדול", ""],
      ["I", "", "מאוד"],
    ],
  },
  { index: 1, audio: "b3:a1", ref: "בוקר טוב", hyp: "בוקר", refWords: 2, sub: 0, del: 1, ins: 0, errors: 1, wer: 0.5, ops: [["=", "בוקר", "בוקר"], ["D", "טוב", ""]] },
  { index: 2, audio: "b3:a2", ref: "תודה", hyp: "תודה", refWords: 1, sub: 0, del: 0, ins: 0, errors: 0, wer: 0, ops: [["=", "תודה", "תודה"]] },
];

export const EVAL: Eval = {
  id: "evl_1",
  projectId: "prj_1",
  status: "done",
  subject: { kind: "checkpoint", id: "ckp_b", label: "he-first r2 · step 200", modelKey: "b3:sub", family: "fam", runId: "run_1" },
  baseline: { kind: "base_model", id: "ver_base", label: "base-model/fixture 2026-09-01.abc", modelKey: "base:ver_base", family: "fam", source: "project-default" },
  goldenSets: [
    { versionId: "ver_gs_he", name: "golden-set/fleurs-he", version: "2026-10-02.aaa", normalizerVersionId: "ver_n", locale: "he-IL", utterances: 10, hours: 0.5, groups: "speaker" },
    { versionId: "ver_gs_sr", name: "golden-set/replay-golden-sr", version: "2026-10-02.bbb", normalizerVersionId: "ver_n", locale: "sr", utterances: 10, hours: 0.4, groups: "utterance", replay: true },
  ],
  profiles: [
    { name: "80ms", latencyMs: 80 },
    { name: "160ms", latencyMs: 160 },
  ],
  primaryProfile: "160ms",
  decoding: [{ index: 0, boost: "none" }],
  significance: { samples: 1000, level: 0.95, seed: 1 },
  progress: { cellsTotal: 6, cellsDone: 6, cellsCached: 3 },
  estimate: { gpuHours: 0.2, audioHours: 0.9, cellsToCompute: 3, gpuHoursPerAudioHour: 0.2, basis: "table" },
  gate: {
    verdict: "passed",
    gatesSha: "abcdef1234567",
    config: {},
    at: "2026-10-02T10:00:00Z",
    checks: [
      { kind: "target", goldenSetVersionId: "ver_gs_he", goldenSet: "golden-set/fleurs-he", profile: "160ms", state: "passed", delta: { value: -0.02, low: -0.03, high: -0.01 }, message: "beats the baseline" },
      { kind: "replay", goldenSetVersionId: "ver_gs_sr", goldenSet: "golden-set/replay-golden-sr", profile: "160ms", state: "passed", threshold: 0.005, message: "no regression" },
    ],
  },
  cells: [
    cell("evc_s_he_160", "subject", "ver_gs_he", "160ms", { summary: summary(0.1, 5, 3, 2), delta: { baselineCellId: "evc_b_he_160", wer: { value: -0.02, low: -0.03, high: -0.01 }, del: { value: 0, low: -0.01, high: 0.01 }, ins: { value: 0, low: -0.01, high: 0.01 }, significant: true, groups: 5, samples: 1000, level: 0.95 } }),
    cell("evc_b_he_160", "baseline", "ver_gs_he", "160ms", { state: "cached", summary: summary(0.12, 6, 4, 2) }),
    cell("evc_s_he_80", "subject", "ver_gs_he", "80ms", { summary: summary(0.15, 8, 4, 3), delta: { baselineCellId: "evc_b_he_80", wer: { value: 0.01, low: -0.005, high: 0.02 }, del: { value: 0, low: 0, high: 0 }, ins: { value: 0, low: 0, high: 0 }, significant: false, groups: 5, samples: 1000, level: 0.95 } }),
    cell("evc_b_he_80", "baseline", "ver_gs_he", "80ms", { state: "cached", summary: summary(0.14, 7, 4, 3) }),
    cell("evc_s_sr_160", "subject", "ver_gs_sr", "160ms", { summary: summary(0.2, 10, 6, 4), delta: { baselineCellId: "evc_b_sr_160", wer: { value: 0.03, low: 0.01, high: 0.05 }, del: { value: 0, low: 0, high: 0 }, ins: { value: 0, low: 0, high: 0 }, significant: true, groups: 10, samples: 1000, level: 0.95 } }),
    cell("evc_b_sr_160", "baseline", "ver_gs_sr", "160ms", { state: "cached", summary: summary(0.17, 9, 5, 3) }),
  ],
  rev: 3,
  actor: { kind: "user", id: "usr_admin", name: "admin" },
  createdAt: "2026-10-02T09:00:00Z",
  updatedAt: "2026-10-02T10:00:00Z",
  finishedAt: "2026-10-02T09:30:00Z",
};

const latency = (p50: number, p95: number, measured = 10) => ({ scorer: "latency_score@1", available: true, pace: "simulated" as const, measured, p50Ms: p50, p95Ms: p95, maxMs: p95 + 40, meanMs: p50 + 5 });
const entities = (accuracy: number, number: number, phone?: number) => ({
  scorer: "entity_score@1",
  available: true,
  refEntities: 12,
  correct: Math.round(accuracy * 12),
  accuracy,
  classes: [
    { class: "number", refEntities: 8, hypEntities: 8, correct: Math.round(number * 8), accuracy: number },
    ...(phone === undefined ? [] : [{ class: "phone", refEntities: 4, hypEntities: 3, correct: Math.round(phone * 4), accuracy: phone }]),
  ],
});

/** EVAL with the streaming metrics, partial stability and one augmentation (telephony) of fleurs-he. */
export const EVAL_STREAMING: Eval = {
  ...EVAL,
  augmentations: [
    { index: 0, profile: "none" },
    { index: 1, profile: "augment/telephony.yaml@abc1234", seed: 7 },
  ],
  cells: [
    // An augmented cell listed first: the matrix and the per-profile charts must not pick it.
    cell("evc_s_he_160_tel", "subject", "ver_gs_he", "160ms", { augmentationIndex: 1, summary: summary(0.3, 15, 10, 5) }),
    ...EVAL.cells!.map((c): EvalCell => {
      const subject = c.role === "subject";
      if (c.goldenSetVersionId !== "ver_gs_he") return { ...c, metrics: { unavailable: [{ metric: "latency", reason: "no VAD for sr" }] } };
      const at160 = c.profile === "160ms";
      return {
        ...c,
        summary: { ...c.summary!, stability: { partialWords: 100, unstableWords: subject ? 8 : 12, ratio: subject ? 0.08 : 0.12, editsPerSecond: subject ? 0.5 : 0.75 } },
        metrics: {
          latency: at160 ? latency(subject ? 180 : 200, subject ? 320 : 360) : latency(subject ? 120 : 130, subject ? 210 : 220),
          ...(at160 ? { entities: subject ? entities(0.75, 0.875, 0.5) : entities(0.5, 0.625) } : {}),
        },
      };
    }),
  ],
  robustness: [
    { role: "subject", goldenSetVersionId: "ver_gs_he", profile: "160ms", decodingIndex: 0, augmentationIndex: 1, cellId: "evc_s_he_160_tel", wer: 0.3, werNone: 0.1, degradation: 0.2 },
    { role: "baseline", goldenSetVersionId: "ver_gs_he", profile: "160ms", decodingIndex: 0, augmentationIndex: 1, cellId: "evc_b_he_160_tel", wer: 0.37, werNone: 0.12, degradation: 0.25 },
    { role: "subject", goldenSetVersionId: "ver_gs_he", profile: "80ms", decodingIndex: 0, augmentationIndex: 1, cellId: "evc_s_he_80_tel" },
  ],
};
