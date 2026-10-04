---
title: dataset_export (step kind)
summary: Writes a frozen dataset version as a NeMo manifest with its WAV files, or as a Cadence bundle another instance imports — to a writable mount (where its audio becomes evictable copies) or the content store.
contexts: [step:dataset_export, artifact:export, artifact:registry_record, command:datasets.export]
---

## What this is

`dataset_export@1` is a runtime-neutral core step kind (every runtime image, CPU, job kind `export`). `datasets.export`
runs it for the formats that are not Shar or the Hub. It reads a frozen `dataset` artifact and writes one of two
layouts to its target:

**`nemo-manifest`** — what NeMo's ASR data loaders read:

| File | Content |
| --- | --- |
| `manifest.<split>.jsonl` | One line per utterance: `audio_filepath` (relative to the manifest), `duration`, `text`, `lang`, `speaker?` |
| `audio/<ab>/<hash>.wav` | The dataset's WAV files at their paths in the dataset, copied byte for byte |

**`cadence-bundle`** — a frozen version for another Cadence instance (`dataset_import` format `cadence-bundle`):

| File | Content |
| --- | --- |
| `bundle.json` | `format: cadence.bundle/1`, `record` (the version's registry record and its sources with licences, the `record` input), `artifact` (the dataset artifact's hash), `files: [{path, hash, bytes}]` (the artifact's manifest) |
| `cas/b3/<ab>/<hex>` | Every blob of the dataset artifact and its manifest, laid out as a content store |

The `record` input (type `registry_record`, `cadence.registry-record/1`) is rendered by the control plane from the
registry: the collection, version, licence, tags, fingerprint, payload and sources. A bundle is refused without it.

**Copies.** Both layouts place the version's audio on the target unchanged. With a mount target the control plane's
`export` output hook records every such file as a copy of its content-store blob on the mount (`blob_copies`), so the
cache may evict the version and `datasets.materialize` copies it back from there; a mount scan finds the
`cas/b3/` layout of a bundle on its own as well. The output is an `export` artifact (`export.json` and, with target
`cas`, the files under `files/`), as for [shar_export](shar-export.md).

## Place in the loop

Data → out of Cadence (NeMo manifests for an outside training stack), or from one Cadence instance to another (a
bundle per dataset version; the project repository travels by git).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `format` | `nemo-manifest` | spec 03 Interoperability | `nemo-manifest`, `cadence-bundle` |
| `target` | `cas` | phase-4 plan decision 1 (the exports mount) | `cas`, or a directory on a writable path mount `mount://exports/<dir>` |
| `version` | `""` | Cadence recommendation | the dataset version (`ver_…`), recorded in `export.json` |
| `name` | `""` | Cadence recommendation | the collection without `dataset/` |

## Commands

- `datasets.export` (formats `nemo-manifest`, `cadence-bundle`).
- `exports.list`, `exports.get`; `storage.get` and `datasets.evict` — the copies the export made.

## Playbooks

- Free cache space for a version you keep: export it as a `cadence-bundle` to the exports mount, then
  `datasets.evict` it; `datasets.materialize` brings it back.
- Move a version to another instance: export a bundle, make the exports directory reachable as a mount there, and run
  `dataset_import` with `format: cadence-bundle`, `path: mount://<mount>/<dir>`.

## Sources

- NVIDIA NeMo documentation, ASR datasets (manifest format: `audio_filepath`, `duration`, `text`).
- docs/spec/03-pipelines-defaults.md "Interoperability" (export to NeMo manifest; a Cadence bundle).
- docs/review/2026-10-03-phase-4-plan.md decisions 1–2 and stream I.
