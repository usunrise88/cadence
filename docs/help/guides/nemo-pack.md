---
title: The NeMo pack
summary: The Nemotron 3.5 streaming family on the nemo-speech runtime — its step kinds, latency profiles, defaults, the conformance run on the staging card and what it measured.
contexts: [guide:nemo-pack, family:nemo.fastconformer-rnnt.cache-aware, runtime:nemo-speech]
---

## What this is

The NeMo pack (R40–R45) is the real training runtime: distribution `cadence-nemo` (`worker/packs/nemo`), installed in
the `nemo-speech` worker image (`worker/Dockerfile`, NeMo Speech 26.07 pinned by digest: NeMo 3.0.0, PyTorch 2.12,
Lhotse 1.33). It publishes the model family **`nemo.fastconformer-rnnt.cache-aware`** — Nemotron 3.5 streaming, a
cache-aware FastConformer RNN-T with a language prompt, the family the seeded base model
`base-model/nemotron-3.5-asr-streaming-0.6b` names — and five step kinds:

| Role | Step kind | Card | Does |
| --- | --- | --- | --- |
| calibrate | [`oomptimizer_calibrate`](../steps/oomptimizer-calibrate.md) | yes | OOMptimizer bucket batches under the cap + timed steps → `calibration` |
| train | [`nemotron_finetune`](../steps/nemotron-finetune.md) | yes | fine-tune on a mix → `checkpoint`, `checkpoint_best`, `training-state` |
| average | [`checkpoint_average`](../steps/checkpoint-average.md) | no | mean of checkpoints → `checkpoint` |
| transcribe | [`nemotron_transcribe`](../steps/nemotron-transcribe.md) | yes | streaming decode through NeMo's cache-aware pipeline (the live decoder) at a profile, optionally phrase-boosted by a `boost_list` → `hypotheses` |
| materialize | [`checkpoint_from_base`](../steps/checkpoint-from-base.md) | no | the base model at its pinned revision → `checkpoint` (evals of the base model) |
| live | [`nemotron_live`](../steps/nemotron-live.md) | yes (job kind `interactive`) | serves a manual transcription session: up to three targets on the live channel, nothing stored |

Live sessions and evals decode with one decoder, NeMo's cache-aware streaming pipeline with the pack's shims
(`cadence_nemo/pipeline.py`): the per-stream language prompt, the stripped locale tag, and a restore on the CPU (the
card holds 2.6 GiB per model instead of a 4.8 GiB load peak; two distinct models of a session load side by side). The
descriptor's `interactive` entry is what a session reserves on a card: 6 000 MB for one model and 2 600 MB per further
distinct model (spike A5).

Scoring is family-neutral: the core [`wer_score`](../steps/wer-score.md) kind, in every runtime image, scores the
`hypotheses`.

The family descriptor: framework `nemo`, format `.nemo` (ONNX joins with export in phase 5), 16 kHz mono input,
features computed by the model's own preprocessor (never stored), sentencepiece tokenizer (a checkpoint names the base
model's), capabilities streaming, word timestamps, confidence, phrase boosting, language prompt, train mode `finetune`,
and five latency profiles `80ms` [56,0], `160ms` [56,1] (the primary cell), `320ms` [56,3], `560ms` [56,6] and
`1120ms` [56,13] — 80 × (r + 1) ms with 4.48 s of left context. Its defaults are `packs.nemo` in `defaults.yaml`.

Registering checkpoints needs no step kind: the control plane's checkpoint hook registers every `checkpoint` output of
a run and keeps the top k by validation WER.

## Place in the loop

Train, and the decode half of Evaluate. `pipelines/train-stage` runs calibrate then fine-tune; `runs.calibrate`,
`checkpoints.average` and (phase 3) evals run the other roles.

## Fields and defaults

