---
title: Gate not passed
summary: models.register publishes only a checkpoint whose latest gated eval passed the project's gate.
contexts: [error:gate-not-passed, field:checkpointId, field:evalId]
---

## What this is

A `409 Conflict` problem of type `gate-not-passed`: `models.register` was asked to publish a checkpoint whose latest
gated eval (or the eval named in `evalId`) failed the gate, or that has no gated eval at all. Registration publishes a
model version to every project with its eval report (R22), so only a checkpoint that beat the baseline under the
project's `gates.yaml` qualifies. Nothing was written.

## Place in the loop

Evaluate → register. `evals.new` scores the checkpoint against the baseline; `evals.gate` turns the primary-profile
cells into a verdict; `models.register` publishes a checkpoint whose verdict is `passed`.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/gate-not-passed` |
| `status` | `409` |
| `detail` | The eval and the checks that did not pass |

## Commands

- `evals.get` — the eval's cells, deltas and the verdict's checks (`gate.checks`).
- `evals.new` then `evals.gate` — evaluate the checkpoint again (after more training, or on other golden sets).
- `gates.get` — the gate the verdict used.

## Playbooks

- Agents: report the failing checks and their numbers (delta, interval, baseline and candidate WER) and propose the
  next run; a gate is changed only by a person (`gates.edit` is approval-gated).

## Sources

- docs/spec/04-blocks.md Block 3 ("The gate"); docs/spec/08-resolutions.md R22.
- RFC 9457, Problem Details for HTTP APIs.
