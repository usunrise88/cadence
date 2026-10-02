---
title: Eval-only dataset
summary: The dataset version may be evaluated but never trained on — a golden or replay test set, or built from a source not cleared for training.
contexts: [error:eval-only-dataset, field:datasets]
---

## What this is

A `422 Unprocessable Entity` problem of type `eval-only-dataset`: a mix (`mixes.new`, `mixes.edit`,
`mixes.preview`, accepting a mix draft), a run estimate (`runs.new`) or a pipeline run (`pipelines.run`, dry run
included) named a dataset version that must not reach training (docs/spec/08-resolutions.md R18). `pipelines.run`
refuses a `dataset` input of such a version, or a `mix` input referencing one, only when a training step
(`resources.jobKind: training`) reads it; steps that evaluate, import or export may read eval-only data. The same
check runs again when a training step is queued with inputs other steps produced (a step that passes a golden set
through fails the run with this reason). A training step reads only **registered** dataset versions: a `dataset`
artifact that no dataset version registers is refused too, and a `mix` input counts by its content (the
`cadence.mix/1` rendering `runs.new` writes: every dataset version and dataset artifact it names), never by what the
caller says in `meta`. A dataset version is eval-only when either holds:

| Why | How to tell | What changes it |
| --- | --- | --- |
| It was registered for evaluation only | `datasets.get` shows `dataset.evalOnly: true`; the collection is tagged `eval-only` (golden and replay test sets, e.g. `dataset/replay-golden-de-de`) | Nothing: such a version is never trained on. Import the training split instead |
| One of its sources is not cleared for training | `dataset.sourceIds` names a source whose `trainingCleared` is `false` (`sources.get`) | A person clears the source: `sources.edit` with `trainingCleared: true` |

Imported sources start eval-only. Clearance is read when the mix or run is checked, so clearing a source makes every
version built from it trainable without a re-import. Nothing was written.

## Place in the loop

Data → Train. Imports (`pipelines/import`) register sources, utterances and a frozen dataset version; a person
decides whether a corpus may be trained on (its licence allows it and the data may be used); mixes and runs refuse
what is not cleared, so evaluation data can never leak into training.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/eval-only-dataset` |
| `status` | `422` |
| `detail` | The first eval-only version and why |
| `errors` | One entry per mix field that failed (`/groups/0/datasets/1`), eval-only ones included |

## Commands

- `sources.get`, `sources.list` — a source's licence and `trainingCleared`.
- `sources.edit` — clear a source for training (`trainingCleared: true`). An agent's call waits for a person's
  approval (preset rule `registry-changes`).
- `datasets.get` — `dataset.evalOnly`, `dataset.sourceIds`.

## Playbooks

- An unregistered dataset artifact: import it (`pipelines/import`), clear its source, and train on the version
  through a mix (`runs.new`).

- Agents: do not retry with the same version. Tell the person which source needs clearing and why you need it, or
  choose a dataset version that is cleared.
- Golden and replay test sets belong in evaluations (phase 3), never in a mix.

## Sources

- docs/spec/08-resolutions.md R17 (replay golden sets), R18 (sources, eval-only until cleared).
- RFC 9110, HTTP Semantics, §15.5.21 422 Unprocessable Content; RFC 9457, Problem Details for HTTP APIs.
