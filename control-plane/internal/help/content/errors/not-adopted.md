---
title: Not in the project's data.lock
summary: A pipeline step parameter names a registry version the project's data.lock does not list; adopt it first (projects.adopt).
contexts: [error:not-adopted, command:pipelines.run]
---

## What this is

A `422 Unprocessable Entity` problem of type `not-adopted`. A step parameter marked `x-cadence.registry: <kind>` (a
dataset version, golden set, auxiliary model …) names a registry version by collection name, `@alias` or `ver_…`.
The pipeline engine resolves it through `data.lock` at the commit the pipeline is read at (bundled templates, and
projects without a repository, through the project's adoptions, which `data.lock` is written from):

| Reference | Resolves to |
| --- | --- |
| `dataset/fleurs-he` or `fleurs-he` | the newest version of that collection `data.lock` lists |
| `@train-current` | the alias's version, which `data.lock` must list |
| `ver_…` | that version, which `data.lock` must list |

A reference the lock does not cover is refused: the pipeline file names the collection, `data.lock` the version, and
both are in the same commit, so a checkout of the repository says exactly which data a run used. The plan
(`pipelines.run` dry run) shows each resolution in its step's `locked`.

`source` parameters (`sdp_ingest`) are not registry versions; "no licence, no ingest" covers them
(`source-unlicensed`).

## Place in the loop

Data and training: **adopt** (`projects.adopt`, which commits `data.lock`) → run the pipeline.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/not-adopted` |
| `status` | `422` |
| `detail` | The step, its kind, the parameter, the reference and where the lock was read |

## Commands

- `registry.search` — find the version.
- `projects.adopt` — adopt it (licence and locale checked); `data.lock` on main lists it from then on.
- `adoptions.list` — what the project adopted.

## Playbooks

- "Adapt a new language": adopt the frozen dataset version before the pipeline that names it runs; the plan
  (`pipelines.run` dry run) shows what every reference resolved to.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry": "Lockfile".
