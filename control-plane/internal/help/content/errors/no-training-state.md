---
title: No training state
summary: runs.resume found no saved training state for the run; it can only be started again or continued from a checkpoint.
contexts: [error:no-training-state]
---

## What this is

A `409 Conflict` problem of type `no-training-state`: `runs.resume` continues the same stage from the run's last
`training-state` artifact (optimiser and sampler state), and the run never saved one — its train step failed or was
cancelled before it stopped at a checkpoint boundary, or the step kind writes no training state. Nothing was
written.

A training step saves its state when it is asked to stop (a pause, a closing availability window, a cancel) and at
the end; the control plane finds the newest one among the leases of the run's train step.

## Place in the loop

Train. Resume keeps the optimiser state; a new stage (`runs.stage`) starts a new optimiser from a checkpoint.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/no-training-state` |
| `status` | `409` |
| `detail` | The run and what to do instead |

## Commands

- `runs.new` — start the stage again (finished steps with the same inputs are reused).
- `runs.stage` — a new stage from one of the run's checkpoints (`checkpoints.list`).
- `jobs.resume` — a paused step job resumes by itself from its saved state.

## Playbooks

- Agents: check `runs.get` (the timeline says which step failed and why) before starting again.

## Sources

- docs/spec/04-blocks.md Block 2 (continue by resuming or a new stage); docs/spec/08-resolutions.md R19.
- RFC 9110 §15.5.10 409 Conflict; RFC 9457.
