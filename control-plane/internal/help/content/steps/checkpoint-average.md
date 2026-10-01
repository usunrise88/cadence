---
title: checkpoint_average (step kind)
summary: The Nemotron family's average role — the element-wise mean of two or more checkpoints' weights, written as one new checkpoint.
contexts: [step:checkpoint_average, artifact:checkpoint, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`checkpoint_average@1` fills the `average` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`). It runs
on the CPU (no card) directly on the `.nemo` files: every floating-point tensor of the weights is averaged (summed in
float32, cast back), integer buffers and the model configuration and tokenizer come from the first checkpoint. The
checkpoints must share the model configuration and the base model; they arrive as one checkpoint-typed input wired
`checkpoints.0`, `checkpoints.1`, ….

The output `checkpoint` carries `family`, `step` (the newest of the sources), `weightsHash`, `averagedFrom` (the
sources' weights hashes), the base model and the tokenizer reference; it has no validation WER of its own until an
eval scores it. Memory: about two copies of the weights (≈ 5 GB for the 0.6 B model).

## Place in the loop

Train — `checkpoints.average` over a run's kept checkpoints; the result registers as an `averaged` checkpoint of the run.

## Fields and defaults

None: the inputs say what to average.

## Commands

`checkpoints.average`, `checkpoints.list`.

## Playbooks

Fine-tune from a dataset version: average the best kept checkpoints before evaluating.

## Sources

docs/spec/08-resolutions.md R41 (the average role), R42 (neutral checkpoint meta); NeMo's checkpoint averaging practice
(`scripts/checkpoint_averaging`).
