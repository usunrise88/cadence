---
title: Audio conversions busy
summary: The control plane is already converting as many audio spans as it runs at once; the player asks again after the Retry-After seconds.
contexts: [error:media-busy]
---

## What this is

A `429 Too Many Requests` problem of type `media-busy` with a `Retry-After` header, answered by `audio.get` when a
span has to be converted (decoded, its channel picked, resampled to 16 kHz and encoded as 16-bit PCM) and
`media.max_conversions` conversions are already running for all viewers together. Nothing was played and nothing
was recorded.

Spans that were converted before are served from the span cache and never wait; so is a stored 16 kHz WAV asked
whole. Only the first request of a new span converts.

## Place in the loop

Review. The audio view asks for the spans it draws (docs/spec/06-platform.md "Media"); a burst of seeks over long,
non-16 kHz audio by several people is the usual cause. The bound keeps conversions from taking the control plane's
CPU from everything else.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/media-busy` |
| `status` | `429` |
| `Retry-After` | Seconds to wait (2) |

| Default | Value | Meaning |
| --- | --- | --- |
| `media.max_conversions` | 2 | Conversions running at once |
| `media.max_span_s` | 600 s | Longest span converted in one response |
| `media.span_cache_mb` | 2048 MB | Converted spans kept for later requests |

## Commands

- `audio.get` — ask again after `Retry-After` seconds.

## Playbooks

- **Several reviewers scrub long calls at once.** Raise `media.max_conversions` in `defaults.yaml` if the control
  plane has CPU to spare; a wider `media.span_cache_mb` keeps more converted spans.

## Sources

- docs/spec/06-platform.md "Media: audio and the live channel (phase 3)".
- RFC 9110 §10.2.3 (Retry-After); RFC 6585 §4 (429 Too Many Requests).
