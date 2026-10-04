---
title: align_reference (step kind)
summary: Word timings of a dataset's reference texts from an omniASR CTC model's emissions (omni runtime, GPU ≤ 8 GB); the golden set carries them and emission delay reads them.
contexts: [step:align_reference, artifact:alignment, artifact:dataset, registry:auxiliary, registry:golden_set]
---

## What this is

`align_reference@1` is the one step kind of the omni pack (runtime `omni`, its own image `cadence/worker-omni`, one
card, 8 GB, job kind `data`). It finds where each word of a dataset's reference texts is spoken: it runs a CTC model
over each utterance and forces the most likely CTC path through the reference (torchaudio's `forced_align`, or the
worker's own NumPy Viterbi where torchaudio lacks it). The output is an `alignment` artifact, JSON lines after a
header (`cadence.alignment/1`), keyed by the BLAKE3 hash of each audio file:

`{audio, text, language, aligned: true, words: [{index, word, start, end, score}], skipped: [index, …]}` or
`{audio, text, language, aligned: false, reason}`

`index` is the word's position in `text.split()` and `word` that token unchanged; `start` and `end` are seconds;
`score` is the mean probability of the word's characters over its frames (low means doubtful). Tokens that spell
nothing the model's vocabulary has (a dash) are listed in `skipped` and get no time.

The model is the auxiliary the `aligner` parameter names (default `auxiliary/omniasr-ctc-1b`: `facebook/omniASR-CTC-1B`
at a pinned revision, Apache-2.0), loaded for this job from the Hugging Face cache (about 3.9 GB on disk, 1.9 GB on
the card in bfloat16). MMS models and torchaudio's `MMS_FA` are CC-BY-NC and never used; NeMo Forced Aligner reads only
NeMo CTC checkpoints, so it cannot use omniASR.

**Nothing is invented.** An utterance stays unaligned, with its reason, when:

- its language is not one the aligner's payload lists (`languages`; omniASR CTC 1B covers `heb_Hebr`, `srp_Cyrl`,
  `hrv_Latn` and `bos_Latn`, checked in its repository's language list on 2026-10-04; `srp_Latn` is not in it) — no
  model is loaded for such a dataset;
- it is longer than `max_duration_s`;
- it has no text, or its audio has fewer frames than its text needs.

Emission delay then reports `n/a` for those utterances instead of a number.

## Why a second runtime

omniASR runs on fairseq2 0.6, whose native library is built against PyTorch 2.8 exactly; the NeMo Speech image ships
PyTorch 2.12 and no torchaudio. The step therefore lives in a small second image (PyTorch 2.8 with CUDA 12.8,
torchaudio 2.8, fairseq2 0.6; R45's one-off allowance: the model is loaded per job, never resident). Start its worker
with `docker compose --profile omni up -d worker-omni`.

## Place in the loop

Evaluate, once per golden set: run the bundled pipeline `align-reference` on the golden set's dataset artifact
(`pipelines.run` with `inputs.data` = `{hash, type: dataset, size}`: the golden set's `datasetHash` from
`goldenSets.get` and its size from `artifacts.get`). The output hook records the alignment against that dataset
artifact, and the golden set shows it (`goldenSets.get` → `alignment`: aligned utterances, words, the aligner, the
reasons). From then on every eval of that golden set feeds the alignment to
`latency_score`, which reports emission delay PR50/PR90 beside latency to final in the Eval report. The golden set
version itself does not change; a newer alignment (another aligner version) replaces the older one for new evals.

## Fields and defaults

| Parameter | Default | Meaning |
| --- | --- | --- |
| `aligner` | `auxiliary/omniasr-ctc-1b` (`packs.omni.align_auxiliary`) | The CTC model, an auxiliary with the `align` role the project adopted |
| `max_duration_s` | 60 (`packs.omni.align_max_duration_s`) | Longest utterance aligned in one pass (attention memory grows with the square of the length) |
| `dtype` | `bfloat16` (`packs.omni.align_dtype`) | Precision of the forward pass; the log-softmax and the path search are float32 |

## Commands

`pipelines.run` (pipeline `align-reference`), `goldenSets.get`, `evals.new`, `evals.get`.

## Playbooks

None runs it yet: align a golden set once after freezing it, before the evals whose emission delay you want.

## Sources

- docs/review/2026-10-03-phase-4-plan.md decision 8 and "Auxiliary models and licences (R26)"; docs/spec/08-resolutions.md
  R51, R54.
- Graves et al., "Connectionist Temporal Classification", ICML 2006 (the forced path is the alignment variant of its
  forward pass); torchaudio `functional.forced_align` (BSD-2-Clause).
- Omnilingual ASR Team, Meta FAIR, "Omnilingual ASR", 2025 (github.com/facebookresearch/omnilingual-asr, Apache-2.0).
