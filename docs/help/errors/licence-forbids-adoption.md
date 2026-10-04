---
title: Licence forbids adoption
summary: projects.adopt refused a registry version whose licence names no licence, forbids commercial use of what it is trained on or ships, or forbids derivative works.
contexts: [error:licence-forbids-adoption, command:projects.adopt]
---

## What this is

A `422 Unprocessable Entity` problem of type `licence-forbids-adoption`. Adopting a registry version into a project
checks its licence first (docs/spec/02-domain-projects-registry.md "Registry": "Adoption checks licence and locale";
R26: nothing is adopted that forbids commercial use of its outputs). The licence is the collection's (the source's,
for a dataset version), else the payload's `licence`; several licences joined with `AND` are each checked.

| Refused when | Kinds | What to do |
| --- | --- | --- |
| The licence names no licence (`unknown`, `none`, `NOASSERTION`, `n/a`, `tbd`, empty) | base models, dataset versions, golden sets, models, noise banks, auxiliary models | A person finds the real licence; for data, set it on the source (`sources.edit`) and ingest or import again |
| The payload says `outputsCommercialUse: false` | any | Use another model; R26 lists the allowed auxiliary models |
| The licence forbids commercial use (`-NC-`, non-commercial, research only) | base models, models, noise banks, auxiliary models, and dataset versions that are not eval-only | Evaluate on it instead: an eval-only dataset version or a golden set of the same data may be adopted |
| The licence forbids derivative works (`-ND-`) | the same | A fine-tuned model is a derivative work; pick other data or another base |

Templates and scoring normalizers are Cadence's own configuration and are not checked. Nothing was adopted and the
project's revision did not change.

## Place in the loop

Data and evaluation: register (sources, imports, ingests) → freeze → **adopt** into the project → mix, train,
evaluate. The licence decides whether a project may use a version at all; clearing a source for training
(`trainingCleared`) is the separate, later decision that lets a mix train on it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/licence-forbids-adoption` |
| `status` | `422` |
| `detail` | The version, its licence and the rule it breaks |

## Commands

- `projects.adopt` — adopt a version (its dry run checks without adopting).
- `registry.search` — find another version or collection (`kind:`, `locale:`, `tag:`).
- `sources.edit` — correct a source's licence (a person's decision; an agent's call waits for an approval).

## Playbooks

- Agents: never work around this refusal. Report the version and its licence, and suggest an eval-only use or another
  version.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry"; docs/spec/08-resolutions.md R26.
- docs/review/2026-10-03-phase-4-plan.md "Owner decisions" 2 and the licence table.
