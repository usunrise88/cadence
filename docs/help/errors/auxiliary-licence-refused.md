---
title: Auxiliary licence refused
summary: The auxiliary model's licence forbids commercial use of what it outputs, so no project may adopt it and no pipeline may use it (R26).
contexts: [error:auxiliary-licence-refused, registry:auxiliary, field:version]
---

## What this is

A `422 Unprocessable Entity` problem of type `auxiliary-licence-refused`. An auxiliary model (a language classifier, a
pseudo-label member, an aligner) writes text and labels that end up in dataset versions and from there in models
Cadence trains. R26 allows only auxiliaries whose licence permits commercial use of their outputs; the licence check
records that as `outputsCommercialUse` in the version's payload. A version with `outputsCommercialUse: false` is
refused:

| Where | What happens |
| --- | --- |
| `projects.adopt` of the version (dry run included) | Refused before anyone is asked to approve |
| A pipeline step whose parameter names it (x-cadence `registryRef`) | The plan fails (`pipeline-invalid` names the parameter) |

Models excluded by the check of 2026-10-03 were never registered (`facebook/mms-lid-*`, `facebook/mms-1b-all`,
torchaudio's `MMS_FA`: CC-BY-NC-4.0; community Hebrew wav2vec2 fine-tunes: licence or data unverified).

## Place in the loop

Data. Adopting an auxiliary is the moment its licence is decided: an approval for everyone, which the admin decides
after reading the licence (preset rule `auxiliary-adoption`). This refusal comes earlier, for a licence the check
already found unusable.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/auxiliary-licence-refused` |
| `status` | `422` |
| `detail` | The auxiliary version and its licence |

## Commands

- `registry.search` with `kind:auxiliary` — the auxiliaries and their licences, conditions and roles.
- `projects.adopt` — adopt an allowed auxiliary instead (an approval).

## Playbooks

- Agents: do not retry. Pick another auxiliary of the same role, or tell the person the model cannot be used.

## Sources

- docs/spec/08-resolutions.md R26 (licences of auxiliary models; nothing whose licence forbids commercial use of its
  outputs).
- docs/review/2026-10-03-phase-4-plan.md, "Auxiliary models and licences (R26)".
