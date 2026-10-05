---
title: Canary required
summary: A production promotion needs the same model version to be the slot's confirmed canary first; this deployment is not.
contexts: [error:canary-required, command:deployments.promote, entity:deployment]
---

## What this is

A `422 Unprocessable Entity` problem of type `canary-required`, answered before an approval is asked when a deployment
is promoted to production but is not the confirmed canary of that delivery target's slot. Production takes the slot's
whole traffic, so the model must first have served a share of it as a canary — promoted, approved, delivered by a
person and confirmed with the delivery script's receipt.

| Detail says | What to do |
| --- | --- |
| `it is a shadow on no slot` | Promote it to canary first (`deployments.promote` `{stage: canary, target, slot}`) |
| `it is a canary on another slot` | Promote the canary of this slot, or name the slot it runs on |
| The canary waits for its receipt | Confirm the delivery (`promotions.verify`, a person) before asking for production |

## Place in the loop

Block 4, Deploy: shadow → **canary** → production.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/canary-required` |
| `status` | `422` |
| `detail` | The model version, the slot and what the deployment is |

- A canary's traffic share is `deploy.canary_share` (0.05) unless the promotion names one; there is no minimum canary
  period (07 "Open questions").

## Commands

- `deployments.get` — `stage`, `slot`, `pending`.
- `deployments.promote` with `dryRun=true` answers every check without asking anyone.

## Playbooks

- Agents ask for canary and production; a person approves each in the confirm modal and confirms each delivery.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployments" (checks before an approval); R33.
