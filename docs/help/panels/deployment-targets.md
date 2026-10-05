---
title: Deployment targets
summary: Settings → Deployment targets — where models are served — the staging target Cadence reaches (its server's health and the models loaded on it) and the delivery targets only a person's delivery script reaches (what they serve, their slots and promotion chain).
contexts: [panel:deployment-targets, entity:deployment_target, command:deploymentTargets.list, command:deploymentTargets.get, command:deploymentTargets.new, command:deploymentTargets.edit, command:deploymentTargets.archive, guide:staging-serving, guide:delivery-script]
---

## What this is

The Settings section that lists deployment targets (R46), instance-wide like compute and mounts.

- **Staging** targets: the server Cadence reaches (the compose profile `serving`). Each row shows the endpoint, the
  server kind and version, what it serves (model families, deployable formats, latency profiles; the first profile
  is the primary one), its **health** (`up`, `down` or `unknown`, since when, the last check's latency or error)
  and its **served models**: each model's versioned name, its deployable, its memory reservation, its state
  (`in-use` with the number of leases using it, `loaded` while idle, `unloaded`) and when it was last used.
- **Delivery** targets: production servers. They have no endpoint — Cadence never reaches them — and list the
  repository path the delivery script installs into, the slots (the model names the production pipeline calls), the
  target concurrency, the card class, the boost limits and the head of the signed promotion chain.

New and Edit open the target form; both are approvals the admin decides, for people too, because a target names
production. Archive retires a target (its chain stays).

## Place in the loop

Block 4, Deploy: where parity, benchmarks and shadow replay run (staging) and where promotions go (delivery).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `health.state` | `up`, `down`, `unknown` (staging only; checked every `serving.health_check_seconds`) |
| `servedModels[]` | `model`, `deployableHash`, `memoryMb`, `state`, `leases`, `lastUsedAt` |
| `serves[]` | `family`, `formats`, `profiles` — compared as data at promotion and serve time |
| `slots`, `repositoryPath` | Delivery only |

The staging target is seeded from `serving.staging_target`; idle models unload after `serving.unload_idle_minutes`.

## Commands

`deploymentTargets.list`, `deploymentTargets.get`, `deploymentTargets.new`, `deploymentTargets.edit`,
`deploymentTargets.archive`; `promotions.list` for a delivery target's chain.

## Playbooks

- An agent reads targets (what serves its model, whether staging is up); changing one is an approval request.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployment targets"; docs/spec/06-platform.md "Staging serving";
  [Staging serving](../guides/staging-serving.md); [Delivery script](../guides/delivery-script.md).
