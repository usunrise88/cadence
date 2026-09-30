---
title: Playbook not available yet
summary: The playbook runs from a later roadmap phase than this build ships; it is listed for reference and cannot start a session yet.
contexts: [error:playbook-unavailable, guide:playbooks]
---

## What this is

A `409 Conflict` problem of type `playbook-unavailable`, answered by `playbooks.run` when the playbook's
`availableFrom` phase has not shipped. Cadence ships the five v1 playbooks from phase 2 so their chains and costs are
visible, but "Adapt a new language", "Improve on telephony", "Fix names and terms" and the "Weekly flywheel" need
steps of phase 4 and later (ingest and freeze, sources, boost lists, triage, schedules). Nothing was written.

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
- `playbooks.run` on `finetune-from-dataset` — the playbook phase 2 runs.

## Playbooks

- Run "Fine-tune from a dataset version" instead, or work the steps that exist by hand.

## Sources

- Cadence recommendation — ROADMAP.md (phases), docs/spec/08-resolutions.md R16.
