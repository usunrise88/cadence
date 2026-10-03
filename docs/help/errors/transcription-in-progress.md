---
title: A transcription session is already open
summary: You already have a manual transcription session open; one person runs one session at a time, so end it (or close its page) before opening another.
contexts: [error:transcription-in-progress, op:transcriptions.new, panel:transcription]
---

## What this is

A `409 Conflict` problem of type `transcription-in-progress`, answered by `transcriptions.new` when you already have a
session that has not ended — queued, loading its models, or live. The detail names the open session (`trs_…`).

Nothing was queued and no ticket was issued.

## Place in the loop

Evaluate. A transcription is a manual test (R47): you run one to three models on a file, the microphone or an
utterance span and watch the words appear. Each session holds a card's memory while it is open (an interactive job,
R49), so v1 allows one session per person (R48, docs/spec/06-platform.md "Media").

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/transcription-in-progress` |
| `status` | `409` |
| `detail` | The open session's id |

| Default | Value | Meaning |
| --- | --- | --- |
| `transcriptions.session_max_minutes` | 15 min | Longest live session |
| `transcriptions.idle_minutes` | 5 min | A session without audio or keepalive closes after this |
| `transcriptions.ticket_ttl_s` | 60 s | A session whose socket never opens ends a little after its ticket expires |

## Commands

- In the Transcription panel, **Stop** sends `end`; closing the panel or the tab closes the socket. Either ends the
  session within seconds.
- A session whose page vanished without closing its socket is ended by the control plane's sweep (every 30 s) once
  its socket is gone.

## Playbooks

- **You opened the panel in two tabs.** Stop the session in the other tab, or close it; then start again here.
- **The page crashed during a session.** Wait half a minute for the sweep, then start again.

## Sources

- docs/spec/08-resolutions.md R47–R49; docs/spec/06-platform.md "Media: audio and the live channel".
