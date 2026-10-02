---
title: nemotron_live (step kind)
summary: The Nemotron family's live role — serves one manual transcription session; it loads up to three targets, dial the control plane's relay and stream partial and final words back over the live channel; stores nothing.
contexts: [step:nemotron_live, job-kind:interactive, family:nemo.fastconformer-rnnt.cache-aware, panel:transcription]
---

## What this is

`nemotron_live@1` fills the `live` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, one card, job kind
`interactive`). The control plane never pipelines it: `transcriptions.new` enqueues it as a session's interactive job
(R47–R49), with the card memory the family declares (`interactive.memoryMb` 6 000 MB for one model, plus
`extraCheckpointMb` 2 600 MB per further distinct model; spike A5 measured 3.7 GB steady and a 5.6 GB load peak; restoring on the CPU first, as the pack now does, keeps the
peak at the model's 2.6 GiB, and two distinct models with three targets peaked at 5.2 GiB allocated).

The job:

1. loads every target — one to three lanes (`A`, `B`, `C`), each a model input (`model.<n>`, a checkpoint, or
   `base.<n>`, a base model fetched into the Hugging Face cache like `checkpoint_from_base` does), a latency
   `profile`, a `language` and an optional boost list (`boost.<n>`). Targets of one model share its weights: one
   restore, one NeMo streaming pipeline per profile over the same model, the encoder's look-ahead switched before
   each step. A `.nemo` restore takes about 22 s and unpacks 2.5 GB into the lease's scratch;
2. dials the relay, `CADENCE_LIVE_URL` (`/api/worker-live/{jobId}`, set by the harness) with the lease's
   `CADENCE_LIVE_TOKEN` (header `Cadence-Live-Token`) — the worker dials out, as for every lease (R14);
3. serves the session (`cadence_worker.live.serve`) until it ends, then releases the lease `done` with no outputs.

Decoding is NeMo's cache-aware streaming pipeline (`nemo.collections.asr.inference`) in fp32 with greedy RNN-T, the
language prompt per stream and end-of-utterance endpointing — the same decoder [`nemotron_transcribe@3`](nemotron-transcribe.md)
evaluates with, so a live session, a paced replay of a file and an eval give the same words. Two shims fix NeMo
3.0.0 gaps: the per-stream language prompt is applied (as shipped the pipeline drops it and the model emits only
blanks) and the trailing locale tag is stripped. A boost list becomes the stream's own phrase boosting tree (NeMo's
per-stream biasing), so a boosted and an unboosted lane of one model share its weights ("test a phrase").

### The live channel (R48)

The relay forwards the browser's frames unchanged. Up (`LiveClientMessage`): `start` (the input: microphone with its
capture rate and `getSettings()`, a file, or an utterance span), binary audio (PCM16 little-endian mono frames of at
most 20 ms at the capture rate, or a file's bytes), `fileEnd`, `finalize`, `keepalive`, `end`. Down
(`LiveServerMessage`): `started` (per target: profile, chunk, language, load time, decoder), `partial` (replaces the
segment's previous one), `final` (words with start, end and confidence in session audio time, the endpoint `eou`,
`finalize` or `end`, and `space: false` when an end of utterance fell inside a word and the final continues it),
`stats` (real-time factor, audio decoded, about once a second), `pong`, `error` (a problem), `summary` (totals, step
times, peak memory, load times) and close 1000.

- Every input reaches the decoders at 16 kHz through the training resampler (streaming polyphase,
  `scipy.signal.resample_poly`): the microphone from its capture rate, a file after ffmpeg decodes its channel 0 (WAV
  without ffmpeg), a span from the stored audio.
- Telephony simulation: 16 kHz → 8 kHz (polyphase) → G.711 μ-law or A-law → 16 kHz, the chain the training
  augmentation's telephone stage uses (`nemotron_finetune@2`).
- `finalize` is a segment boundary: the pending audio is padded with silence to a whole chunk, end of utterance is
  forced, and the next audio opens a new decoder stream with a fresh encoder cache (its 4.48 s of left context reset).
- A file or span plays at the session's pace (`realtime`: 20 ms of audio per 20 ms; `fast`: as fast as the card
  allows) and ends the session with its summary when it is through. Files are capped at `maxFileSeconds` and
  `maxFileBytes` (problem `transcription-input-invalid`); the uploaded bytes live in the lease's work directory and are
  deleted when the session ends (a worker sweeps scratch a crashed process left after
  `transcriptions.scratch_sweep_minutes`).

Nothing is written: no audio, text or metric outlives the session (R47).

## Place in the loop

Evaluate, by hand: the Transcription panel's targets. Agents never reach it (media endpoints refuse agent tokens);
they test models with `evals.new`, which decodes with the same pipeline.

## Fields and defaults

The control plane sets the session's parameters from `transcriptions.new`: `session`, `targets`
(`[{target, model, profile, language, boost?}]`), `input` (`{kind, start?, end?, channel?}`), `telephony`
(`{codec, sampleRate}` or none), `pace`, `maxFileSeconds`, `maxFileBytes`, `frameMs`.

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `stop_history_eou_ms` | `packs.nemo.live_stop_history_eou_ms` (800) | Spike A5 | 80–10 000 ms |
| `boost_weight` | `packs.nemo.boost_weight` (0.5) | Measured on the staging card | 0–10 |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |

## Commands

`transcriptions.new` (people only; tag `media`). The job shows in Queue & GPU as kind `interactive` and is cancelled
with the session.

## Playbooks

None: a manual test.

## Sources

- docs/spikes/A5-live-transcription.md "Result" (the decoder, its shims, latency per profile, memory, 20 ms frames).
- docs/spec/06-platform.md "Media: audio and the live channel"; docs/spec/08-resolutions.md R47–R50.
- NeMo 3.0.0 `nemo/collections/asr/inference/pipelines/cache_aware_rnnt_pipeline.py` (per-stream prompts, biasing and
  endpointing).
