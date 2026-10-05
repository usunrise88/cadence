---
title: Benchmark missing
summary: A canary promotion needs a finished benchmark of the export at the target's concurrency and profile; there is none (never run, still running, or its run failed).
contexts: [error:benchmark-missing, command:models.benchmark, command:deployments.promote, artifact:benchmark_report]
---

## What this is

A `422 Unprocessable Entity` problem of type `benchmark-missing`, answered before an approval is asked when the
export has no finished benchmark whose levels include the target's concurrency. A benchmark is measured, not assumed:
the latency budget is checked on the staging server with the export's own engine.

| Detail says | What to do |
| --- | --- |
| no benchmark | `models.benchmark` with `{version, profile, target}` (dry run first) |
| the benchmark is running | Wait for its pipeline run; a waiting benchmark drains its card for up to `deploy.benchmark_drain_max_minutes` |
| the benchmark failed | `models.get` shows its error (often the staging server was down: `serving-unavailable`) |
| no level at the target's concurrency | Benchmark again with the target named: its concurrency is always added to the levels |

## Place in the loop

Block 4, Deploy: export → parity → **benchmark** → shadow → canary.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/benchmark-missing` |
| `status` | `422` |
| `detail` | The export and the concurrency the promotion needs |

- Levels: `deploy.benchmark_streams` (1, 8, 16, 32, 64, 128) plus the target's concurrency, each
  `deploy.benchmark_seconds_per_level` (120 s) at real-time pace after a `deploy.benchmark_warmup_seconds` warm-up.

## Commands

- `models.benchmark` — measure the export on the staging server (job kind `benchmark`: the card alone).
- `models.get` — `exports[].benchmarks`.

## Playbooks

- Benchmarks take a card alone and never preempt training: ask a person before one when a run is training.

## Sources

- R30, R31; docs/spec/02-domain-projects-registry.md "Deployments" (checks before an approval); docs/spec/06-platform.md
  "Exclusive benchmarks".
