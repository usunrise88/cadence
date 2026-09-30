---
title: oomptimizer_calibrate (step kind)
summary: The Nemotron family's calibrate role — OOMptimizer bucket batch sizes under the card's memory cap, then timed optimiser steps on the mix, written as a calibration artifact the estimate uses.
contexts: [step:oomptimizer_calibrate, artifact:calibration, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`oomptimizer_calibrate@1` fills the `calibrate` role of the Nemotron 3.5 streaming family
(`nemo.fastconformer-rnnt.cache-aware`, runtime `nemo-speech`). It needs a card (`resources.gpu`, job kind `training`).

1. Loads the base model (`base`: a `base_model` artifact, or a checkpoint) under the lease's memory cap minus
   `cuda_context_reserve_mb` (the CUDA context sits outside PyTorch's allocator).
2. **OOMptimizer**: for every duration bucket in `bucket_bins`, from the longest down, the largest batch whose training
   step and AdamW update fit — synthetic audio at the bucket's longest clip, transcripts of `tokens_per_second` × that
   length (0 measures the 99th percentile on the mix, with the language tag), the language prompt of the data. A
   bucket where not even one clip fits is dropped (it lowers the longest usable clip); neighbours with the same batch
   size merge.
3. **Timed steps**: `calibrate_warmup_steps` untimed, then `calibrate_timed_steps` real optimiser steps on the mix's
   training clips with those buckets.

It writes one `calibration` artifact (JSON) with `secondsPerStep`, `secondsPerStepStd`, `plusMinus` (2σ / mean),
`batchSize` (mean clips per step), `batchSizes` (`bucket_duration_bins`, `bucket_batch_size`), `bucketConfig` (the
requested bins, tokens per second, longest clip), the precision, the memory cap and the peak card memory. The control
plane's calibration hook caches it per base model, card class, cap and precision; estimates then answer
`basis: measured`. `nemotron_finetune` reads the buckets from it.

Stopping mid-search writes nothing (the lease is released cancelled). An `oom` here means the model alone does not fit
the cap.

## Place in the loop

Train — the first step of `pipelines/train-stage`, and `runs.calibrate` alone.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `precision` | `training.precision` (bf16) | Community fine-tune kit | bf16, fp16, fp32 |
| `bucket_bins` | `packs.nemo.bucket_bins` (4, 6, …, 20 s) | Spike A3 | 1–40 buckets |
| `tokens_per_second` | `packs.nemo.tokens_per_second` (0 = measure) | Spike A3 | 0–60 |
| `start_batch_size` | `packs.nemo.start_batch_size` (16) | Spike A3 | 1–512 |
| `search_threshold` | `packs.nemo.search_threshold` (0.05) | NeMo OOMptimizer | 0.01–0.5 |
| `timed_steps` | `packs.nemo.calibrate_timed_steps` (20) | Cadence recommendation | 1–500 |
| `warmup_steps` | `packs.nemo.calibrate_warmup_steps` (3) | Cadence recommendation | 0–50 |
| `min_duration` | `packs.nemo.min_duration` (0.5 s) | Key defaults | 0.05–10 s |
| `num_workers` | `packs.nemo.num_workers` (4) | Cadence recommendation | 0–32 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `grad_clip` | `packs.nemo.grad_clip` (5) | Key defaults | 0–100 |
| `seed` | `packs.nemo.seed` (0) | Cadence recommendation | 0–2147483647 |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |

## Commands

`runs.calibrate`, `runs.new` (through `train-stage`), `pipelines.run`.

## Playbooks

Fine-tune from a dataset version: calibrate first, then read the measured estimate before training.

## Sources

- NeMo `scripts/speech_recognition/oomptimizer.py` (v3.0.0), re-implemented in the pack with the prompt model's three
  fixes (spike A3, `docs/spikes/A3-nemotron-finetune.md` step 1).
- docs/spec/08-resolutions.md R12 (estimates), R41; the NeMo pack guide (`guides.nemo-pack`).
