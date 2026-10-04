---
title: Not in the project's data.lock
summary: A pipeline step parameter or an alias names a registry version the project has not adopted (its data.lock does not list it); adopt it first (projects.adopt).
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

A parameter marked `x-cadence.registryRef: {kind: auxiliary, role}` (a pseudo-label member, a LID classifier, an
aligner) resolves the same way when `data.lock` at that commit lists its collection: to the newest version the lock
lists, even if the project adopted a newer one since. When the lock does not list the collection (or the project has
no repository), it resolves to the newest version the project adopted. "Newest" is the version the registry created
last, not the greatest name. The version must be adopted either way. `aliases.set` answers `not-adopted` too when it
names a version the project has not adopted.

Only what `data.lock` pins and the adoptions allow resolves: an entry the project never adopted (a hand edit, merged from a branch) is
not resolved either, and merging such an edit adopts nothing — only `projects.adopt` adopts, after the licence,
locale, leakage and auxiliary checks. An agent session's edit of `data.lock` waits for a person to accept it.

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
