---
title: latency_score (step kind)
summary: Latency to final at real-time pace (p50, p95) and emission delay (PR50, PR90) per eval cell (R54) — from each utterance's speech end (frame VAD) to its final, and from each reference word's aligned end to its first stable partial.
contexts: [step:latency_score, artifact:metric_scores, artifact:vad, artifact:hypotheses, artifact:alignment]
---

## What this is

`latency_score@3` is a core step kind (CPU, job kind `eval`, every runtime image). It measures what a caller waits
for after they stop talking: the time from the end of speech to the final transcript, with audio fed at real-time pace,
reported at p50 and p95 (R54; Pipecat's "time to final segment" is the same measure). Version 3 (phase 4) adds
**emission delay**: how long after a word is spoken it first shows up in a partial and stays, reported as PR50 and PR90
(Yu et al., FastEmit, ICASSP 2021).

Inputs:

- `hypotheses` — a streaming transcription with partial events (`audioOffsetMs`, `emitMs`, `text`);
- `data` — the dataset it decodes;
- `vad` — utterance ends from a frame-VAD step on the same dataset (`frame_vad@1` in the NeMo pack): JSON lines
  `{audio, durationS, speech: [[start, end], …], speechEndS}` after a header line naming the VAD model;
- `normalizer` (optional) — the golden set's scoring normalizer, to compare words as WER does;
- `alignment` (optional) — word timings of the references (`cadence.alignment/1`, written by
  [align_reference](align-reference.md) and carried by the golden set).

Output `scores`, a `metric_scores` artifact: `summary.json` (`metric` `latency`, `pace`, `utteranceEnd` `vad`, `vad`
— kind, model, revision —, `profile`, `utterances`, `measured`, `p50Ms`, `p95Ms`, `meanMs`, `maxMs`, `earlyFinals`,
`noSpeech`, `emptyFinals`, `emission`) and `utterances.jsonl` (`index`, `audio`, `speechEndMs`, `finalPartial`,
`finalAtMs`, `latencyMs`, `emission: {words, pr50Ms, pr90Ms}`). `emission` is `{available, reason?, aligner,
utterances, alignedUtterances, words, matchedWords, pr50Ms, pr90Ms, meanMs, earlyWords, unalignedUtterances,
mismatchedUtterances}`.

How:

- **The final** of an utterance is the first partial from which the text no longer changes (it equals the final text).
  Latency = its emit time − the speech end. A final emitted before the VAD's end (the VAD's hangover after the last
  word) counts as 0 and is counted in `earlyFinals`; utterances without speech or with an empty final are counted and
  left out.
- **Real-time pace.** A decode run at real-time pace (`decoding.pace: realtime` in the hypotheses) is used as it is. An
  eval's file decode runs faster than real time, so its emit times at real-time pace are simulated from its own
  compute. The hypotheses row carries `steps`, one `[audio available ms, compute ms]` per chunk the stream stepped
  (silent chunks included; compute is the batch step's wall time over the streams it stepped, so the batch size does
  not inflate it), and each partial the `step` that emitted it: chunk k is done at
  `done[k] = max(available[k], done[k−1]) + compute[k]` — a decoder slower than real time queues — and a partial is
  emitted when its chunk is done. Hypotheses written before the steps existed fall back to the partials' own times
  (`emitMs[k] − emitMs[k−1]` as the compute of partial k), which version 1 used and which charged a partial the silent
  chunks before it and the batch's other streams. `pace` says `simulated` or `realtime`, `timing` what a simulation
  used (`steps` or `events`).
- **Emission delay.** Reference and hypothesis words are compared after the scoring normalizer, each reference word
  carrying the aligned end of the whitespace token it came from. A reference word the final hypothesis matches (an `=`
  of the same word alignment WER uses) is emitted by the first partial from which the final's word at that position
  stays in place to the end; its delay is that partial's emit time at real-time pace (as above) minus the word's
  aligned end. A negative delay (a word shown before its aligned end) is kept and counted in `earlyWords`;
  substituted, deleted and inserted words have none. PR50 and PR90 are taken over every matched word of the cell.
- **Never an estimate.** Without an alignment input, a normalizer, or aligned references (the aligner does not cover
  the golden set's language, or the alignment was made for another text) emission delay is `available: false` with the
  reason, and no number is reported.
- Percentiles interpolate linearly between ranks (NumPy's default).
- Measured on the staging card with version 1's event timing (2026-10-02; the base model at 160 ms on the NeMo pack's ten FLEURS he fixture clips,
  batch 32, frame VAD on the CPU): p50 441 ms, p95 674 ms (8 measured, 2 empty finals); under the `telephony` profile
  p50 462 ms, p95 654 ms. The VAD took 10.6 s for the ten clips, model load included; the decode peaked at 4.9 GB.

When it is unavailable: the transcribe step of a family without streaming writes no partials (no latency step is
planned), and without a published VAD step kind (a runtime without the NeMo pack) `evals.get` reports latency to final
as unavailable with that reason. The latency and VAD steps are optional in the eval's pipeline: when one fails, the
eval still finishes and gates on WER, and the cell shows latency to final unavailable with the step's error (or "was
skipped" when the VAD it reads failed). Emission delay needs the golden set's references aligned once
([align_reference](align-reference.md)); augmented cells get none (their timing may have changed).

## Place in the loop

Evaluate — `vad-g<n>a<k>` once per dataset, then `latency-k<n>` per streaming cell, with the golden set's normalizer
and, for unaugmented cells of an aligned golden set, its alignment. The summary is kept beside the cell's eval record
(`evals.get` → `metrics.latency`, emission delay under `metrics.latency.emission`), keyed by the record key, the scorer,
the VAD step kind and the alignment artifact. The Eval report shows both beside WER.

## Fields and defaults

No parameters.

## Commands

`evals.new`, `evals.get`.

## Playbooks

The fine-tune playbook evaluates through `evals.new`, which plans this step when the eval needs it.

## Sources

- Yu, J. et al., "FastEmit: Low-latency Streaming ASR with Sequence-level Emission Regularization", ICASSP 2021.
- docs/spec/08-resolutions.md R54; docs/spec/04-blocks.md "Task and streaming metrics"; docs/spec/03-pipelines-defaults.md
  "Scorers and metrics"; docs/review/2026-10-02-phase-3-plan.md stream R; docs/review/2026-10-03-phase-4-plan.md
  decision 8 (stream L).
