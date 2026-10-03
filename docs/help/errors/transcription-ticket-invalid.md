---
title: Transcription ticket invalid
summary: The live socket was opened with a ticket that is unknown, already used or expired; tickets are single-use and live 60 seconds, so open a new session.
contexts: [error:transcription-ticket-invalid, op:stream.connect, panel:transcription, field:ticket]
---

## What this is

A `403 Forbidden` problem of type `transcription-ticket-invalid`, answered before the WebSocket upgrade of
`stream.connect` (`/api/transcriptions/{id}/stream?ticket=…`) when the ticket does not open the session:

- it was used already — a ticket opens one socket, once (a reconnect needs a new session);
- it expired — tickets live `transcriptions.ticket_ttl_s` seconds (60) after `transcriptions.new` answered;
- the session ended, or the id and the ticket do not belong together.

A socket from a page on another site is refused before this check with `forbidden` (the `Origin` must be this
server's own, or one of `CADENCE_ALLOWED_ORIGINS`), and a session belongs to the person who opened it.

## Place in the loop

Evaluate. The ticket and the `Origin` check keep the live channel to the page that asked for it (R48): the socket
carries your microphone's audio.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/transcription-ticket-invalid` |
| `status` | `403` |
| `detail` | Which session |

| Default | Value | Meaning |
| --- | --- | --- |
| `transcriptions.ticket_ttl_s` | 60 s | How long a ticket may open the socket |

## Commands

- `transcriptions.new` — a new session with a fresh ticket (the Transcription panel's **Start**).

## Playbooks

- **The network dropped during a session.** The old socket is gone and its ticket used; press Start again for a new
  session (models load again, about 20 s for Nemotron).
- **Behind a proxy on another host name.** Set `CADENCE_ALLOWED_ORIGINS=https://<that host>` on the control plane, or
  keep the proxy's `Host` header (Caddy's `reverse_proxy` does by default).

## Sources

- docs/spec/08-resolutions.md R48 (single-use ticket valid 60 s; Origin check); docs/spikes/A5-live-transcription.md
  "Browser" (ticket reuse refused).
