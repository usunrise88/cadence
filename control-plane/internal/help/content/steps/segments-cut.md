---
title: segments_cut (step kind)
summary: Cuts the untranscribed segments of an ingest from their mount into the dataset the pseudo-label members and language identification read — scratch input, never a dataset version.
contexts: [step:segments_cut, artifact:segments, artifact:dataset]
---

## What this is

`segments_cut@1` is a runtime-neutral core step kind (CPU, job kind `data`). It reads a `segments` artifact
(`sdp_ingest`) and writes a `dataset` artifact of format `cadence.dataset/1` with `purpose: pseudo-label`: one 16 kHz
mono WAV per segment that needs a label — a segment without text of its own, or with a pseudo-label from an earlier
pass — cut from its file on the mount exactly as `dataset_freeze` cuts it, and checked against the segment's hash.
The bot's scripted turns and segments with a human transcript are left out (`which: all` cuts every segment).

The pseudo-label members (`nemotron_transcribe`, `whisper_transcribe`, `oasis_transcribe`) and `lid_classify` read
it as any dataset and key their rows by the BLAKE3 hash of each clip, which is the segment's identity, so
`pseudolabel_ensemble` joins their hypotheses back to the segments.

It is scratch input: the dataset hook never registers it as a dataset version and no training step may read it
(`golden-set-leakage` names it a derived dataset). The manifest rows are `audio`, `hash`, `uri`, `duration`,
`language`, `text: ""`, `role` and `speaker?`.

## Place in the loop

Data — `pipelines/pseudo-label.yaml`: `sdp_ingest` → **`segments_cut`** → the members and `lid_classify` →
`pseudolabel_ensemble` → `text_normalise` → `manifest_filter` → `speaker_disjoint_split` → `dataset_freeze` (draft).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `which` | `unlabelled` | phase-4 plan decision 6 (members label segments without text) | `unlabelled`, `all` |

## Commands

- `pipelines.run` with `pseudo-label` (dry run first).
- `pipelineRuns.get` — the step's output and its meta (`utterances`, `hours`).

## Playbooks

- The step fails with "no segment needs a label": every segment already has text — run `data-ingest` instead.
- "no longer matches the indexed segment": the file on the mount changed since ingest; run the pipeline again.

## Sources

- docs/review/2026-10-03-phase-4-plan.md decisions 3 and 6, "Interfaces between streams" (D → X).
- docs/help/steps/dataset-freeze.md (the cut), docs/help/steps/pseudolabel-ensemble.md (the join by hash).
