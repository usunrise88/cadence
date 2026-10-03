---
title: Transcription input invalid
summary: The session's audio could not be used — a file that cannot be decoded or is longer than 15 minutes, a span outside its utterance, or audio sent before start.
contexts: [error:transcription-input-invalid, op:transcriptions.new, op:stream.connect, panel:transcription]
---

## What this is

A `422` problem of type `transcription-input-invalid`. It comes as a `LiveError` message on the live socket (with
`fatal`), or as the answer of the session when its worker could not start it:

- a **file** the worker cannot decode (it decodes with ffmpeg in its temporary directory, else as WAV/FLAC), or one
  longer than `transcriptions.max_file_minutes` (15 min), or bigger than `transcriptions.max_file_mb` as it arrives.
  Before decoding, ffprobe reads the local file only and accepts only audio containers — WAV, FLAC, MP3, Ogg
  (Opus/Vorbis), MP4/M4A (AAC), WebM/Matroska — so a playlist (HLS `.m3u8`), a concatenation list or a video-only file
  is refused; a declared duration over the limit (or a sample rate above 192 kHz) is refused before anything is
  decoded, and the decode itself stops one second past the limit and at a bounded output size, so a small file that
  expands to hours of silence costs nothing;
- binary audio sent **before `start`** (non-fatal: the frame is dropped), or file bytes in a microphone session;
- a **span** whose utterance audio cannot be read.

`transcriptions.new` itself answers `validation-failed` for a span that ends before it starts, names a channel the
utterance lacks, or is longer than the file limit.

Nothing of the input is kept: a file's bytes live in the worker's temporary directory for the session only and are
deleted when the socket closes (a sweep removes what a crashed worker left within an hour).

## Place in the loop

Evaluate. Manual tests take any audio a person has at hand (R47); evaluations that count use golden sets.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/transcription-input-invalid` |
| `status` | `422` |
| `detail` | What was wrong with the input |

| Default | Value | Meaning |
| --- | --- | --- |
| `transcriptions.max_file_minutes` | 15 min | Longest file or span |
| `transcriptions.max_file_mb` | 300 MB | Largest file |
| `transcriptions.max_message_kb` | 64 KB | Largest socket message (files go in chunks) |

## Commands

- `transcriptions.new` — start again with another file or a shorter span.

## Playbooks

- **A long call recording.** Cut the part you want to hear (any audio editor) or import the call and open a span of
  its utterance.
- **An unusual codec or container (AMR, 3GP, a video file).** Convert it to WAV or FLAC first; without ffmpeg in the
  worker image only WAV (and FLAC/OGG/MP3 with soundfile) decode.

## Sources

- docs/spec/08-resolutions.md R47 (files ≤ 15 minutes, decoded with ffmpeg in the worker's temporary directory,
  deleted on close); R48 (the message protocol).
