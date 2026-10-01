---
title: Playbook stopped
summary: The playbook of this session ended — its chain is complete or a stop condition hit (a failed step, a denied approval, an exhausted budget) — so it spends no more GPU time.
contexts: [error:playbook-stopped, guide:playbooks]
---

## What this is

A `409 Conflict` problem of type `playbook-stopped`: a playbook session sent a command that spends GPU time after its
playbook ended. A playbook ends when every available step of its plan is done, or at a stop condition its template
names (docs/spec/03-pipelines-defaults.md "Playbooks"):

| Stop | When |
| --- | --- |
| `step: failed` | The job a step waits for failed or was cancelled (`jobs.wait` answered it) |
| `approval: denied` | A person denied a command the session asked for |
| `budget: exceeded` | The session paused on its turn or token budget, or on the project's daily agent budget |
| `gate: failed` | The gate failed (phase 3) |

The session's transcript carries the summary and the suggested next step; the session ends after the turn. Nothing
was written.

## Place in the loop

Train. A stopped playbook is a decision point for a person: fix the cause, then start the playbook again or continue
by hand in an interactive session.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/playbook-stopped` |
| `status` | `409` |
| `detail` | The playbook, its state and its summary |

## Commands

- `agentSessions.get` — `playbook.state`, `playbook.stop`, `playbook.summary`, `playbook.next`.
- `playbooks.run` — start the playbook again (a person).

## Playbooks

- Agents: stop spending, write a short summary with the next step, and end your turn.

## Sources

- Cadence recommendation — docs/spec/05-agents.md "Playbooks and schedules as sessions", R16.
