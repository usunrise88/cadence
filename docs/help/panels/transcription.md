---
title: Transcription
summary: A manual test — run one to three models on the microphone, a file or an utterance span and watch their words appear live, with time to final and the real-time factor; nothing is stored.
contexts: [panel:transcription, command:transcriptions.new, error:transcription-in-progress, error:transcription-allowance-exhausted, error:transcription-ticket-invalid, error:transcription-input-invalid, error:transcription-limit]
---

## What this is

A floating tool panel for trying models by ear (R47–R50, docs/spec/06-platform.md "Media"). You pick the input and one
to three targets, press **Go live**, and each target's words stream into its own lane: grey partials that change as
the model hears more, then solid finals that never change. When the session ends the audio view shows the audio with
every target's words on one time axis.

**Inputs**

| Input | What happens |
| --- | --- |
| Microphone | Captured in your browser at the device's own rate (no resampling on the page), channel 0 only, 16-bit PCM frames of 20 ms. **Raw microphone** (default) asks the browser for echo cancellation, noise suppression and automatic gain off, as recognition wants; turn it off to hear what a call stack's processing does to the words. Pick the device; the level meter shows the input and **CLIP** lights when it reaches full scale. |
| File | Its bytes go to the worker running the session (≤ 15 minutes of audio), are decoded there with ffmpeg and the training resampler, played at real time or as fast as the card allows, and deleted when the session closes. |
| Utterance span | `utt_…#t=1.2,3.4` (the span open in the Audio panel fills it in). The worker reads the stored audio. |

**Targets.** Each lane runs a checkpoint of the project, a registered model version or a base model, at a latency
profile (default: the primary profile), in a language (default: the project's first locale), with or without a boost
list of the project's language pack. The same model at `1120ms` beside its deployment profile shows the gap to the
high-latency reference; two targets that differ only in their boost list test a phrase with and without boosting (the Language pack's own
"test a phrase" box is **not built yet**; set up the two lanes here by hand).
Targets of one model share its weights on the card.

**Telephony** simulates a phone line: down to 8 kHz, through G.711 μ-law (or A-law, or none), and back to 16 kHz with
the same streaming polyphase resampler training uses.

**Blind** shuffles the lanes and hides what each one runs until you press **This one is better** on one of them.
Nothing records the pick.

**Lanes.** Words are shaded by confidence (fainter is less sure; hover a word for its times and confidence). A mark
follows each final: ⏎ the model's end of utterance, ⇥ your **Finalize**, ■ the end of the session. **Finalize** is a
segment boundary: the words so far become final and the next audio starts a fresh stream. Hebrew reads right to left;
each word is bidi-isolated, so digits and Latin words keep their order. Type a reference under the lanes for a WER and
a word diff per lane (≠ substituted, − deleted, + inserted), computed on this page with light normalisation — evals are
where numbers are kept and compared. **Copy** takes a lane's final text.

**Figures under the lanes** come from the session's own events and your browser's clock: time to final p50/p95 (from
sending the audio up to a final's end to receiving the final; microphone only), finalize → final, the real-time factor
the worker reports, and round trips to the relay (ping) and to the worker (keepalive, which waits behind audio).

## Place in the loop

Review. A person speaks or listens; agents test models with evals instead (this panel's operation is not an MCP tool
and an agent token cannot reach it).

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `transcriptions.ticket_ttl_s` | 60 s | The socket's single-use ticket |
| `transcriptions.session_max_minutes` | 15 min | Longest live session (less when the allowance has less left) |
| `transcriptions.idle_minutes` | 5 min | Closed without audio or keepalive for this long (the page sends keepalive every 20 s) |
| `transcriptions.queue_wait_minutes` | 15 min | Longest wait for a card |
| `transcriptions.frame_ms` | 20 ms | Microphone frame |
| `transcriptions.max_file_minutes` / `max_file_mb` | 15 min / 300 MB | Largest file |
| `budgets.manual_test_gpu_hours_per_project_per_day` | 1 GPU-h | The project's daily allowance of manual tests, metered as wall time of the session's job |

**While waiting.** The session is an interactive job: it takes the highest queue priority and fits beside training
under the card's memory cap, never beside a benchmark. Until a card has room the panel shows the place in the queue
and why; then the worker loads the models (about 20–30 s) and live mode starts. The microphone meter works meanwhile,
but no audio is sent before the worker says started.

**Closing codes.** 1000 the session ended normally; 4001 idle; 4002 the time cap; 4003 the worker was lost; 4004 the
job could not start; 1013 the worker fell behind; 1001 the control plane is stopping. A new start needs a new
session (a new ticket).

**Errors.**

- `transcription-in-progress`: you already have an open session (one per person); stop it first.
- `transcription-allowance-exhausted`: the project used today's manual-test allowance.
- `transcription-ticket-invalid`: the ticket was used, expired or is unknown, or the page's origin is not allowed.
- `transcription-input-invalid`: the file or span cannot be decoded, or is too long or too large.
- `transcription-limit`: the session hit a limit (idle, time cap, queue wait, the allowance ran out).

**What is kept.** Only the interactive job's record: who, when, which targets and the GPU time (for the queue and the
allowance). No audio, text or metric is stored; the words, the recording and the figures live in this page until you
start again or close it.

## Commands

- `transcriptions.new` — opens the session (from this panel only).
- **Open Transcription** (`view.openTranscription`) in the palette.

## Playbooks

- **Does the fine-tune hear our product names?** Two targets: the base model and the checkpoint, same profile; say a
  few sentences with the names, **Finalize** after each, compare the lanes.
- **How much does latency cost?** The checkpoint twice, at its deployment profile and at `1120ms`.
- **Phone audio.** Turn on Telephony and read the same sentences; the lanes show what an 8 kHz call loses.
- **Firefox and Safari.** Check that the permission prompt appears, that the raw microphone setting holds (the session
  start records what the browser applied), and that the socket opens through the proxy; Safari keeps audio suspended
  until a click, which **Go live** is.

## Sources

- docs/spec/08-resolutions.md R47 (manual tests, nothing stored), R48 (the live channel), R49 (interactive compute),
  R50 (capture in the browser).
- docs/spec/06-platform.md "Media: audio and the live channel".
- docs/spikes/A5-live-transcription.md "Result" (20 ms frames, latencies, the split-word join).
