---
title: Sweep over its GPU-hour cap
summary: sweeps.run refused a sweep whose runs' estimates add up to more than its GPU-hour cap.
contexts: [error:sweep-over-cap, field:gpuHourCap, field:runs, field:parameters]
---

## What this is

A `422 Unprocessable Entity` problem of type `sweep-over-cap`: `sweeps.run` adds up the estimates of every point of
the sweep (each prepared as a run of the experiment, the same estimate `runs.new?dryRun=true` gives) and the sum is
more than `gpuHourCap` (default `sweeps.gpu_hour_cap`). The detail says the total, its range and how many of the
first points would fit. Nothing was written and no run was queued.

The same type answers when the first point alone would pass the cap at the moment the sweep starts. Later, while the
sweep runs, the cap does not fail a command: the sweep stops (state `stopped`) before a run whose estimate, added to
the GPU-hours the sweep's runs already used, would pass it.

## Place in the loop

Run → experiment. An experiment pins a mix revision and a base model; `sweeps.run` generates its runs from a grid or
a random draw over recipe parameters and queues them one after another on the project's training slot.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/sweep-over-cap` |
| `status` | `422` |
| `detail` | The total estimate, its range, the cap, and how many points fit |

## Commands

- `sweeps.run?dryRun=true` — the points with their estimates, `estimateGpuHours`, `withinCap` and `fits`.
- `sweeps.run` again with fewer points (`runs`, fewer grid values), fewer `steps`, or a larger `gpuHourCap`.
- `runs.calibrate` — a measured estimate is usually tighter than the table's.

## Playbooks

- Agents: dry-run first, then shrink the sweep to the points that fit, or ask the person for a larger cap; the cap is
  what they agreed to spend on the question.

## Sources

- docs/spec/04-blocks.md "Experiments and sweeps"; docs/spec/08-resolutions.md R12 (estimates).
- RFC 9457, Problem Details for HTTP APIs.
