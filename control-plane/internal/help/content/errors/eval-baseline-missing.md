---
title: No baseline to compare with
summary: An eval compares every cell with the baseline's cell, and the project has no baseline — or the eval has no scored baseline cells for the gate to read.
contexts: [error:eval-baseline-missing, field:baseline]
---

## What this is

A `422 Unprocessable Entity` problem of type `eval-baseline-missing`. Every eval cell is a comparison: the subject
(a checkpoint, a model version or a base model) against the baseline's cell of the same golden set, latency profile
and decoding. `evals.new` takes the baseline from, in order:

| Source | Where it is set |
| --- | --- |
| The request's `baseline` | `ver_…`, `@alias`, `base-model/<name>` or `model/<name>` |
| The project's `@baseline` alias | `aliases.set` with name `baseline` (approval) |
| The project's default base model | The project wizard, `projects.edit` |

When none of them names a model, nothing was written. `evals.gate` answers the same problem for an eval without a
scored baseline cell (an eval created before its baseline cells could be computed).

## Place in the loop

Evaluate. Until something is promoted, the baseline is the project's base model at its pinned revision (decision 1
of the phase-3 plan); its cells are computed once and cached in the eval records for every project.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/eval-baseline-missing` |
| `status` | `422` |
| `detail` | What was missing and where to set it |

## Commands

- `evals.new` with `baseline` — compare against a model you name.
- `aliases.set` (name `baseline`) — point the project's baseline at a base model or model version (a person approves).
- `projects.get` — the project's default base model.

## Playbooks

- Agents: name the baseline in `evals.new` (the project's base model, `baseModels.list`), or ask a person to set
  `@baseline`; never work around the comparison.

## Sources

- docs/spec/04-blocks.md Block 3; docs/spec/08-resolutions.md R23; docs/review/2026-10-02-phase-3-plan.md "Decisions" 1–2.
- RFC 9457, Problem Details for HTTP APIs.
