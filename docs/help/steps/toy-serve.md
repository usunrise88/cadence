---
title: toy_serve (step kind)
summary: The toy pack's serve role — decodes a dataset with a toy deployable through a simulated server queue and writes hypotheses (with token ids) and serving timings, so parity and benchmarks run on a CPU.
contexts: [step:toy_serve, artifact:deployable, artifact:hypotheses, artifact:serving_timings, family:toy-ctc]
---

## What this is

`toy_serve@1` fills the `serve` role of the `toy-ctc` family (phase 5). It reads a `deployable` (from
[`toy_export`](toy-export.md)) and a `dataset` and writes `hypotheses` — one row per utterance with `text`, `words`
and `tokens` (the CTC token ids) — and `timings` (`serving_timings`, `cadence.serving-timings/1`). There is no server:
the model runs in process and `concurrency` streams share it through a simulated queue in which each chunk takes its
measured compute and waits for the chunks before it, so latency grows with concurrency as on a real server. The clock
is simulated (`clock: simulated` in the header): a real-time level costs no wall time.

A family's real serve kind is a streaming client of a staging target's server with the same parameters, inputs and
outputs; the conformance suite and the generated parity and benchmark pipelines call it the same way.

## Place in the loop

Block 4, Deploy — the conformance suite's `parity` and `benchmark` stages; `models.parity` and `models.benchmark` for
a toy model version.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `target` | `""` | The serve role's parameters | ≤ 100 characters (recorded only) |
| `profile` | `packs.toy.profile` (`offline`) | The toy-ctc family descriptor | `offline`, `320ms` |
| `concurrency` | 1 | The serve role's parameters | 1–1024 |
| `pace` | `fast` | The serve role's parameters | `fast`, `realtime` |
| `seconds` | 0 (every utterance once) | Cadence recommendation | 0–3600 |
| `warmup_seconds` | 0; only with `seconds` > 0 (the level lasts warm-up + `seconds`, as `nemotron_serve`'s) | Cadence recommendation | 0–120 |

## Commands

`models.parity`, `models.benchmark`.

## Playbooks

None.

## Sources

docs/review/2026-10-05-phase-5-plan.md "Interfaces between streams" (D2 → D1); docs/spec/03-pipelines-defaults.md
"Export, parity and benchmark (phase 5)".
