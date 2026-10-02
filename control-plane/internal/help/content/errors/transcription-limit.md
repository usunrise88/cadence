---
title: Transcription session limit reached
summary: A live session ended at one of its limits — 5 minutes idle, its 15-minute cap (or what the manual-test allowance had left), 15 minutes waiting for a card, or a worker that fell behind.
contexts: [error:transcription-limit, op:stream.connect, panel:transcription]
---

## What this is

A problem of type `transcription-limit` sent as a fatal `LiveError` message on the live socket just before the
session ends. The detail says which limit:

| Limit | Close code | Default |
| --- | --- | --- |
| No audio, `keepalive` or `ping` for this long | 4001 | `transcriptions.idle_minutes` 5 min |
| The session's cap, from the moment its worker joined | 4002 | `transcriptions.session_max_minutes` 15 min, or less when the project's manual-test allowance had less left |
| No card took the session (or its worker never dialled) | 4004 | `transcriptions.queue_wait_minutes` 15 min |
| The job ended before the session started | 4004 | — |
| The worker fell behind: the relay's queue stayed full | 1013 | `transcriptions.backpressure_wait_s` 5 s |

At the idle and session limits the relay sends the worker `end` first, so the words so far arrive as finals and a
`summary` follows before the socket closes.

## Place in the loop

Evaluate. A session holds a card's memory beside training (R49); the limits return it when nobody is using it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/transcription-limit` |
| `status` | `409` |
| `detail` | Which limit |

## Commands

- `transcriptions.new` — a new session (the panel's **Start**).
- The Queue & GPU panel shows what holds the card while a session waits; its place and the reason appear in the
  Transcription panel.

## Playbooks

- **Waiting for a card while training runs.** A training step reserves its card's whole memory cap: the session waits
  for the step to end, or for a card with room. On the staging card (22 GB cap beside a resident service) a session
  fits only when the training step leaves 6 GB of the cap free.
- **Long thinking pauses.** The page sends a keepalive every 20 s while the socket is open; idle means the page went
  away or stopped sending.
- **Backpressure on a file.** Use pace `fast` only when the card is free; at `realtime` the worker keeps up easily.

## Sources

- docs/spec/08-resolutions.md R48 (limits in v1), R49 (interactive compute); docs/spikes/A5-live-transcription.md.
