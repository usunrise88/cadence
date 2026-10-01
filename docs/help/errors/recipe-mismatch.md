---
title: Recipe does not fit the run
summary: The run's pipeline (its recipe) has no step of the model family's train kind, has an input a run cannot fill, or lacks a parameter the request sets.
contexts: [error:recipe-mismatch, field:pipeline]
---

## What this is

A `422 Unprocessable Entity` problem of type `recipe-mismatch`: the pipeline a run executes (`pipelines/train-stage.yaml`
unless the request names another, `training.pipeline` in `defaults.yaml`) does not fit the run. A run's recipe must:

| Rule | Why |
| --- | --- |
| Have exactly one step whose kind is the family's `train` role | A run is one optimisation stage of the base model's family |
| Declare only inputs a run can fill: `mix` (the rendered mix), `dataset` (the mix's only dataset), `base_model` (the base model, or the start checkpoint for `init: checkpoint`), `checkpoint` (the start checkpoint) | The run fills the inputs by type |
| Have a train kind with the parameters the request sets: `steps` (the step budget), `seed`, and for `runs.stage` a learning-rate parameter (`peak_lr`, `learning_rate` or `lr`) | Otherwise the request cannot be honoured |

An average step kind (`checkpoints.average`) must consume exactly one input, of type `checkpoint`. Nothing was
written.

## Place in the loop

Train. Recipes are committed pipeline files of the project repository; a run records the pipeline, its version and
the commit it was read at, so it can be reproduced.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/recipe-mismatch` |
| `status` | `422` |
| `detail` | What does not fit and how to fix it |

## Commands

- `pipelines.list` — the project's pipelines with their inputs and steps.
- `modelFamilies.get` — the family's roles (which kind trains).
- `stepKinds.get` — a kind's parameters.

## Playbooks

- Agents: read the pipeline (`pipelines.list`) and the family (`modelFamilies.get`); propose the fix to
  `pipelines/train-stage.yaml` in the session's worktree, or name the pipeline that fits (`pipeline`).

## Sources

- docs/spec/04-blocks.md Block 2; docs/spec/08-resolutions.md R13, R41, R44.
- RFC 9457, Problem Details for HTTP APIs.
