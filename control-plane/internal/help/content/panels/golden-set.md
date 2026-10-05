---
title: Golden set
summary: A frozen golden set version — the eval-only dataset version and scoring normalizer it pins, locale, size, resampling unit, the projects that use it — and the freeze form (admin approval).
contexts: [panel:golden-set, command:goldenSets.freeze, command:goldenSets.get, command:goldenSets.align, command:projects.adopt]
---

## What this is

A document for one **golden set** version (`goldenSets.get`), a registry entry in `golden-set/<name>`. A golden set
ties an **eval-only** dataset version to one **scoring normalizer** version, so every WER computed on it is comparable
across checkpoints, projects and time (R21). It shows:

- **What it pins**: the dataset version and its content hash, the normalizer version, locale and domain, utterances
  and hours, the fingerprint, and the bootstrap's resampling unit (**call**, **speaker** or **utterance**: confidence
  intervals resample whole groups, so correlated utterances do not narrow them).
- **Word timings**: whether its references are aligned (`goldenSets.align` for several golden sets in one run, or the
  `align-reference` pipeline for one; step [align_reference](../steps/align-reference.md)): aligned utterances and
  words, the aligner, and why the rest stayed unaligned. Emission delay in evals needs them; without them it is n/a.
- **Scoring normalizer**: its rules — Unicode form, case folding, punctuation, combining marks (niqqud), literal
  mappings — applied to both reference and hypothesis before scoring.
- **Used by**: the projects that adopted it and their aliases. Evals score on adopted golden sets; `gates.yaml` names
  the target and replay ones.

A golden set's utterances are kept out of training: freezing checks for leakage, and mixes and pipelines refuse any
dataset that overlaps a golden set (`golden-set-leakage`).

**Adopt into project** (the header's primary action, **Adopt into <project>…** under Used by, or **Adopt…** on the
Library row; `projects.adopt`) opens the adopt card for the open project. It dry-runs first: adopting re-runs the
leakage check against what the project trained on, so a refusal (`golden-set-leakage`) lists the overlapping dataset
versions before anything changes. **Adopt** then adopts it (`data.lock` lists it); name it in `gates.yaml` (Project
home → Gate) to make it a target or replay set.

**Freeze a new version…** opens the freeze form: an eval-only dataset version (`ver_…` or `dataset/<name>`), a
normalizer (default `defaults.yaml` `eval.normalizer`), the collection name, domain and resampling unit. **Check**
shows what would be registered; **Freeze** always waits for an admin's approval and answers with its id (Approvals
shows it).

## Place in the loop

Evaluation · Record: the fixed yardstick every eval measures against.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Dataset version | — | Must be frozen and registered eval-only (`golden-set-not-eval-only`) |
| Normalizer | `eval.normalizer` | The scoring normalizer version both texts are compared after |
| Collection | the dataset's name (`dataset/x` → `golden-set/x`) | Where the version is registered |
| Resample by | speaker when the utterances name speakers, else utterance | The bootstrap's unit (R54) |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Freeze golden set… | `goldenSets.freeze` | Dry run first; the real call waits for an admin's approval |
| Open Lineage | — | Dataset version, normalizer, projects and evals around it |

## Playbooks

- **Make a held-out set a golden set.** Register the dataset version eval-only, open any golden set (or the palette's
  Freeze golden set…), enter the dataset version, Check, Freeze; an admin approves in Approvals.
- **Change the scoring rules.** Register a new normalizer version and freeze a new golden set version with it; old
  and new WERs are then never mixed (R21).

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Golden set); docs/spec/02-domain-projects-registry.md "Evaluation
  entities"; R21, R54 in docs/spec/08-resolutions.md.
