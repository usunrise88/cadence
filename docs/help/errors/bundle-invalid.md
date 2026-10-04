---
title: Project bundle invalid
summary: A project bundle cannot be read or does not hold what its bundle.json says — not a bundle, a damaged or missing blob, a record that does not fit this instance.
contexts: [error:bundle-invalid, command:bundles.adopt, command:projects.new, command:projects.export]
---

## What this is

A `422 Unprocessable Entity` problem of type `bundle-invalid`, from `bundles.adopt`, `projects.new` with `bundle`,
or the job either starts. The detail names the file or version:

| Detail says | Why |
| --- | --- |
| no readable `bundle.json` | The path is not a bundle's directory, or the export that wrote it did not finish (`bundle.json` is written last) |
| is not a project bundle | `bundle.json` is another format — a dataset bundle (`cadence.bundle/1`) is imported with `dataset_import` format `cadence-bundle` |
| unknown registry kind, not a collection name, not a version string, no fingerprint, repeated version id | A record was edited or written by an incompatible Cadence |
| collection … holds … versions here | This instance uses the collection name for another kind |
| blob … is missing from the bundle, does not hash to … | A file was lost or changed in transit: the bundle is damaged; copy it again |
| its dataset bundle … is unreadable, the record names no source | A dataset bundle inside it is incomplete |
| golden set …: its dataset version or normalizer is not in the bundle | The bundle lacks what the golden set is made of |
| the repository bundle | `repository.bundle` is missing or git cannot verify it |

Nothing was registered: blobs already copied stay in the content store unreferenced, and the import's registrations
run in one transaction.

## Place in the loop

Outside the loop: moving a project between instances ([Project bundles](../guides/project-bundles.md)).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/bundle-invalid` |
| `status` | `422` |
| `detail` | The file or version and what is wrong with it |

## Commands

- `bundles.adopt` with `dryRun=true` — reads `bundle.json` and every dataset bundle's without copying anything.
- `projects.export` on the source instance — write the bundle again.

## Playbooks

- Agents: report the detail to the person; do not edit a bundle's files.

## Sources

- docs/spec/03-pipelines-defaults.md "Interoperability"; RFC 9457, Problem Details for HTTP APIs.
