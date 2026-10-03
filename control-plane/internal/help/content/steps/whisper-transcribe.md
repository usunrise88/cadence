---
title: whisper_transcribe (step kind)
summary: A Whisper pseudo-label member, loaded for one job from the auxiliary the project adopted (NeMo pack runtime, GPU ≤ 8 GB).
contexts: [step:whisper_transcribe, artifact:hypotheses, artifact:dataset, registry:auxiliary]
---

## What this is

`whisper_transcribe@1` is a step kind of the NeMo pack (runtime `nemo-speech`, one card, 8 GB, job kind `data`). It
decodes every utterance of a `dataset` with a Whisper model and writes `hypotheses`, one JSON line per utterance keyed
by the BLAKE3 hash of its audio (the segment's identity):

`{audio, text, member, language, detectedLanguage, languageConfidence, model: {auxiliary, versionId, hfRepo,
revision}, decodingHash}`

The weights are those of the auxiliary the `auxiliary` parameter names, resolved by the control plane to the version
the project adopted: `hfRepo` at its pinned `revision`, read from the Hugging Face cache (`HF_HOME`, or a read-only
cache in `CADENCE_HF_READONLY_CACHES`) and downloaded there only once. They are loaded through transformers in fp16
for the length of the job (R45's one-off allowance; no resident service): large-v3 takes about 3.5 GB of the card.

The language is forced to each utterance's (or `target_lang`), task `transcribe`; audio longer than 30 s decodes
alone, long-form with timestamps. With `detect_language`, the step also records Whisper's own language identification
— the distribution over its language tokens after the start token — as `detectedLanguage` and `languageConfidence`:
the ensemble's second opinion on LID.

| Auxiliary | Languages | Conditions (licence check 2026-10-03) |
| --- | --- | --- |
| `auxiliary/whisper-large-v3` | all of Whisper's | Apache-2.0; nothing on outputs |
| `auxiliary/whisper-he-ivrit` | Hebrew only | Apache-2.0 weights, ivrit.ai data CC-BY-4.0: attribute ivrit.ai; never for voice cloning; Hebrew audio only (its LID is weakened) |

## Place in the loop

Data. One member of the pseudo-label ensemble (`pseudolabel_ensemble`), beside `oasis_transcribe` and
`nemotron_transcribe`, on segments without text.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `auxiliary` | `packs.nemo.whisper_auxiliary` (`auxiliary/whisper-large-v3`) | the R26 table | an adopted auxiliary with role `pseudolabel` |
| `batch_size` | `packs.nemo.whisper_batch_size` (8) | Cadence recommendation | 1 – 64 |
| `num_beams` | `packs.nemo.whisper_num_beams` (1, greedy) | Cadence recommendation | 1 – 8 |
| `detect_language` | `packs.nemo.whisper_detect_language` (true) | decision 7 | true, false |
| `target_lang` | empty (each utterance's language) | Cadence recommendation | a BCP-47 tag |
| `transliterate` | empty | as `dataset_import` | `""`, `sr-Cyrl-Latn` |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | spike A3 | 0 – 8192 |

Whisper writes Serbian in Cyrillic: set `transliterate: sr-Cyrl-Latn` when the base model is trained on Latin (as
`dataset_import` does), or the ensemble compares two scripts and every segment is a disagreement.

## Commands

`pipelines.run`; `projects.adopt` of the auxiliary first (an approval).

## Playbooks

"Adapt a new language" (phase 4, wave 3).

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Owner decisions" 1, "Decisions taken for phase 4" 5–7 and the R26 table.
- Radford et al., "Robust Speech Recognition via Large-Scale Weak Supervision", 2022.
- <https://huggingface.co/openai/whisper-large-v3>, <https://huggingface.co/ivrit-ai/whisper-large-v3-turbo>.
