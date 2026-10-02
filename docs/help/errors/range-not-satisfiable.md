---
title: Range not satisfiable
summary: A byte range asked of an audio file starts past its end; the answer's Content-Range header says how long the file is.
contexts: [error:range-not-satisfiable, field:Range]
---

## What this is

A `416 Range Not Satisfiable` problem (RFC 9110 §15.5.17) from `audio.get`: the request's `Range: bytes=…` header
asked for bytes that are not in the WAV file served, for example `bytes=50000-` of a 40 000-byte span, or a suffix of
zero bytes (`bytes=-0`). The answer carries `Content-Range: bytes */<size>`, the file's length.

Ranges Cadence ignores (it answers the whole file with `200` instead, as the RFC allows): several ranges in one
header, a unit other than `bytes`, or a malformed range.

## Place in the loop

Review. Media elements fetch audio in ranges while they play and seek. A span's WAV is generated for the span asked,
so its length changes with `start`, `end` and `channel`: a player that kept byte offsets from another span gets this
error. Asking again from `bytes=0-` (or with no range) fixes it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/range-not-satisfiable` |
| `status` | `416` |
| `detail` | The range asked and the file's length |

## Commands

- `audio.get` — with no `Range` header for the whole span.

## Playbooks

- **A custom client streams audio.** Read `Content-Length` (or `Content-Range` on a `206`) of the first answer and keep
  ranges inside it for that span.

## Sources

- RFC 9110, HTTP Semantics, §14 Range Requests and §15.5.17 416 Range Not Satisfiable.
- docs/spec/06-platform.md "Media" (audio serving with range requests, R25).
