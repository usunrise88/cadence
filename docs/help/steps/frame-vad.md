---
title: frame_vad (step kind)
summary: Speech segments and utterance ends from NVIDIA's Frame-VAD Multilingual MarbleNet v2.0 (NeMo pack, CPU) — what latency to final is measured from.
contexts: [step:frame_vad, artifact:vad, artifact:dataset]
---

## What this is

`frame_vad@1` is a step kind of the NeMo pack (runtime `nemo-speech`, CPU, job kind `eval`). It runs NVIDIA's
Frame-VAD Multilingual MarbleNet v2.0 (`nvidia/Frame_VAD_Multilingual_MarbleNet_v2.0`, 91.5 K parameters, one speech
probability per 20 ms frame at 16 kHz) over every utterance of a dataset and writes a `vad` artifact: a header line
`{"vad": {kind, model, revision, frameMs, onset, offset, minSpeechMs, minSilenceMs}}`, then per utterance in manifest
order `{audio, durationS, speech: [[start, end], …], speechEndS}` in seconds (`speechEndS` is null without speech).

Segments: speech starts when the probability reaches `onset` and ends when it falls below `offset` (hysteresis);
pauses shorter than `min_silence_ms` are closed, segments shorter than `min_speech_ms` dropped. An utterance ends where
its last segment ends.

**Licence (R26).** The model card says "This model is ready for commercial use" and "GOVERNING TERMS: Your use of this
model is governed by the NVIDIA Open Model License Agreement". The agreement (version of October 24, 2025,
<https://www.nvidia.com/en-us/agreements/enterprise-software/nvidia-open-model-license/>) grants a perpetual,
worldwide, royalty-free licence to use the model and says "NVIDIA claims no ownership rights in outputs". It passes
R26's rule (commercial use of outputs allowed). Redistributing the model would need the agreement and the notice
"Licensed by NVIDIA Corporation under the NVIDIA Open Model License"; Cadence does not redistribute it — the worker
downloads it into its Hugging Face cache at the pinned revision. Checked 2026-10-02 at revision
`9d25eb877d9edab5101ccc843edd04787a6c72c4`.

**Languages.** The card lists Chinese, English, French, German, Russian and Spanish. Voice activity transfers across
languages far better than recognition does, but Hebrew and the other replay locales are outside its training data;
the latency summary names the model and revision it relied on.

## Place in the loop

Evaluate — `vad-g<n>a<k>` once per dataset (golden or augmented) in an eval pipeline, before the `latency_score` steps
of its streaming cells. The control plane picks the newest published step kind that turns a `dataset` into a `vad`
artifact, so a later per-channel VAD (phase 4) replaces it without a change to the eval. The step is optional in the
eval's pipeline: when it fails, the latency steps that read it are skipped, the eval still finishes and gates on WER,
and those cells show latency to final unavailable.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `model` | `packs.nemo.vad_model` (`nvidia/Frame_VAD_Multilingual_MarbleNet_v2.0`) | the model card, licence above | — |
| `revision` | `packs.nemo.vad_revision` (`9d25eb8…`) | the repository on 2026-10-02 | — |
| `onset` | `packs.nemo.vad_onset` (0.5) | the model card's example threshold | 0.05 – 0.95 |
| `offset` | `packs.nemo.vad_offset` (0.3) | Cadence recommendation | 0.05 – 0.95, ≤ onset |
| `min_speech_ms` | `packs.nemo.vad_min_speech_ms` (100) | Cadence recommendation | 0 – 2000 |
| `min_silence_ms` | `packs.nemo.vad_min_silence_ms` (200) | Cadence recommendation | 0 – 5000 |

## Commands

`evals.new` plans it; `pipelines.run` runs it directly.

## Playbooks

The fine-tune playbook evaluates through `evals.new`, which plans this step when the eval needs it.

## Sources

- docs/spec/08-resolutions.md R26 (licences of auxiliary models), R54 (utterance ends for latency to final).
- The model card: <https://huggingface.co/nvidia/Frame_VAD_Multilingual_MarbleNet_v2.0>.
