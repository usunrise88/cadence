---
title: toy_live (step kind)
summary: The toy pack's live role — serves a manual transcription session on the CPU with the toy model, so the live channel runs end to end without a card.
contexts: [step:toy_live, job-kind:interactive]
---

## What this is

`toy_live@1` fills the `live` role of the `toy-ctc` family (runtime `toy`, no card, job kind `interactive`). Like
[`nemotron_live`](nemotron-live.md) it loads the session's targets (a toy checkpoint `model.<n>`, or a toy base model
`base.<n>`: the untrained network from its seed), dials the control plane's relay with the lease's live token and
speaks the live channel (`cadence_worker.live.serve`: microphone, file and span inputs, telephony simulation, pace,
`finalize`, `end`, `summary`).

Each target streams through the GRU with its hidden state carried, decoding the model frames (20 ms) as they become
complete and sending a partial after every chunk of the profile (320 ms; the `offline` profile streams in 320 ms
chunks too). A segment's final is the greedy CTC decode of its frames, with word times and confidences from the
alignment; `finalize` closes the segment and resets the state. The family cannot boost, so a target with a boost list
is refused. Nothing is written.

## Place in the loop

Evaluate, by hand — on the CPU toy worker, to try the Transcription panel and the relay without a GPU.

## Fields and defaults

Set by the control plane from `transcriptions.new`: `session`, `targets`, `input`, `telephony`, `pace`,
`maxFileSeconds`, `maxFileBytes`, `frameMs` (see [`nemotron_live`](nemotron-live.md)).

## Commands

`transcriptions.new`.

## Playbooks

None.

## Sources

- docs/spec/06-platform.md "Media: audio and the live channel"; docs/spec/08-resolutions.md R45 (the toy pack keeps
  the seams honest), R47–R50.
