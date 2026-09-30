---
title: Step kind conflict
summary: Another runtime already publishes this step kind at this version, so the worker's registration is refused.
contexts: [error:step-kind-conflict, entity:step_kind]
---

## What this is

A `409 Conflict` problem of type `step-kind-conflict`, answered to `workerRegistrations.new`. A worker publishes its
runtime's step kinds at start; a pipeline pins a step kind as `name@version`, so the pin must mean one thing:

- a **framework** step kind (training, decoding, calibration for one model family) lives in exactly one runtime
  (R40); a second runtime publishing the same `name@version` is refused;
- a **neutral** core kind (`echo`, `dataset_import`) ships in every runtime image, but every runtime must publish it
  with the same parameter schema (the `schemaHash` in `stepKinds.get`); a different schema is refused.

Nothing was registered.

## Place in the loop

Runtimes, step kinds and model families are registry versions the worker publishes (R40, R41, R45); the queue hands
a step to any worker whose runtime published its `name@version`. A conflict would let two different programs answer
the same pin.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/step-kind-conflict` |
| `status` | `409` |
| `detail` | The step kind, its version and the runtime that already publishes it |

## Commands

- `stepKinds.list` (filter `collection=step-kind/<name>`) — who publishes which version.
- `runtimes.list` — the runtimes and their workers.

## Playbooks

- Pack author: rename the framework step kind or bump its version; for a neutral kind, bump its version when its
  parameters change and ship the same schema in every runtime image.

## Sources

- docs/spec/08-resolutions.md R40 (runtimes), R45 (framework packs).
- docs/review/2026-09-30-phase-2-plan.md "Decisions taken for phase 2" — neutral core kinds.
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
