---
title: Estimate unavailable
summary: Cadence cannot estimate the run — the estimate table in defaults.yaml has no row for this base model, card class, memory cap and precision.
contexts: [error:estimate-unavailable, field:estimate]
---

## What this is

A `422` problem of type `estimate-unavailable`: `runs.new?dryRun=true` needs seconds per training step for the
combination the run would use, and nothing answers it. Until a card is calibrated (`runs.calibrate`, phase 2) the
estimate comes from the table in `defaults.yaml` (`estimates.training`), keyed by

- the base model's registry collection (`base-model/nemotron-3.5-asr-streaming-0.6b`),
- the card class of the chosen compute card (`blackwell-48gb`),
- the card's memory cap in GB (24 on the shared staging card),
- the precision (`bf16`).

A different memory cap (after `compute.edit`), another base model or another precision has no row until someone
adds one.

## Place in the loop

Every spending command shows its estimate first (docs/spec/08-resolutions.md R12); approvals and budgets read it.
Without an estimate the run cannot be checked against the project's daily GPU-hour budget. Nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/estimate-unavailable` |
| `status` | `422` |
| `detail` | The base model, card class, memory cap and precision that have no row |

Each table row carries `seconds_per_step`, `plus_minus` (relative uncertainty), a description and its source. An
estimate built from it says `basis: table`; one from a calibration will say `basis: measured`.

## Commands

- `defaults.get` — the estimate table as the server runs with it.
- `compute.get` — the card class and memory cap of each card.
- `runs.new?dryRun=true` with `precision`, `compute` or `baseModel` set to a combination the table covers.

## Playbooks

- Put the card back to the memory cap the table knows, or add a row to `defaults.yaml` with its source (a
  measurement, or "Cadence recommendation" until one exists).
- From phase 2: calibrate the card once (`runs.calibrate`) and the estimate uses the measured speed.

## Sources

- docs/spec/08-resolutions.md R12 — the estimate model.
- docs/spikes/A3-nemotron-finetune.md — the measurement that replaces the placeholder row.
