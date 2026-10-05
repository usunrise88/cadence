---
title: benchmark_score (step kind)
summary: The neutral latency judge — reads the serving timings of each concurrency level and writes a benchmark_report with p50/p95/p99 chunk latency, time to final, RTF, streams per card and the verdict at the target concurrency.
contexts: [step:benchmark_score, artifact:benchmark_report, artifact:serving_timings, command:models.benchmark, job-kind:benchmark]
---

## What this is

`benchmark_score@1` is the last step of the pipeline `models.benchmark` generates (R30, R31). It runs on the CPU in
every runtime and never names a family or a server. Its input `timings` takes one `serving_timings` artifact per
level (`timings.0`, `timings.1`, …): the family's serve role streaming the parity sample at real-time pace through
the staging server, `cadence.serving-timings/1` — a header (`concurrency`, `chunkMs`, `warmupMs`, server, card class)
and rows of type `chunk` (`availableMs`, `doneMs`, `last`), `telemetry` (`utilizationPct`, `memoryUsedMb`,
`foreignUtilPct`), `server` (the server's own counters) and `error`.

Per level it reports chunk latency (from the moment a chunk's audio is complete to its result) and time to final
(the same for an utterance's last chunk) at p50/p95/p99, RTF (mean chunk latency ÷ chunk), errors, serving memory and
the card's use by processes outside Cadence. A level is **within the budget** when its p95 chunk latency is at most
`budget_ms` and no stream failed, **contended** when foreign use averaged above `max_foreign_util_pct`.
`maxStreamsWithinBudget` — streams per card — is the largest level whose every lower level is within the budget too.
The verdict is taken at `target_streams`: contended → `inconclusive`, within the budget → `passed`, else `failed`.

## Place in the loop

Block 4, Deploy: export → parity → **benchmark**. The control plane records it on the export (`models.get` →
`exports[].benchmarks`); a canary promotion needs `passed` at the target's concurrency (`latency-budget-exceeded`,
`benchmark-missing`).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `budget_ms` | `deploy.latency_budget_over_chunk_ms` (100) | R31, read by spike E1 | 0–1000 |
| `target_streams` | `deploy.target_concurrency` (32; the target's concurrency when the request names one) | R31 · confirm | 1–512 |
| `max_foreign_util_pct` | `deploy.benchmark_max_foreign_util_pct` (10) | Cadence recommendation (R30) | 0–100 |

## Commands

`models.benchmark` (dry run first); `models.get`; `artifacts.get` on the report.

## Playbooks

None.

## Sources

R30, R31; docs/spec/06-platform.md "Staging serving"; docs/spikes/E1-onnx-triton.md "Triton" (p95 per chunk at
1–512 streams).
