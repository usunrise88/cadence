---
title: toy_calibrate (step kind)
summary: The toy pack's calibrate role — times a few optimiser steps of the tiny CTC model and writes a calibration artifact.
contexts: [step:toy_calibrate]
---

## What this is

`toy_calibrate@1` fills the `calibrate` role of the `toy-ctc` family (runtime `toy`, CPU). It reads a `dataset`
(input `data`), runs one warm-up step and then `steps` timed optimiser steps at `batch_size` (times the batch scale
of an OOM retry), and writes a `calibration` artifact: `{family, batchSize, secondsPerStep, stepsMeasured, device,
precision, memoryCapMb, leaseOverheadSeconds}` (the last: loading plus one checkpoint and state save, timed). The final metric `seconds_per_step` travels with the outcome. The toy pack exists only to
keep the framework seams honest (R45); its numbers say nothing about real models.

## Place in the loop

Train — before a run, to switch its estimate from the table to a measured basis. In CI, the first stage of the
conformance suite.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `batch_size` | `packs.toy.batch_size` (8) | Cadence recommendation | 1–64 utterances |
| `steps` | `packs.toy.calibrate_steps` (3) | Cadence recommendation | 1–50 steps |

## Commands

`pipelines.run`; `runs.calibrate` once runs use a toy base model.

## Playbooks

None.

## Sources

docs/spec/08-resolutions.md R12 (calibration), R45 (the toy pack); defaults in `defaults.yaml` section `packs.toy`.
