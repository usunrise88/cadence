---
title: Imports and exports
summary: Bring corpora in (NeMo manifests, Lhotse cuts and Shar, Hugging Face datasets, folders with a CSV, Cadence bundles) and take frozen dataset versions out (Shar, NeMo manifests, Cadence bundles, the Hugging Face Hub) — datasets.export, exports.list, exports.get.
contexts: [guide:interoperability, command:datasets.export, command:exports.list, command:exports.get, entity:export]
---

## What this is

Data arrives in several formats and frozen versions may need to leave; both directions are step kinds, so a new
format is one module (docs/spec/03 "Interoperability").

**In.** Two paths:

| Path | When | What it makes |
| --- | --- | --- |
| Ingest (`pipelines/data-ingest`: `sdp_ingest` → … → `dataset_freeze`) | Audio on a mount that should stay there: long recordings, stereo calls, corpora you segment yourself | A draft indexed in place; `datasets.freeze` copies it into the content store |
| Import ([dataset_import](../steps/dataset-import.md), `pipelines/import`) | A corpus already cut into utterances: a NeMo manifest, a Lhotse CutSet or Shar, a Hugging Face dataset, a folder with `metadata.csv`, a Cadence bundle | A version frozen at import (the audio copied into the content store) |

Both register the corpus as a source with its licence (no licence, no ingest) — eval-only until a person clears it.

**Out.** `datasets.export` exports a **frozen** dataset version (a draft: [dataset-not-frozen](../errors/dataset-not-frozen.md)):

| `format` | Step kind | Writes | Target |
| --- | --- | --- | --- |
| `lhotse-shar` | [shar_export](../steps/shar-export.md) | Shar per split: cuts + recording tars | a writable mount or `cas` |
| `nemo-manifest` | [dataset_export](../steps/dataset-export.md) | `manifest.<split>.jsonl` + the WAV files | a writable mount or `cas` |
| `cadence-bundle` | [dataset_export](../steps/dataset-export.md) | `bundle.json` (registry record, sources) + every blob as `cas/b3/…` | a writable mount or `cas` |
| `hf-hub` | [hf_push](../steps/hf-push.md) | An audiofolder dataset with its card, pushed to `hubRepo` | the Hugging Face Hub — licence check ([export-not-allowed](../errors/export-not-allowed.md)) and an approval the admin decides |

The default target is `mount://<storage.export_mount>/<collection>/<version>/<format>` when the `exports` mount is
registered and writable (the phase-4 stand binds `/cadence/exports` read-write), else the content store. The export
runs as a one-step pipeline run in a project — by default the one the version was ingested or imported in — on a
worker with the `export` job kind; follow it on `entity.export.{id}` or its pipeline run.

**Copies and the cache.** An export that writes the version's audio unchanged to a mount (`nemo-manifest`,
`cadence-bundle`) records every such file as a copy of its content-store blob. A version whose blobs all have copies
can be evicted from the local cache (`datasets.evict`, or the cache sweep above the high-water mark) and brought back
with `datasets.materialize`. A bundle on a mount is the cheapest way to keep a version you rarely train on.

**Backups on a mount.** The backup mirror of the content store can live on a writable mount too
(`backups.mirror_mount`; [Backups](backups.md)): every mirrored blob is then a copy the cache can evict and
materialise from.

## Place in the loop

Data: import or ingest → freeze → (export). Exports never change the version: it stays frozen, with the same
fingerprint, and the export is work in a project (`exports.list`).

## Fields and defaults

| `datasets.export` field | Default | Meaning |
| --- | --- | --- |
| `version` | required | The frozen dataset version (`ver_…`) |
| `format` | required | `lhotse-shar`, `nemo-manifest`, `cadence-bundle`, `hf-hub` |
| `project` | the version's ingest or import project | Where the export's pipeline run runs |
| `target` | `storage.export_mount` (`exports`), else `cas` | `cas` or `mount://<writable path mount>/<directory>` |
| `hubRepo` | — | `hf-hub`: `<org>/<name>` |
| `hubPrivate` | `storage.export_hub_private` (true) | `hf-hub`: create the repository private |

An export record (`exports.get`): `state` (running, done, failed, cancelled — its pipeline run's), `target`, `files`,
`bytes`, `copies` (blobs recorded on the mount), `sample` (the first files), `hub` (repository and commit), `error`.

## Commands

- `datasets.export` — `dryRun=true` answers the plan (step kind, target, utterances, bytes, licence, sources, whether
  an approval is needed, whether the audio becomes copies).
- `exports.list` (a project's, newest first; `version` filters), `exports.get`.
- `mounts.list` — which mounts are writable; `storage.get` — what the cache holds and could evict.

## Playbooks

- Train outside Cadence on a frozen version: `datasets.export {version, format: lhotse-shar}`.
- Move a version to another Cadence instance: `datasets.export {version, format: cadence-bundle}`, then on the other
  instance `dataset_import` with `format: cadence-bundle` and the bundle's path; the version keeps its fingerprint.
- Share a public-corpus subset: `datasets.export {version, format: hf-hub, hubRepo: <org>/<name>}`; the admin approves.

## Sources

- docs/spec/03-pipelines-defaults.md "Interoperability"; docs/spec/02 "Storage and mounts".
- docs/review/2026-10-03-phase-4-plan.md decisions 1–4 and stream I.
