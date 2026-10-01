---
title: nemotron_transcribe (step kind)
summary: The Nemotron family's transcribe role — decode a dataset with a checkpoint in true cache-aware streaming at a latency profile and write hypotheses with words, confidence and partial events.
contexts: [step:nemotron_transcribe, artifact:hypotheses, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_transcribe@1` fills the `transcribe` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, a
card, job kind `eval`). It streams every utterance of a `dataset` (input `data`) through a `checkpoint` (input
`model`) with NeMo's **cache-aware streaming decoder** — chunk by chunk with the encoder caches carried, never an
offline decode relabelled — at the latency `profile`:

| Profile | `att_context_size` | Latency |
| --- | --- | --- |
| `80ms` | [56,0] | 80 ms |
| `160ms` (default, primary cell) | [56,1] | 160 ms |
| `320ms` | [56,3] | 320 ms |
| `560ms` | [56,6] | 560 ms |
| `1120ms` | [56,13] | 1120 ms |

Decoding is greedy RNN-T in fp32 with the language prompt of the dataset's language (or `target_lang`) and the locale
tag stripped. The `hypotheses` artifact (R42) has one JSON line per utterance: `audio` (the BLAKE3 hash of its audio),
`text`, `words` (`word`, `start`, `end` in seconds, `confidence` — NeMo's word confidence, minimum over the word's
tokens, or null), `decoding` (profile, context, decoder, prompt) and its `decodingHash`, `family`, `weightsHash`, and
`partials`: after each chunk that reached the utterance, `audioOffsetMs` (audio consumed including the chunk's right
context), `emitMs` (wall time since the batch's decode started; a batch's streams decode together) and the text so far;
the last is `final`. Word times come from those emissions, so their resolution is one chunk (the profile's latency).

One language per step; scoring (normalisation, WER) belongs to the eval step kinds (phase 3).

## Place in the loop

Evaluate — the eval matrix's decode step (phase 3); the conformance suite runs it at every profile.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.nemo.profile` (160ms) | Key defaults (eval latency [56,1]) | 80ms, 160ms, 320ms, 560ms, 1120ms |
| `batch_size` | `packs.nemo.transcribe_batch_size` (32) | Spike A3 | 1–256 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |

## Commands

`pipelines.run`; evals from phase 3.

## Playbooks

None yet (phase 3 evaluation).

## Sources

- NeMo `examples/asr/asr_cache_aware_streaming/speech_to_text_cache_aware_streaming_infer.py` (v3.0.0), as run in spike
  A3 step 3; docs/spec/08-resolutions.md R42 (hypotheses), R43 (latency profiles).
