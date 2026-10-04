---
title: lid_classify (step kind)
summary: Spoken language identification per utterance with the classifier auxiliary the project adopted — the lid artifact the pseudo-label ensemble checks (NeMo pack runtime, GPU ≤ 8 GB).
contexts: [step:lid_classify, artifact:lid, artifact:dataset, registry:auxiliary]
---

## What this is

`lid_classify@1` is a step kind of the NeMo pack (runtime `nemo-speech`, one card, 8 GB, job kind `data`). It writes a
`lid` artifact: one JSON line per utterance of a `dataset` in manifest order,
`{audio, language, confidence, top: [[language, p], …], expected, model}` — `expected` is the utterance's own
language tag, `top` the `top_k` most probable languages.

The classifier is the auxiliary the `auxiliary` parameter names (role `lid`), by its payload's `engine`:

| Engine | Auxiliary | What it does |
| --- | --- | --- |
| `transformers-whisper` | `auxiliary/whisper-large-v3` (the default) | Whisper's language token after the start token: a distribution over its languages from the first 30 s, loaded per job in fp16 |
| `speechbrain-ecapa` | `auxiliary/lid-voxlingua107` | SpeechBrain's ECAPA-TDNN over VoxLingua107 (107 languages) |

**The nemo-speech runtime cannot run the VoxLingua107 classifier.** SpeechBrain 1.1 imports torchaudio at start, and
torchaudio publishes no build for the image's torch 2.12 (its newest, 2.11, pins torch 2.11; checked 2026-10-03). The
step then fails with an input error naming the fallback, which is why `packs.nemo.lid_auxiliary` defaults to Whisper.
A runtime with torchaudio (a small second image, as the omniASR aligner may need) would serve it unchanged.

## Place in the loop

Data. Before `pseudolabel_ensemble` (its optional `lid` input) and `manifest_filter` (the LID-mismatch filter).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `auxiliary` | `packs.nemo.lid_auxiliary` (`auxiliary/whisper-large-v3`) | decision 7, with the fallback above | an adopted auxiliary with role `lid` |
| `top_k` | `packs.nemo.lid_top_k` (3) | Cadence recommendation | 1 – 10 |
| `batch_size` | `packs.nemo.lid_batch_size` (8) | Cadence recommendation | 1 – 128 |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | spike A3 | 0 – 8192 |

Language codes are the classifier's own (`sr`, `hr`, `he`, …). The ensemble compares them with the segment's language
by primary subtag and treats `pseudolabel.lid_equivalents` (`[[sr, hr, bs]]`) as one language: neither classifier
tells Serbian, Croatian and Bosnian apart reliably.

## Commands

`pipelines.run`; `projects.adopt` of the auxiliary first (an approval).

## Playbooks

"Adapt a new language" (phase 4, wave 3).

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Decisions taken for phase 4" 7 and the R26 table (`mms-lid` excluded,
  CC-BY-NC).
- Valk and Alumäe, "VoxLingua107: a Dataset for Spoken Language Recognition", SLT 2021.
- <https://huggingface.co/speechbrain/lang-id-voxlingua107-ecapa>, <https://huggingface.co/openai/whisper-large-v3>.
