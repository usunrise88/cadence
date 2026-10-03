---
title: speaker_disjoint_split (step kind)
summary: Assigns train, validation and test so that one speaker (or one call) is never on two sides; stable across re-runs.
contexts: [step:speaker_disjoint_split, artifact:segments]
---

## What this is

`speaker_disjoint_split@1` is a runtime-neutral core step kind (CPU, job kind `data`). It sets `split` on every segment
and `splitRule: speaker-disjoint` in the header. Segments are grouped — by speaker, or for a segment without a speaker
by its source file (one call is one caller); with `group_by: file` always by file, with `group_by: text` by the
transcript — and a whole group goes to one side, chosen by a stable hash of its key: below `test_share` test, below
`test_share + validation_share` validation, else train. A re-run assigns the same. When validation holds fewer than
`min_validation_utterances` segments, whole groups move over from train, next in line by that hash, never past half the
segments (the same rule as `dataset_import`).

## Place in the loop

Data — `pipelines/data-ingest.yaml`, after `manifest_filter`, before `dataset_freeze`. Golden sets come from annotation
batches, so `test_share` is usually 0; utterances in any golden set are excluded at `datasets.freeze` by the
leakage check, not here.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `validation_share` | `data.validation_share` (0.02) | Cadence recommendation | 0–0.5 |
| `test_share` | `0` | Cadence recommendation | 0–0.5 |
| `min_validation_utterances` | `data.min_validation_utterances` (100) | phase-2 rehearsal | 0–100000 |
| `group_by` | `speaker` | spec 04 Block 1 (speaker- and source-disjoint) | `speaker`, `file`, `text` |

## Commands

- `pipelines.run` with `data-ingest`.
- `datasets.preview` — hours per split of the registered draft.

## Playbooks

- A corpus without speaker ids: the default groups by file, which keeps each recording on one side.

## Sources

- docs/spec/04-blocks.md Block 1; docs/help/steps/dataset-import.md (the top-up rule).
