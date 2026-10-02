---
title: entity_score (step kind)
summary: Entity accuracy for number classes — numbers, dates, phone numbers, amounts from the language pack's itn.yaml — compared between the references and a transcribe step's hypotheses.
contexts: [step:entity_score, artifact:metric_scores, artifact:itn, artifact:hypotheses]
---

## What this is

`entity_score@1` is a core step kind (CPU, job kind `eval`, every runtime image). A voice agent fails on a wrong phone
number long before WER notices, so every eval cell of a golden set whose locale has a language pack with ITN classes
also reports how many of the references' numbers, dates, phone numbers and amounts the model got right.

Inputs:

- `hypotheses` — the cell's transcription;
- `data` — the dataset it decodes (the golden set's, or its augmented copy);
- `itn` — the project's `lang/<locale>/itn.yaml` at the pack's version (the last commit that changed the pack),
  rendered by the control plane as JSON (`format` `cadence.itn/1`, `locale`, `commit`, `classes`).

Output `scores`, a `metric_scores` artifact:

- `summary.json` — `schema` `cadence.metric-scores/1`, `scorer` `entity_score@1`, `metric` `entities`, `available`,
  `itn` (`locale`, `commit`), `utterances`, `utterancesWithEntities`, `refEntities`, `hypEntities`, `correct`,
  `accuracy` (correct / reference entities), `precision` (correct / hypothesis entities) and the same per class in
  `classes`. Rates are fractions; null when there is nothing to divide by.
- `utterances.jsonl` — one row per utterance with an entity on either side: `index`, `audio`, `ref` (`class`, `text`,
  `found`) and `extra` (hypothesis entities no reference entity matched).

How:

1. Both texts go through the pack's examples as spoken → written replacements (whole words, case-insensitive, the
   longest spoken form first): the examples-based conversion until the ITN step (phase 4) converts spoken numbers in
   general. A model that writes digits needs none of it.
2. Each class's `pattern` finds its written forms; where matches of several classes overlap, the longest wins (then
   the class listed first), so `054-1234567` is one phone number, not three numbers.
3. A reference entity is found when the hypothesis has an entity of the same class with the same text (whitespace
   ignored); both sides count as multisets per utterance.

Names and addresses need annotated spans in the golden set (phase 4). Entity accuracy is reported, not gated: the gate
gains an entity check only when `gates.yaml` defines one.

## Place in the loop

Evaluate — `entities-u<n>` beside `score-u<n>` in an eval pipeline (and for cells whose WER was cached, from the cached
hypotheses). Its summary is kept beside the cell's eval record, keyed by the record key, the scorer and the ITN file's
hash; `evals.get` shows it under each cell's `metrics.entities`. The step is optional in the eval's pipeline: when it
fails, the eval still finishes and gates on WER, and the cell shows entity accuracy unavailable with the step's error.

## Fields and defaults

No parameters: the pack's `itn.yaml` names the classes, their patterns and examples (`langpacks.edit`).

## Commands

`evals.new`, `evals.get`; `langpacks.get` / `langpacks.edit` for the ITN classes.

## Playbooks

The fine-tune playbook evaluates through `evals.new`, which plans this step when the eval needs it.

## Sources

- docs/spec/04-blocks.md "Task and streaming metrics"; docs/spec/03-pipelines-defaults.md "Language pack" (itn.yaml),
  "Scorers and metrics"; docs/review/2026-10-02-phase-3-plan.md stream R.
