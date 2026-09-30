---
title: toy_train (step kind)
summary: The toy pack's train role — a few CTC optimiser steps on CPU, with loss, lr and validation WER metrics, a checkpoint and a training state.
contexts: [step:toy_train]
---

## What this is

`toy_train@1` fills the `train` role of the `toy-ctc` family: a linear layer and a unidirectional GRU with a CTC head
(about 60 k parameters) over log-mel features computed on the fly from a `dataset` (input `data`). It posts the
metrics `loss`, `lr` and `val_wer` and writes two outputs:

- `checkpoint` (type `checkpoint`, a directory: `model.pt`, `config.json`, `tokenizer.json`) with the neutral meta
  `family`, `step`, `valWer` and `weightsHash` (R42);
- `state` (type `training-state`: weights, optimiser and step), used only to resume.

When the lease carries `overrides.resumeFrom`, it continues from that training state up to `steps` in total. Asked to
stop (cancel, pause, a closing window), it writes the training state and returns; the lease is released as cancelled
with that output.

## Place in the loop

Train — the conformance suite's train, stop and resume stages; never a real model.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `steps` | `packs.toy.train_steps` (300) | Cadence recommendation | 1–5000 steps |
| `learning_rate` | `packs.toy.learning_rate` (0.003) | Cadence recommendation | 0.00001–0.1 |
| `batch_size` | `packs.toy.batch_size` (8) | Cadence recommendation | 1–64 utterances |
| `val_every` | `packs.toy.val_every` (20) | Cadence recommendation | 1–5000 steps |
| `seed` | `packs.toy.seed` (0) | Cadence recommendation | 0–2147483647 |

## Commands

`pipelines.run`, `pipelineRuns.cancel` (stops at a training-state boundary), `pipelineRuns.retry`.

## Playbooks

None.

## Sources

docs/spec/08-resolutions.md R42 (neutral checkpoint and training-state), R44, R45.
