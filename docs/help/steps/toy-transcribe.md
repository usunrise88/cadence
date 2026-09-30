---
title: toy_transcribe (step kind)
summary: The toy pack's transcribe role — decodes a dataset with a toy checkpoint at a latency profile and writes hypotheses, with partial events when streaming.
contexts: [step:toy_transcribe]
---

## What this is

`toy_transcribe@1` fills the `transcribe` role of the `toy-ctc` family. It reads a `checkpoint` (input `model`) and a
`dataset` (input `data`) and writes `hypotheses` (R42): one JSON line per utterance with `audio`, `text`, `words`
(word, start, end, confidence from the CTC alignment), `decoding` and `decodingHash`, `family` and `weightsHash`.
With the streaming profile `320ms` it feeds the audio in 320 ms chunks, carrying the GRU state, and adds `partials`:
one event per chunk with `audioOffsetMs`, `emitMs` (milliseconds since the decode started) and the text so far; the
last is marked `final`. The model is unidirectional, so the streaming result equals the offline one.

## Place in the loop

Evaluate — the conformance suite's file and streaming transcription stages.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.toy.profile` (`offline`) | The toy-ctc family descriptor | `offline`, `320ms` |

## Commands

`pipelines.run`.

## Playbooks

None.

## Sources

docs/spec/08-resolutions.md R42 (hypotheses), R43 (latency profiles), R54 (partial events), R45.
