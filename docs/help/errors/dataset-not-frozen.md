---
title: Dataset version not frozen
summary: The dataset version is a draft — segments indexed in place on a mount, no audio copied — and drafts are never mixed, trained on, exported or adopted; freeze it first.
contexts: [error:dataset-not-frozen, field:datasets]
---

## What this is

A `422 Unprocessable Entity` problem of type `dataset-not-frozen`: a mix (`mixes.new`, `mixes.edit`, `mixes.preview`),
a run (`runs.new`) or a pipeline run whose training step reads a dataset named a **draft** dataset version
(`datasets.get` shows `state: draft` and `dataset.frozen: false`).

`pipelines/data-ingest` ends in a draft: `sdp_ingest` indexes the audio on a mount in place, the following steps
normalise, filter and split the segments, and `dataset_freeze` (mode `draft`) registers them as a draft version. Its
utterances already have their identity (the BLAKE3 hash of each segment's canonical 16 kHz WAV), so
`datasets.preview` and the leakage check work on it, but no audio is in the content store yet. Only frozen versions
can be trained on, exported or adopted (docs/spec/04-blocks.md Block 1, "Gates"). Nothing was written.

## Place in the loop

Data → Train: ingest → preview → **freeze** → mix → run.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/dataset-not-frozen` |
| `status` | `422` |
| `detail` | The draft version (collection and version) |

## Commands

- `datasets.preview` — hours per language and split of the draft after filters.
- `datasets.freeze` — the leakage check against every golden set, then a CPU pipeline run that cuts the segments into
  the content store, verifies each hash, writes the shards, quality checks and the card, and freezes the version.
- `datasets.get` — `state`, `dataset.frozen`, `dataset.freeze` (the freeze's pipeline run).

## Playbooks

- Agents: preview the draft, freeze it (`datasets.freeze`), wait for the pipeline run, then use the frozen version
  in the mix. Do not retry the mix or the run with the draft.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, decisions 3 (index in place) and 4 (draft dataset versions).
- RFC 9110, HTTP Semantics, §15.5.21 422 Unprocessable Content; RFC 9457, Problem Details for HTTP APIs.
