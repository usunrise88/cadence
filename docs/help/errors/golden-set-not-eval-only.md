---
title: Dataset is not eval-only
summary: goldenSets.freeze named a dataset version that was not registered eval-only; a golden set is frozen only from a test set imported for evaluation.
contexts: [error:golden-set-not-eval-only, field:datasetVersionId]
---

## What this is

A `422 Unprocessable Entity` problem of type `golden-set-not-eval-only`: `goldenSets.freeze` (or its dry run) named a
dataset version whose payload says `dataset.evalOnly: false`. A golden set must be audio no run could ever train on, so
it is frozen only from a dataset version registered eval-only — imported with `evalOnly: true` in its
`dataset.json` header (golden and replay test sets such as `dataset/replay-golden-he`). A version that is eval-only
only because its source is not cleared yet does not qualify: clearing the source would make it trainable. Nothing was
written and no approval was requested.

## Place in the loop

Data → Evaluate. Import the test split as its own eval-only dataset version (`pipelines/import.yaml` with
`evalOnly: true`, or the replay import), then freeze it with a scoring normalizer.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/golden-set-not-eval-only` |
| `status` | `422` |
| `detail` | The dataset version and why it does not qualify |

## Commands

- `datasets.get` — `dataset.evalOnly`; eval-only collections carry the tag `eval-only`.
- `goldenSets.freeze` — with a dataset version registered eval-only.

## Playbooks

- Agents: do not retry with the same version. Find (`registry.search kind:dataset_version tag:eval-only`) or import an
  eval-only test set, and tell the person which one you propose to freeze.

## Sources

- docs/spec/04-blocks.md Block 3 (golden sets are frozen, never trainable).
- docs/spec/08-resolutions.md R17, R18 (eval-only datasets).
- RFC 9457, Problem Details for HTTP APIs.
