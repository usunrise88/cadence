---
title: Model family unavailable
summary: The base model's model family, or the step kind one of its roles needs, has not been published by any worker runtime.
contexts: [error:family-unavailable, field:baseModel]
---

## What this is

A `422 Unprocessable Entity` problem of type `family-unavailable`: `runs.new`, `runs.stage`, `runs.calibrate` or
`checkpoints.average` needs a step kind that fills a role of the base model's model family (R41), and none is
published. Runs never name a family or its step kinds in code: they read the base model's `familyId`, the family
descriptor its runtime published (`model-family/<familyId>`), and the step kind of the role the command needs —
`calibrate`, `train` or `average`. The detail says which of these is missing:

| Missing | Why | What changes it |
| --- | --- | --- |
| `familyId` on the base model | The base model version predates model families | Register a base model version that names its family |
| The family descriptor | No worker of the family's runtime has registered since the control plane started with this database | Start the runtime's worker service; `modelFamilies.list` then lists the family |
| A role's step kind | The family names no kind for the role, or no runtime publishes that kind | `modelFamilies.get` shows the roles; `stepKinds.list` the published kinds |

Nothing was written.

## Place in the loop

Train. A run is one stage of the family's train role; calibration runs its calibrate role; averaging its average
role. A framework pack (runtime image, family descriptor, role step kinds) makes a family available (R45).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/family-unavailable` |
| `status` | `422` |
| `detail` | The base model, the family and the role or kind that is missing |

## Commands

- `modelFamilies.list`, `modelFamilies.get` — published families and their roles.
- `stepKinds.list` — the step kinds workers publish, by runtime.
- `runtimes.list` — the runtimes whose workers registered.

## Playbooks

- Agents: do not retry. Tell the person which runtime's worker must be running (the family's runtime), or pick a
  base model whose family is published.

## Sources

- docs/spec/08-resolutions.md R40 (runtimes), R41 (model families), R45 (framework packs).
- RFC 9457, Problem Details for HTTP APIs.
