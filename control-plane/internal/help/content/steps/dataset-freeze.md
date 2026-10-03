---
title: dataset_freeze (step kind)
summary: Turns split segments into a dataset version — a draft without audio (quality checks, statistics, card), then on datasets.freeze the frozen copy cut into the content store with shards.
contexts: [step:dataset_freeze, artifact:dataset, artifact:segments]
---

## What this is

`dataset_freeze@1` is a runtime-neutral core step kind (CPU, job kind `data`) with two modes.

**`mode: draft`** ends `pipelines/data-ingest.yaml`. It writes a `dataset` artifact of format
`cadence.dataset-draft/1` — no audio:

| File | Content |
| --- | --- |
| `dataset.json` | `format`, `name?`, `description?`, `source: {name}`, `splitRule`, `counts: {train, validation, test}`, `hours`, `tags?`, `evalOnly?`, `quality`, `stats`, `card: card.md`, `sourceInfo?` |
| `manifest.jsonl` | Per member: `uri`, `hash`, `bytes`, `duration`, `sampleRate: 16000`, `channels: 1`, `language`, `speaker?`, `text`, `origin`, `confidence?`, `split`, `role` |
| `card.md` | The dataset card |

The control plane registers it as a **draft** dataset version (`frozen: false`). Hashes are known, so the leakage check
and fingerprints work before anything is copied; a draft is never mixed, trained on, exported or adopted.

**`mode: cut`** is what `datasets.freeze` runs (with `draft_version`). It decodes every file from its mount, cuts each
member, checks it against its hash (a file changed since ingest fails the step), and writes the first copy: a
`cadence.dataset/1` artifact the phase-2 training path reads — `dataset.json` (plus `draftVersionId`, `quality`,
`stats`, `card`, `shards`), `manifest.jsonl` (plus `uri`, `role`; `audio` is `audio/<h2>/<h>.wav`, h the BLAKE3 hex),
the canonical WAVs, `card.md`, and the shards: `shards/cuts.NNNNNN.jsonl.gz`, a Lhotse cuts file (MonoCut, recording
source a file inside the artifact) per `shard_utterances` members — the unit of pinning, eviction and materialisation.
The output is byte-identical on every host.

Every member needs `text`, `language` and `split`; the origin defaults to `human`; a disputed pseudo-label is refused;
a segment repeating an earlier one's audio is left out.

**Quality checks** (`quality: {passed, checks: [{name, status, value, threshold, message}]}`; `pass` or `warn`, never
blocking): `silence_share` (mean 1 − voice-activity share), `clipping_share` (share of members with more than 0.1 % of
samples at full scale), `length_outliers` (share of members whose characters per second lie beyond `quality_outlier_z`
standard deviations). **Statistics** (`stats`, binned for the Dataset version panel, R53): `durationHistogram`,
`charsPerSecondHistogram`, `levelHistogram` (`edges` are lower bounds, the last bucket is open), `durationPercentiles`
(p5, p50, p95), `origins`, `roles`, `sourceRates`, `speakers`.

## Place in the loop

Data — the end of `pipelines/data-ingest.yaml` (draft); `datasets.freeze` runs the cut, then the version is mixed and
trained on (Block 2).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `mode` | `draft` | phase-4 plan decisions 3–4 | `draft`, `cut` |
| `name` | `""` (source name) | Cadence recommendation | collection name without `dataset/` |
| `description` | `""` | Cadence recommendation | ≤ 2000 characters |
| `tags` | `[]` | Cadence recommendation | ≤ 32 tags |
| `eval_only` | `false` | R18 | true, false |
| `draft_version` | `""` | set by `datasets.freeze` | `ver_…`, required in cut mode |
| `shard_utterances` | `data.shard_utterances` (2000) | Lhotse Shar (1000 cuts per shard) | 10–100000 |
| `quality_max_silence_share` | `data.quality_max_silence_share` (0.5) | Cadence recommendation | 0–1 |
| `quality_max_clipped_share` | `data.quality_max_clipped_share` (0.01) | Cadence recommendation | 0–1 |
| `quality_outlier_z` | `data.quality_outlier_z` (3) | Cadence recommendation | 1–10 |
| `quality_max_outlier_share` | `data.quality_max_outlier_share` (0.02) | Cadence recommendation | 0–1 |

## Commands

- `datasets.preview` — hours per language and split of the draft under filters.
- `datasets.freeze` — leakage check, then the cut; `datasets.get` shows `frozen`, `quality`, `stats`, `card`, `shards`.

## Playbooks

- The step fails with "has no split": add `speaker_disjoint_split` before it. "has no text": pseudo-label the audio or
  let `manifest_filter` drop untranscribed segments.
- A freeze failing with "no longer matches": the file on the mount changed since ingest; run `data-ingest` again.

## Sources

- docs/review/2026-10-03-phase-4-plan.md decisions 3–4, "D/A → R"; docs/spec/04-blocks.md Block 1 step 7.
- Lhotse documentation, "Lhotse Shar" and the CutSet manifest format.
