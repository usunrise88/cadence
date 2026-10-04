---
title: Spectrogram tiles could not be built
summary: The control plane's last attempt to build the spectrogram tile pyramid of this audio failed; it tries again after media.tiles_retry_s.
contexts: [error:media-tiles-failed]
---

## What this is

A `422 Unprocessable Content` problem of type `media-tiles-failed`, answered by `spectrogram.get` when the audio view
asks for the tile pyramid of long audio and the `media.spectrogram` job that builds it has failed within the last
`media.tiles_retry_s`. The detail names the job and its error: the audio could not be read (a file on a mount that
moved, a codec the control plane does not decode), the content store was full, or the settings in `views.audio` do
not make an STFT (an FFT size that is not a power of two, a window longer than the FFT).

Nothing else is affected: the waveform, the words and playback work without the spectrogram.

## Place in the loop

Review. Audio longer than `views.audio.browser_stft_max_s` (a call recording) is too long for the browser to
transform; the control plane builds its pyramid once, on the first view, and every later view reads it
(docs/spec/06-platform.md "Media", R52).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/media-tiles-failed` |
| `status` | `422` |
| `detail` | The job (`job_…`) and its error |

| Default | Value | Meaning |
| --- | --- | --- |
| `media.tiles_retry_s` | 600 s | How long a failure is reported before a request builds again |
| `media.tiles_max_s` | 14 400 s | Longest audio a pyramid is built for |
| `views.audio.window_ms`, `hop_ms`, `n_fft`, `tile_frames` | 25 ms, 10 ms, 512, 512 | The STFT and tile shape |

## Commands

- `jobs.get` — the failed job's error and log.
- `spectrogram.get` — after `media.tiles_retry_s`, asking for the manifest builds the pyramid again.

## Playbooks

- **The file moved on its mount.** Run `mounts.scan` on the mount, then open the audio again.
- **The settings changed.** Check `views.audio` in `defaults.yaml`: `n_fft` must be a power of two at least as long as
  the window.

## Sources

- docs/spec/06-platform.md "Media: audio and the live channel (phase 3)"; docs/spec/08-resolutions.md R52.
