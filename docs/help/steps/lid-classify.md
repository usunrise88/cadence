---
title: lid_classify (step kind)
summary: Spoken language identification per utterance with SpeechBrain's VoxLingua107 classifier — the lid artifact the pseudo-label ensemble checks (omni runtime, GPU, about 0.6 GB).
contexts: [step:lid_classify, artifact:lid, artifact:dataset, registry:auxiliary]
---

## What this is

`lid_classify@2` is a step kind of the omni pack (runtime `omni`, image `cadence/worker-omni`, one card, 4 GB
reserved, job kind `data`). It writes a `lid` artifact: one JSON line per utterance of a `dataset` in manifest order,
`{audio, language, confidence, top: [[language, p], …], expected, model}` — `expected` is the utterance's own
language tag, `top` the `top_k` most probable languages.

The classifier is the auxiliary the `auxiliary` parameter names: role `lid`, engine `speechbrain-ecapa`, pinned
weights. The bundled one is `auxiliary/lid-voxlingua107`, SpeechBrain's ECAPA-TDNN over VoxLingua107 (107 languages,
about 21 M parameters), loaded per job. VoxLingua107's legacy codes are written as BCP 47 primary subtags (`iw` →
`he`, `jw` → `jv`), the codes the sources and Whisper use.

**Why the omni runtime.** SpeechBrain imports torchaudio at start, and torchaudio publishes no build for the
nemo-speech image's torch 2.12; the omni image's torch 2.8 and torchaudio 2.8 serve it with speechbrain 1.1 added.
A step kind lives in one runtime (R40), so the move is a new version: `lid_classify@1` (the NeMo pack, Whisper's
language token) is no longer published. Whisper's own detection is not lost: the `whisper_transcribe` member writes
it beside each text (`detectedLanguage`), and the ensemble falls back to it when the classifier is unsure or absent.

## Place in the loop

Data. Before `pseudolabel_ensemble` (its optional `lid` input) and `manifest_filter` (the LID-mismatch filter). The
project adopts `auxiliary/lid-voxlingua107` first (`projects.adopt`, an approval the admin decides).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `auxiliary` | `packs.omni.lid_auxiliary` (`auxiliary/lid-voxlingua107`) | decision 7 | an adopted auxiliary with role `lid` and engine `speechbrain-ecapa` |
| `top_k` | `packs.omni.lid_top_k` (3) | Cadence recommendation | 1 – 10 |
| `batch_size` | `packs.omni.lid_batch_size` (16) | Cadence recommendation | 1 – 128 |

An OOM retry runs at 0.75 × `batch_size` (the batch pads to its longest clip). The ensemble compares the language
with the segment's by primary subtag and treats `pseudolabel.lid_equivalents` (`[[sr, hr, bs]]`) as one language:
no classifier tells Serbian, Croatian and Bosnian apart reliably.

## Errors

An auxiliary with another engine (a Whisper auxiliary) or without pinned weights is an input error: Whisper's
detection comes from the `whisper_transcribe` member instead.

## Commands

`pipelines.run`; `projects.adopt` of the auxiliary first (an approval).

## Playbooks

"Adapt a new language" (the `pseudo-label` pipeline's `lid` step).

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Decisions taken for phase 4" 7 and the R26 table (`mms-lid` excluded,
  CC-BY-NC).
- Valk and Alumäe, "VoxLingua107: a Dataset for Spoken Language Recognition", SLT 2021 (attribution, CC-BY-4.0).
- <https://huggingface.co/speechbrain/lang-id-voxlingua107-ecapa>.
