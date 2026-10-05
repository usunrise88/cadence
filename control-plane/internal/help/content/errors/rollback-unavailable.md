---
title: Rollback unavailable
summary: A rollback returns the slot to its confirmed earlier production version, which stayed loaded on the production host; this slot has none.
contexts: [error:rollback-unavailable, command:deployments.rollback, entity:deployment]
---

## What this is

A `422 Unprocessable Entity` problem of type `rollback-unavailable`, answered before an approval is asked when a
deployment is rolled back but its slot has no earlier production version to return to. A rollback is a signed record
and a small delivery script that routes the slot back to the version before; it installs nothing, because every
delivery leaves the previous version loaded.

| The deployment is | It rolls back to |
| --- | --- |
| A canary | The slot's production deployment (the canary's share returns to it) |
| Production | The production deployment its confirmation replaced |
| The slot's first production | Nothing: refused (`rollback-unavailable`) |

## Place in the loop

Block 4, Deploy: canary → production, each step reversible.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/rollback-unavailable` |
| `status` | `422` |
| `detail` | The target and slot |

## Commands

- `deployments.rollback` with `dryRun=true` answers the check and the version restored.
- `promotions.list` — the slot's chain: which production version was confirmed before.

## Playbooks

- A first production that misbehaves is taken out of the slot through the production pipeline's own routing, outside
  Cadence; Cadence records only what its delivery scripts did.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployments" (checks before an approval) and "Promotion records".
