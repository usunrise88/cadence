---
title: Dry run required first
summary: In a playbook session a command that spends GPU time runs only after a successful dry run of the same request in the same session; call it with dryRun=true, report the estimate, then call it again with the same arguments.
contexts: [error:playbook-dry-run-required, guide:playbooks]
---

## What this is

A `409 Conflict` problem of type `playbook-dry-run-required`. A playbook session (docs/spec/05-agents.md
"Playbooks and schedules as sessions") runs one step at a time with a dry run before each spending step, and the
server enforces it: `runs.new`, `runs.calibrate`, `runs.resume`, `runs.stage` and `checkpoints.average` sent without
`dryRun` by a playbook session are refused unless the session's last dry run of the same operation since its last real one
was the same request: the same path, query and body (the `Idempotency-Key` does not count). A dry run of a cheap
request therefore never admits a different, more expensive one; dry-run the request again after changing an
argument. Each real spending command uses up its dry run, so two training runs need two dry runs. Nothing was
written.

The rule is the server's, not the playbook template's: a template cannot turn it off, and the agent cannot claim a
dry run it did not make — only a dry run the control plane answered counts.

## Place in the loop

Train. The dry run answers the estimate (GPU-hours, card, duration, basis and ±) and whether the real command would
wait for an approval (`Cadence-Policy: approval`); the plan's step turns running with the estimate as its note.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/playbook-dry-run-required` |
| `status` | `409` |
| `detail` | The operation that needs its dry run, and whether its dry run had other arguments |

## Commands

- The same operation with `dryRun=true` — then again without it.
- `agentSessions.get` — `playbook.dryRuns` lists the spending operations with a dry run pending in the session.

## Playbooks

- Agents: call the command with `dryRun=true`, tell the person the estimate in one line, then call it for real with
  the same arguments. Do not retry the real call without the dry run.

## Sources

- Cadence recommendation — docs/spec/08-resolutions.md R16 (playbooks), R12 (estimates).
