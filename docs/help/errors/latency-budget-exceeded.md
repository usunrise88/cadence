---
title: Latency budget exceeded
summary: A canary promotion needs a benchmark of the export that held the latency budget at the target's concurrency; the newest benchmark at that concurrency failed it or was inconclusive.
contexts: [error:latency-budget-exceeded, command:models.benchmark, command:deployments.promote, artifact:benchmark_report]
---

## What this is

A `422 Unprocessable Entity` problem of type `latency-budget-exceeded`, answered before an approval is asked when
the export's benchmark at the target's concurrency did not pass. A benchmark streams the parity sample's audio
through the staging server at real-time pace at each concurrency level; a chunk's latency runs from the moment its
audio is complete to its tokens coming back. The budget (R31, read by spike E1): **p95 chunk latency ≤
`deploy.latency_budget_over_chunk_ms` (100 ms)** at the target's `concurrency` (else `deploy.target_concurrency`,
32), so a word waits at most the profile's chunk plus 100 ms.

| Verdict | Why | What to do |
| --- | --- | --- |
| failed | p95 above the budget at the target's concurrency | A faster engine, a smaller concurrency per card, or another card class; `maxStreamsWithinBudget` says where the export stops |
| inconclusive | Processes outside Cadence used more than `deploy.benchmark_max_foreign_util_pct` of the card during that level | Benchmark again when the card is quiet (a `benchmark` availability window at night) |

Spike E1: the fp32 TensorRT engine at 80 ms held p95 20 ms at 32 streams and 63 ms at 256 streams on the stand's
card (beside idle resident services, so a lower bound).

## Place in the loop

Block 4, Deploy: export → parity → **benchmark** → shadow → canary.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/latency-budget-exceeded` |
| `status` | `422` |
| `detail` | The benchmark, its verdict, p95 and budget at the concurrency |

## Commands

- `models.get` — `exports[].benchmarks`: newest first, with `p95ChunkLatencyMs`, `maxStreamsWithinBudget`,
  `contended` and `verdict`.
- `models.benchmark` — benchmark again (the card is taken alone; it drains for up to
  `deploy.benchmark_drain_max_minutes`).

## Playbooks

- Never promote on an inconclusive benchmark: it was measured beside other load.

## Sources

- R30, R31; docs/spec/06-platform.md "Staging serving"; docs/spikes/E1-onnx-triton.md "Triton".
