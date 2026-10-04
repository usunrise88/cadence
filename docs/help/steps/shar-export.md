---
title: shar_export (step kind)
summary: Writes a frozen dataset version as Lhotse Shar — one Shar directory per split with cuts and recording tars of the 16 kHz WAV audio — to a writable mount or the content store.
contexts: [step:shar_export, artifact:export, command:datasets.export]
---

## What this is

`shar_export@1` is a runtime-neutral core step kind (every runtime image, CPU, job kind `export`). `datasets.export`
with format `lhotse-shar` runs it in a one-step pipeline run. It reads a frozen `dataset` artifact and writes
[Lhotse Shar](https://lhotse.readthedocs.io/) — the sequential, tar-based format NeMo's and Lhotse's data loaders
stream from — one Shar directory per split:

| File | Content |
| --- | --- |
| `<split>/cuts.NNNNNN.jsonl.gz` | One `MonoCut` per utterance: id = the audio's BLAKE3 hex, start 0, its duration, one supervision (text, language, speaker), `custom: {origin, split, uri?}`; its recording is a Shar placeholder (`sources: [{type: shar}]`) |
| `<split>/recording.NNNNNN.tar` | Per cut, in cut order: `<id>.wav` (the dataset's WAV, byte for byte) then `<id>.json` (the placeholder recording) — what Lhotse's `SharWriter` writes and its `LazySharIterator` reads |

Shards hold `shard_utterances` cuts. Archives and gzip streams carry no timestamps, owners or modes from the host, so
the same version exports to the same bytes on every host. Audio and text only — never features: mel bins, frame rate
and normalisation belong to a model family (docs/spec/03 "Artifact types", `shar`).

The output is an `export` artifact: `export.json` (`format: cadence.export/1`, `exportFormat: lhotse-shar`, `target`,
`version`, `utterances`, `files: [{path, hash, bytes}]`) and, with target `cas`, the files under `files/`. With a mount
target the files are written there (each atomically) and the artifact holds only the manifest. The tars are new
bytes, so a Shar export adds no copies of the version's blobs on the mount; a `nemo-manifest` or `cadence-bundle`
export does ([dataset_export](dataset-export.md)).

## Place in the loop

Data → out of Cadence: a frozen dataset version for a training stack outside Cadence, or for another team. Shar
written by this step re-imports with `dataset_import` format `lhotse-shar` (`split_rule: source`) to the same audio
bytes, splits and texts — the same content fingerprint.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `target` | `cas` | phase-4 plan decision 1 (the exports mount) | `cas`, or a directory on a writable path mount `mount://exports/<dir>` (datasets.export sets `mount://<storage.export_mount>/<collection>/<version>/lhotse-shar` by default) |
| `version` | `""` | Cadence recommendation | the dataset version (`ver_…`), recorded in `export.json` |
| `name` | `""` | Cadence recommendation | the collection without `dataset/` |
| `shard_utterances` | `data.shar_shard_utterances` (1000) | Lhotse SharWriter's default shard size | 10–100000 |

## Commands

- `datasets.export` (format `lhotse-shar`) — plans and starts the export; `dryRun=true` shows the target and size.
- `exports.list`, `exports.get` — the exports of a project, with their files and state.

## Playbooks

- Train elsewhere on a frozen version: `datasets.export {version, format: lhotse-shar}` writes
  `mount://exports/<collection>/<version>/lhotse-shar/{train,validation,test}/`; point Lhotse's
  `CutSet.from_shar(in_dir=…)` at a split folder.

## Sources

- Lhotse documentation, "Lhotse Shar" (`lhotse.shar.writers.SharWriter`, `lhotse.shar.readers.LazySharIterator`).
- docs/spec/03-pipelines-defaults.md "Interoperability" and "Artifact types".
- docs/review/2026-10-03-phase-4-plan.md, stream I.
