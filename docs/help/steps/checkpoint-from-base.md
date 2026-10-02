---
title: checkpoint_from_base (step kind)
summary: The Nemotron family's materialize role — turn a base model at its pinned revision into a checkpoint, so an evaluation transcribes the base model the way it transcribes a fine-tune.
contexts: [step:checkpoint_from_base, artifact:checkpoint, artifact:base_model, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`checkpoint_from_base@1` fills the `materialize` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, CPU,
job kind `eval`). It reads a `base_model` artifact (input `base`: Hugging Face repository, pinned revision and `.nemo`
file), takes that `.nemo` from the worker's Hugging Face cache (downloading it there when absent) and writes a
`checkpoint` with the layout every checkpoint of the family has: `model.nemo` and `checkpoint.json` with the neutral
meta `family`, `step` 0, `valWer` null, `weightsHash` (the BLAKE3 hash of `model.nemo`, computed as for a trained
checkpoint), the `base` it came from and its `tokenizer` reference, and `init: base`.

Evaluations always transcribe a checkpoint, so a base model's cells (the baseline, R23) start with this step; its
output is the same for every project and eval, so it runs once per base model version and is reused.

## Place in the loop

Evaluate — `materialize-<model>` in an eval pipeline, before the transcribe and score steps of the base model's cells.
The conformance suite materializes the fixture base model and scores it.

## Fields and defaults

No parameters: the base model artifact pins repository, revision and file.

## Commands

`evals.new` generates it for base models; `pipelines.run` runs it directly.

## Playbooks

None (evals reuse its output for every project).

## Sources

- docs/review/2026-10-02-phase-3-plan.md (decision 6: base models are evaluated through the materialize role);
  docs/spec/08-resolutions.md R23 (baseline), R42 (checkpoint artifacts).
