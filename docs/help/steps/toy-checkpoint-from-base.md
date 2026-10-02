---
title: toy_checkpoint_from_base (step kind)
summary: The toy pack's materialize role — the untrained toy network from a base model's seed, as a checkpoint.
contexts: [step:toy_checkpoint_from_base, artifact:checkpoint, artifact:base_model]
---

## What this is

`toy_checkpoint_from_base@1` fills the `materialize` role of the `toy-ctc` family (runtime `toy`, CPU, job kind
`eval`). The toy family has no upstream weights, so its "base model" is the untrained network: the step reads a
`base_model` artifact (input `base`, `cadence.base_model/1`), seeds PyTorch with the base model's `model.seed`
(default 0) and writes a `checkpoint` with the layout of a trained toy checkpoint (`model.pt`, `config.json`,
`tokenizer.json`) and the meta `family`, `step` 0, `valWer` null and `weightsHash`. It refuses a base model of
another family. It exists so evaluations can be tried end to end on the CPU.

## Place in the loop

Evaluate — the materialize step of an eval pipeline on the toy runtime; the conformance suite's materialize stage.

## Fields and defaults

No parameters: the base model artifact names the seed.

## Commands

`pipelines.run`; `evals.new` with a toy base model.

## Playbooks

None.

## Sources

- docs/review/2026-10-02-phase-3-plan.md (decision 6); docs/help/guides/toy-pack.md.
