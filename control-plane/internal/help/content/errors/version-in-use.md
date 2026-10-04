---
title: Registry version in use
summary: versions.archive refused a registry version that something still uses — a project adopted it, another version names it, a run, mix, eval record or pipeline step was built on it.
contexts: [error:version-in-use, command:versions.archive]
---

## What this is

A `409 Conflict` problem of type `version-in-use`. The registry never deletes a version; `versions.archive` is its soft
delete, and only for a version nothing uses (docs/spec/02-domain-projects-registry.md "Registry": "a registry version
referenced by anything cannot be deleted"). The problem's `errors` list the users (at most 20):

| User | What it means |
| --- | --- |
| `adopted by project <slug>` | The project adopted it; its `data.lock` lists it |
| `default base model of project <slug>` | The project's wizard choice |
| `named by ver_…` | Another registry version's payload names it (a golden set's dataset version, a model's base model) |
| `run run_…`, `experiment exp_…` | Training was built on it |
| `mix mix_…` | A mix revision names the dataset version |
| `eval record erc_…` | An eval scored on the golden set or normalizer |

Nothing was archived. Lineage stays whole: a version a result came from never disappears from under it.

## Place in the loop

Registry housekeeping, the admin's: archive the versions nothing uses so the Library and `registry.search` stop
offering them. What is in use stays; a newer version replaces it by adoption and aliases.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/version-in-use` |
| `status` | `409` |
| `errors[]` | One entry per user (`/version`, relation and id) |

## Commands

- `versions.archive` — archive (dry run to check first).
- `registry.lineage` — everything around the version, both ways.

## Playbooks

- Archive what nothing uses; to retire a version a project uses, point its aliases and gates at the newer
  version first, and leave the old one — lineage keeps pointing at it.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry".
