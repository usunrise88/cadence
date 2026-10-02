---
title: latency_score (step kind)
summary: Latency to final at real-time pace (R54) — from each utterance's speech end, found by a frame-VAD step, to the partial whose text is final — p50 and p95 per eval cell.
contexts: [step:latency_score, artifact:metric_scores, artifact:vad, artifact:hypotheses]
---

## What this is

`latency_score@1` is a core step kind (CPU, job kind `eval`, every runtime image). It measures what a caller waits
for after they stop talking: the time from the end of speech to the final transcript, with audio fed at real-time pace,
reported at p50 and p95 (R54; Pipecat's "time to final segment" is the same measure).

Inputs:

- `hypotheses` — a streaming transcription with partial events (`audioOffsetMs`, `emitMs`, `text`);
- `data` — the dataset it decodes;
- `vad` — utterance ends from a frame-VAD step on the same dataset (`frame_vad@1` in the NeMo pack): JSON lines
  `{audio, durationS, speech: [[start, end], …], speechEndS}` after a header line naming the VAD model.

Output `scores`, a `metric_scores` artifact: `summary.json` (`metric` `latency`, `pace`, `utteranceEnd` `vad`, `vad`
— kind, model, revision —, `profile`, `utterances`, `measured`, `p50Ms`, `p95Ms`, `meanMs`, `maxMs`, `earlyFinals`,
`noSpeech`, `emptyFinals`) and `utterances.jsonl` (`index`, `audio`, `speechEndMs`, `finalPartial`, `finalAtMs`,
`latencyMs`).

How:

- **The final** of an utterance is the first partial from which the text no longer changes (it equals the final text).
  Latency = its emit time − the speech end. A final emitted before the VAD's end (the VAD's hangover after the last
  word) counts as 0 and is counted in `earlyFinals`; utterances without speech or with an empty final are counted and
  left out.
- **Real-time pace.** A decode run at real-time pace (`decoding.pace: realtime` in the hypotheses) is used as it is. An
  eval's file decode runs faster than real time, so its emit times at real-time pace are simulated from its own compute
  times: chunk k's audio becomes available at `audioOffsetMs[k]` and takes `emitMs[k] − emitMs[k−1]` to decode (the
  streams of a batch decode together, as concurrent calls on one card do), so
  `emit[k] = max(audioOffsetMs[k], emit[k−1]) + compute[k]` — a decoder slower than real time queues. `pace` says
  `simulated` or `realtime`.
- Percentiles interpolate linearly between ranks (NumPy's default).

When it is unavailable: the transcribe step of a family without streaming writes no partials (no latency step is
planned), and without a published VAD step kind (a runtime without the NeMo pack) `evals.get` reports latency to final
as unavailable with that reason. The aligned reference's end (forced alignment) and per-channel VAD of call recordings
arrive in phase 4.

## Place in the loop

Evaluate — `vad-g<n>a<k>` once per dataset, then `latency-u<n>` per streaming cell. The summary is kept beside the
cell's eval record (`evals.get` → `metrics.latency`), keyed by the record key, the scorer and the VAD step kind.

## Fields and defaults

No parameters.

## Commands

`evals.new`, `evals.get`.

## Sources

- docs/spec/08-resolutions.md R54; docs/spec/04-blocks.md "Task and streaming metrics"; docs/spec/03-pipelines-defaults.md
  "Scorers and metrics"; docs/review/2026-10-02-phase-3-plan.md stream R.
