---
title: Target does not serve this model
summary: The deployment target named does not serve the model's family, deployable format or latency profile — or it is a delivery target, which Cadence never reaches. Nothing was started.
contexts: [error:target-does-not-serve, guide:staging-serving, guide:delivery-script, panel:deployment-targets, command:deployments.promote, command:transcriptions.new, command:models.parity, command:models.benchmark, entity:deployment_target]
---

## What this is

A `422 Unprocessable Entity` problem of type `target-does-not-serve`. A deployment target lists what it serves
(R46): model families (compared as data), the deployable formats of each and the latency profiles, the first being
the primary one. Work that names a target must match it:

| Detail says | Why | What to do |
| --- | --- | --- |
| `does not serve model family …` | The target lists other families | Pick a target that serves the family, or have the admin add it (`deploymentTargets.edit`, an approval) |
| `serves … in X, not Y` | The export's format is not one the target loads | Export in the target's format (`models.export` with `format`) |
| `serves … at A, not at B` | The export's latency profile is not served there | Export at a profile the target lists |
| `is a delivery target` | Cadence never reaches a production server: only a person's delivery script does | Decode through the staging target; promote to the delivery target |
| `is archived` | The target takes no new work | Use an active target |
| `no deployment target "…"` | The name or id is unknown | `deploymentTargets.list` |

A serve step whose lease names such a target fails with the same text (the grant gives it no endpoint).

## Place in the loop

Block 4, Deploy: promotion checks (02 "Deployments") and staging serving (06 "Staging serving").

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/target-does-not-serve` |
| `status` | `422` |
| `detail` | The target and what it serves instead |

The seeded staging target serves what `serving.staging_target` lists; `serving.default_target` is the target a serve
step uses when its pipeline names none.

## Commands

- `deploymentTargets.list`, `deploymentTargets.get` — what each target serves.
- `deploymentTargets.edit` — change it (an approval for everyone).

## Playbooks

- An agent picks a target that serves the model, or asks a person to extend one; it never edits a target itself.

## Sources

- docs/spec/08-resolutions.md R46; docs/spec/02-domain-projects-registry.md "Deployment targets".
