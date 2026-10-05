---
title: Shadow volume short
summary: A canary promotion needs the shadow deployment to have replayed deploy.shadow_min_hours (20 h) of call audio against the comparison model; it has replayed less.
contexts: [error:shadow-volume-short, command:deployments.promote, command:shadowReplays.new, panel:shadow, entity:deployment]
---

## What this is

A `422 Unprocessable Entity` problem of type `shadow-volume-short`, answered before an approval is asked when a
shadow deployment is promoted to canary with fewer hours of replayed calls than `deploy.shadow_min_hours`. Every night
at `deploy.shadow_replay_at` (in `policies.timezone`) a shadow deployment replays the newest calls of its replay
mount it has not replayed yet, up to `deploy.shadow_replay_max_hours`; each call counts once, by its duration.

| Detail says | What to do |
| --- | --- |
| `replayed 6.0 h … over 2 night(s)` | Wait for more nights, or replay now (`shadowReplays.new`) when the mount holds calls not replayed yet |
| `0.0 h … over 0 night(s)` | Check the Shadow panel: a night that could not run is listed as skipped with its reason (no new calls, the mount or the staging server down) |

## Place in the loop

Block 4, Deploy: export → parity → benchmark → **shadow** → canary → production.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/shadow-volume-short` |
| `status` | `422` |
| `detail` | Hours replayed, nights, and the hours a canary needs |

- `deploy.shadow_min_hours` 20 h; `deploy.shadow_replay_max_hours` 4 h a night; `deploy.shadow_replay_at` 02:00.

## Commands

- `deployments.get` — `shadow.hours`, `shadow.nights`, `shadow.nextReplayAt`.
- `shadowReplays.list` — the nights and why one was skipped; `shadowReplays.new` replays now.

## Playbooks

- Do not shorten the shadow to reach a canary sooner: the hours are what makes the divergence trustworthy.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployments" (checks before an approval); docs/spec/03-pipelines-defaults.md
  "Shadow replay"; [Shadow](../panels/shadow.md).
