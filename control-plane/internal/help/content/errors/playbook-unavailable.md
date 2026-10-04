---
title: Playbook not available yet
summary: The playbook runs from a later roadmap phase than this build ships; it is listed for reference and cannot start a session yet.
contexts: [error:playbook-unavailable, guide:playbooks]
---

## What this is

A `409 Conflict` problem of type `playbook-unavailable`, answered by `playbooks.run` when the playbook's
`availableFrom` phase has not shipped. Cadence lists every bundled playbook so its chain and cost are visible; in
phase 4 only the "Weekly flywheel" waits (it needs phase 5's signals, triage verbs and schedules). Nothing was
written.

## Place in the loop

Any. `playbooks.list` says for each playbook whether it is `runnable` and, if not, why (`unavailable`).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/playbook-unavailable` |
| `status` | `409` |
| `detail` | The playbook and the phase it runs from |

## Commands

- `playbooks.list` — `runnable`, `availableFrom`, `unavailable`.
- `playbooks.run` on `try-cadence`, `adapt-new-language` or `finetune-from-dataset` — playbooks this phase runs.

## Playbooks

- Run "Fine-tune from a dataset version" instead, or work the steps that exist by hand.

## Sources

- Cadence recommendation — ROADMAP.md (phases), docs/spec/08-resolutions.md R16.
