---
title: Manual-test allowance used up
summary: The project used today's GPU-hours of manual transcription tests; evaluate with evals.new, or try again tomorrow.
contexts: [error:transcription-allowance-exhausted, op:transcriptions.new, panel:transcription, field:budgets.manual_test_gpu_hours_per_project_per_day]
---

## What this is

A `429 Too Many Requests` problem of type `transcription-allowance-exhausted`, answered by `transcriptions.new` when
the project's manual-test allowance for today is spent. Nothing was queued.

The allowance is `budgets.manual_test_gpu_hours_per_project_per_day` (1 GPU-hour by default), metered as the wall
time of the project's interactive leases on GPU cards since the start of the day in the instance time zone. It is
wall time, not busy time: a live session holds its card's memory for as long as it is open, even while the decoder
waits for you to speak (spike A5 measured a real-time factor of 0.10–0.17). Manual tests do not count against the
project's GPU budget for training and evals, and evals do not count against this allowance.

A session that is already open also ends when the allowance runs out: its cap is what the allowance had left when its
worker joined.

## Place in the loop

Evaluate. Manual tests are for hearing and seeing what a model does (R47); comparisons that count are evals, which are
stored and reused.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/transcription-allowance-exhausted` |
| `status` | `429` |
| `detail` | The project and its allowance |

| Default | Value | Meaning |
| --- | --- | --- |
| `budgets.manual_test_gpu_hours_per_project_per_day` | 1 GPU-h | Manual tests per project per day |

## Commands

- `evals.new` — evaluate the model on golden sets instead (it uses the project's GPU budget).
- The transcription panel shows the allowance used and left under the lanes.

## Playbooks

- **A long microphone session ate the allowance.** Stop sessions when you are done: an idle session still holds the
  card until `transcriptions.idle_minutes` closes it.
- **The allowance is too small for the team.** An admin raises the default in `defaults.yaml`
  (`budgets.manual_test_gpu_hours_per_project_per_day`, at most 24).

## Sources

- docs/spec/08-resolutions.md R49 (a small daily GPU-hour allowance per project); docs/spikes/A5-live-transcription.md
  proposal 4 (count wall time).
