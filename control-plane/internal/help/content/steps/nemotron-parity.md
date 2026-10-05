---
title: nemotron_parity (step kind)
summary: The Nemotron family's parity reference — decode the parity sample exactly as an eval does (NeMo's cache-aware streaming pipeline, fp32, batch 8) with each utterance's token ids, and write the smoke inputs the delivery script streams through the production server.
contexts: [step:nemotron_parity, artifact:hypotheses, artifact:smoke_inputs, artifact:checkpoint, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_parity@1` fills the `parity` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, a card, job
kind `eval`): the reference side of `models.parity`. It decodes the parity sample (a `dataset`, input `data`) with the
`checkpoint` (input `model`) exactly as [`nemotron_transcribe@4`](nemotron-transcribe.md) decodes an eval — NeMo's
cache-aware streaming pipeline in fp32 at matmul precision "highest", greedy RNN-T, the eval's batch of 8, the same
endpointing — so parity compares the served engine with what the gate judged.

Outputs:

- `hypotheses`: the rows `nemotron_transcribe` writes, the same decoding config and hash, plus `tokens`: every token
  id of the utterance in order, read from the hypothesis the pipeline hands its greedy decoder on the stream's last
  step (endpointing never resets the RNN-T state, so it covers the whole stream; the locale tag included). An empty
  transcript has no tokens. `parity_score` compares these with the tokens the served engine returned.
- `smoke` (`smoke_inputs`, `cadence.smoke-inputs/1`): `smoke.json` (`items`: audio hash and file per utterance) and
  one `cadence.nemo-chunks/1` file per utterance for the first `smoke_utterances` utterances in dataset order — the
  feature buffers this decoder sends (pre-encode cache and chunk, right-padded to the profile's buffer, float32 in
  base64, the prompt index of the language). The delivery bundle ships them with the deployable's
  `client/transcribe`, which streams them through the newly installed model and prints its token ids; the delivery
  script compares them with what the staging server returned.

Spike E1 measured the reference against the exported step graph on 200 FLEURS sr test utterances at 80 ms: 198/200
identical token sequences with TF32 off (99.0 %), WER equal; NeMo against itself (batch 8 against batch 1) agreed on
197/200.

Measured on the staging card (2026-10-04): 20 FLEURS sr utterances (hr-HR prompt) in 38 s with the model load, the
card at +3.8 GB; their smoke inputs streamed through the deployable on Triton 26.08 gave 20/20 identical token
sequences ([`nemotron_export`](nemotron-export.md)).

## Place in the loop

Deploy — the `reference` step of the pipeline `models.parity` generates (beside the family's `serve` step through the
staging server, then `parity_score`).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.nemo.profile` (80ms) | Key defaults | the family's profiles |
| `batch_size` | `packs.nemo.transcribe_batch_size` (8) | Spike E1 (parity at the eval's batch) | 1–256 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |
| `stop_history_eou_ms` | `packs.nemo.live_stop_history_eou_ms` (800) | Spike A5 | 80–10 000 ms |
| `smoke_utterances` | `packs.nemo.parity_smoke_utterances` (20) | 02 "Delivery bundle" (≤ 20) | 0–20 |

## Commands

`models.parity`.

## Playbooks

None yet (phase 5).

## Sources

- docs/spikes/E1-onnx-triton.md "2 · Parity" and docs/spikes/e1/parity.py (`capture_tokens`).
- docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)"; docs/spec/08-resolutions.md R31.
