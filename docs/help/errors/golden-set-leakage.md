---
title: Golden set leakage
summary: A dataset version and a golden set share utterances — by content or by a fingerprint — so one of them cannot be used the way it was asked.
contexts: [error:golden-set-leakage, field:datasets, field:datasetVersionId]
---

## What this is

A `422 Unprocessable Entity` problem of type `golden-set-leakage`: golden-set audio would reach training, or training
audio would be scored as held-out. Cadence compares utterances by identity (the content hash of the audio) and by every
fingerprint the imports wrote (`utterance_fingerprints`: `audio-b3` today, an acoustic fingerprint in phase 4). It is
answered in three places:

| Where | What overlaps | What it protects |
| --- | --- | --- |
| `goldenSets.freeze` (dry run included, and before the admin is asked) | The dataset version to freeze shares utterances with a dataset version not registered eval-only | A golden set is never audio a run may train on |
| `mixes.new`, `mixes.edit`, `mixes.preview`, accepting a mix draft, `runs.new` (and its dry run), `pipelines.run` | A dataset version — directly, or through a mix — that a training step reads shares utterances with a golden set | Runs cannot reference golden-set audio by construction (docs/spec/04-blocks.md Block 3) |
| `projects.adopt` of a golden set | A dataset version the project trained on (its runs' mixes, its pipelines' training inputs) shares utterances with it | A project never scores a model on audio it trained on (spec 02, "Adoption re-runs the leakage check") |

Nothing was written.

## Place in the loop

Data → Train → Evaluate. Golden sets are frozen once per language and domain and pinned by evals and gates; the
leakage check is what makes their scores mean "held out". Selecting checkpoints uses validation splits only.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/golden-set-leakage` |
| `status` | `422` |
| `detail` | What was refused and the first overlap |
| `errors` | One entry per overlap, path `/overlaps/<n>`: "`<dataset> <version> (ver_…)` shares N utterances with `<golden set or dataset> <version> (ver_…)`", with how many matched only through a fingerprint of another utterance. A mix lists its failing fields (`/groups/0/datasets/1`) first |

## Commands

- `goldenSets.list`, `goldenSets.get` — the golden sets and the dataset version each holds (`goldenSet.datasetVersionId`).
- `datasets.get`, `utterances.list` (filter `dataset`) — what a dataset version holds; `utterances.get` shows an
  utterance's fingerprints and every dataset version it is in.
- Re-import the training data without the overlapping utterances (a new dataset version), then use that version.

## Playbooks

- Agents: do not retry. Tell the person which dataset versions overlap which golden set and how many utterances; propose
  a re-import without them. Never try to work around the check (renaming, re-mixing the same versions).
- At freeze: choose a test split disjoint from every training set (FLEURS test for FLEURS train), or remove the
  utterances from the training set first.

## Sources

- docs/spec/04-blocks.md Block 3 (golden sets never trained on; leakage by construction impossible), Block 1 step 6.
- docs/spec/02-domain-projects-registry.md "Registry" (adoption re-runs the leakage check), "Data entities as built"
  (utterance fingerprints).
- RFC 9457, Problem Details for HTTP APIs.
