---
title: The toy pack and the conformance suite
summary: A tiny CPU CTC model (runtime toy, family toy-ctc) that proves the framework seams on every pull request, and the conformance suite every pack passes.
contexts: [guide:toy-pack, family:toy-ctc]
---

## What this is

A framework pack is the unit of extension (R45): a runtime image, step kinds for a model family's roles, the family
descriptor with its latency profiles, a `defaults.yaml` section and help. The toy pack is the smallest real one:
runtime `toy` (python:3.12-slim with CPU PyTorch, image `cadence/worker-toy`), family `toy-ctc` (a linear layer and a
unidirectional GRU with a CTC head, character tokenizer, log-mel features computed on the fly), latency profiles
`offline` and a simulated streaming `320ms`, and the step kinds `toy_calibrate`, `toy_train`, `toy_average` and
`toy_transcribe`. It trains in seconds on synthetic tone clips and exists only to keep the seams honest; NeMo is the
only real pack. Its kinds read the `dataset` directory artifact `dataset_import` writes (`dataset.json`,
`manifest.jsonl` with `audio` as a path inside the artifact, the audio files) and name each utterance by the BLAKE3
hash of its audio file.

The conformance suite (`python -m cadence_worker.conformance --runtime <runtime>`, `make conformance` for the toy
pack) checks the schemas (complete `x-cadence`, help articles, declared profiles, every role mapped to a published
kind that declares that role) and then imports the pack's fixtures with `dataset_import` (a `folder-csv` folder)
into a `dataset` artifact and runs calibrate → train → stop (training state on cancel) → resume → average →
transcribe for every profile → score through the real harness path, with a local content store and no control plane.
Export and parity join in phase 5.

## Place in the loop

Platform — CI runs the toy pack on every pull request; the NeMo pack runs nightly on the staging card. Start the toy
worker with `docker compose --profile toy up worker-toy`.

## Fields and defaults

The pack's defaults are the `packs.toy` section of `defaults.yaml`: `train_steps` 300, `learning_rate` 0.003, `batch_size`
8, `val_every` 20, `seed` 0, `calibrate_steps` 3, `profile` offline.

## Commands

`runtimes.list`, `modelFamilies.get` and `stepKinds.list` show what a worker published; `pipelines.run` runs its kinds.

## Playbooks

None.

## Sources

docs/spec/08-resolutions.md R40–R45; worker/README.md.