Every parameter of the five kinds defaults from `packs.nemo` (or `training.*`) in `defaults.yaml`; each step article
lists its table. The ones people change most: `peak_lr` (2e-4), `steps` (3000), `val_every` (500), `augmentation`
(telephony; `{profile: clean}` off), `profile` (160ms).

### The shared card

The staging card is one RTX PRO 5000 Blackwell (48 GB) with the resident vLLM service holding about 23.8 GB. A
lease's memory cap (`CADENCE_MEMORY_CAP_MB`, 22528 MiB for the staging card's 22 GB training cap) is what the whole
process may use as nvidia-smi sees it; the NeMo steps give PyTorch's allocator the cap minus `cuda_context_reserve_mb`
(1024 MiB), because the CUDA context and library workspaces sit outside the allocator.

### The conformance run

`python -m cadence_worker.conformance --runtime nemo-speech --memory-cap-mb 22528` inside the worker image on the card
(nightly on the staging host) imports the ten fixture clips (FLEURS he_il, CC-BY-4.0,
`worker/packs/nemo/cadence_nemo/fixtures`) and runs calibrate → train 6 steps → stop → resume to 9 → average →
transcribe at every profile → materialize the base model → transcribe it at 160 ms → score, every transcription
through `wer_score` with a case-folded, punctuation-stripped normalizer (from phase 3; the table below predates it and
compared lower-cased references with punctuated hypotheses). The fixtures are FLEURS **test** clips, so the scores
only prove the path works.

### Measured on the staging card (2026-09-30)

All runs inside `cadence/worker:dev` under a 22528 MiB cap (allocator 21504 MiB, fraction 0.444), vLLM resident:
vLLM stayed at 23 808 MiB in every 2 s sample and answered all 95 health checks; the step processes peaked at
21 904 MiB in nvidia-smi.

| Stage (conformance, 10 fixture clips) | Seconds | Result |
| --- | --- | --- |
| calibrate (bucket ≤ 4 s, 3 timed steps) | 100 | batch 38, 0.51 s/step |
| train 6 steps (two validations, two `.nemo` saves, one state) | 203 | loss 119 → 79, val_wer 0.357 |
| stop after the first metric | 127 | training state at step 2 (a state save takes ≈ 15 s) |
| resume to step 9 | 208 | val_wer 0.357 |
| average two checkpoints (CPU) | 23 | — |
| transcribe, per profile (model load included) | 55–64 | WER 0.74 / 0.72 / 0.63 / 0.56 / 0.58 at 80 / 160 / 320 / 560 / 1120 ms |

The fixture WER compares lower-cased references with the raw punctuated hypotheses and is on eight fine-tuning steps;
it only shows the path works (the latency trend is the expected one).

Calibration on 3 h of spike A3's Hebrew training data (default buckets, measured 12 tokens/s at p99, 20 timed steps):
bucket batches 40 / 21 / 12 / 6 / 3 / 1 for ≤ 4 / 6 / 8 / 10 / 14 / 20 s (12 s merged into 14 s, 16–18 s into 20 s),
**0.50 s per optimiser step** (σ 0.30 s between buckets) on 60 s of audio per step, compute only. Spike A3's full
Lightning loop on the same data measured 0.67 s/step with data loading and logging; the `estimates.training` row for
`blackwell-48gb`, cap 22, bf16 stays at **0.7 ± 0.2 s/step**.

## Commands

`runs.calibrate`, `runs.new`, `runs.stage`, `runs.resume`, `checkpoints.average`, `stepKinds.get`, `modelFamilies.get`.

## Playbooks

Fine-tune from a dataset version (`cadence-train` skill).

## Sources

- Spike A3, `docs/spikes/A3-nemotron-finetune.md`: the image, the model, OOMptimizer under the cap, the fine-tune and the
  streaming decoder.
- docs/spec/03-pipelines-defaults.md "Runtimes, model families and latency profiles", "Framework packs and the
  conformance suite"; docs/spec/08-resolutions.md R40–R45.
