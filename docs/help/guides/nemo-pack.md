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
`base-model/nemotron-3.5-asr-streaming-0.6b` names — and four step kinds:

| Role | Step kind | Card | Does |
| --- | --- | --- | --- |
| calibrate | [`oomptimizer_calibrate`](../steps/oomptimizer-calibrate.md) | yes | OOMptimizer bucket batches under the cap + timed steps → `calibration` |
| train | [`nemotron_finetune`](../steps/nemotron-finetune.md) | yes | fine-tune on a mix → `checkpoint`, `checkpoint_best`, `training-state` |
| average | [`checkpoint_average`](../steps/checkpoint-average.md) | no | mean of checkpoints → `checkpoint` |
| transcribe | [`nemotron_transcribe`](../steps/nemotron-transcribe.md) | yes | cache-aware streaming decode at a profile → `hypotheses` |

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

## The shared card

The staging card is one RTX PRO 5000 Blackwell (48 GB) with the resident vLLM service holding about 23.8 GB. The
lease's memory cap (`CADENCE_MEMORY_CAP_MB`) is what the whole process may use as nvidia-smi sees it; the NeMo steps
give PyTorch's allocator the cap minus `cuda_context_reserve_mb` (1024 MiB), because the CUDA context and library
workspaces sit outside the allocator. A 24 GB cap does not fit beside vLLM on this card; spike A3 and the conformance
run used a 20.5 GiB allocator (≈ 21.6 GB in nvidia-smi).

## The conformance run

`python -m cadence_worker.conformance --runtime nemo-speech` inside the worker image on the card (nightly on the
staging host) imports the ten fixture clips (FLEURS he_il, CC-BY-4.0, `worker/packs/nemo/cadence_nemo/fixtures`) and
runs calibrate → train 6 steps → stop → resume to 9 → average → transcribe at every profile → score. The fixtures are
FLEURS **test** clips, so the scores only prove the path works.

## Commands

`runs.calibrate`, `runs.new`, `runs.stage`, `runs.resume`, `checkpoints.average`, `stepKinds.get`, `modelFamilies.get`.

## Playbooks

Fine-tune from a dataset version (`cadence-train` skill).

## Sources

- Spike A3, `docs/spikes/A3-nemotron-finetune.md`: the image, the model, OOMptimizer under the cap, the fine-tune and the
  streaming decoder.
- docs/spec/03-pipelines-defaults.md "Runtimes, model families and latency profiles", "Framework packs and the
  conformance suite"; docs/spec/08-resolutions.md R40–R45.
