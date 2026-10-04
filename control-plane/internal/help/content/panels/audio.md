---
title: Audio
summary: Hear and see one utterance or span — waveform, spectrogram, the hypothesis words marked against the reference and the reference words at their aligned times — played through a short-lived signed link; loop, zoom, attach the span to Chat, export the words.
contexts: [panel:audio, command:view.audioPlay]
---

## What this is

A floating tool panel with the audio view (`@/shell/audio`, R51). It shows the utterance last opened from an Eval
report's utterance table (the speaker button of a row), from Diff (**Open in Audio**) or from a span reference. Diff
carries the same view, compact, under its alignment. A workspace does not open it on its own: the Eval, Data and
Triage workspaces keep a floating slot for it, and it appears there the first time you open audio, so no empty window
covers the documents (dock it like any panel if you prefer it at an edge).

Tracks, top to bottom, on one time axis that runs left to right in every locale:

| Track | Shows |
| --- | --- |
| Ruler | Time, labels as fine as the zoom needs |
| Waveform | Min/max per 10 ms per channel (a call: one lane per channel); red ticks mark clipping |
| Spectrogram | dB magnitude, mel or Hz axis, one per channel. Audio of 8 kHz origin stops at 4 kHz (the band above is dark) |
| Hypothesis | The model's words at their times, shaded by confidence; ≠ substituted (hover for the reference word), + inserted, − a deleted reference word |
| Reference | The golden set's reference words at the times its alignment (`align_reference`) gives them, outlined; a reference that stayed unaligned shows as text with the reason, never at guessed times |
| Overview | The whole audio with the visible range; drag it to scroll |

The hypothesis track appears when the view knows the eval cell: its hypotheses and scores artifacts. The reference
track appears when it knows the cell's golden set and the golden set has been aligned (Eval report and Diff rows pass
both). Comparing the two lanes shows where the model put a word against where it was said. Words are real text:
Hebrew reads right to left inside each word's box, and the transcript under the view follows the language's direction
— click a word to move the playhead there.

Short audio (up to `views.audio.browser_stft_max_s`, 10 minutes) gets its spectrogram computed in your browser, in a
background worker. Longer audio — a call recording — reads a tile pyramid from the server: the first view asks the
control plane to build it once (*Building the spectrogram…* while the `media.spectrogram` job runs), and every later
view reads it; audio of 8 kHz origin keeps bins to 4 kHz only. Its waveform comes from peaks stored when the dataset
version was registered: an overview of the whole recording at a coarse step, and 10 ms detail for the part you zoom
into. All views of a window share one WebGL2 renderer, so a dozen views across popouts keep drawing; if the graphics
context is lost the views keep their last picture and redraw when it returns.

## Keys

Active only while a view has the focus (click it or Tab to it):

| Key | Does |
| --- | --- |
| Space | Play or pause |
| ← / → | Seek a tenth of the visible span |
| = (+) / − | Zoom in / out around the playhead |
| [ / ] | Loop starts / ends at the playhead |
| , / . | Previous / next word |
| Ctrl/Cmd + wheel | Zoom at the pointer; the wheel alone scrolls |

Click the waveform or spectrogram to move the playhead; drag across it to select a span, which becomes the loop.

## Place in the loop

Evaluation · Review: after Diff shows *what* the model wrote, Audio lets you hear *why* — noise, crosstalk, clipping,
a word swallowed at a cut. Phase 4's Triage and Annotate use the same view.

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `views.audio.window_ms`, `hop_ms`, `n_fft` | 25 ms, 10 ms, 512 | The spectrogram's STFT (the model's frame grid) |
| `views.audio.axis`, `fmax_hz` | mel, 8000 | Frequency axis |
| `views.audio.range_db`, `gain_db` | 80 dB, 0 | Shown range below the peak; gain |
| `views.audio.colormap` | magma | Also viridis, cividis, inferno, grey, inverse grey; never jet or rainbow |
| `views.audio.words_max_visible` | 400 | Above it the word track shows density blocks; zoom in for words |
| `media.signed_link_ttl_s` | 300 s | How long a playback link works |
| `media.max_span_s` | 600 s | Longest span converted to 16 kHz in one response (a stored 16 kHz WAV plays whole at any length) |
| `media.max_conversions`, `media.span_cache_mb` | 2, 2048 MB | Conversions at once (one more waits: `media-busy`); converted spans kept for the next plays |
| `media.play_audit_window_s` | 600 s | A play is written to the audit log once per viewer, span and channel within it |
| `media.tiles_max_s`, `media.tiles_retry_s` | 14 400 s, 600 s | Longest audio the server builds a tile pyramid for; how long a failed build is reported ([media-tiles-failed](../errors/media-tiles-failed.md)) before a view builds again |
| `media.tiles_retention_days` | 14 days | A server tile pyramid not viewed for this long leaves the content store (a daily sweep, no approval); the next view builds it again |

The toolbar changes colormap, axis, range and gain for this view without touching the defaults.

## Commands

- `view.audioPlay`, `view.audioSeekBack`, `view.audioSeekForward`, `view.audioZoomIn`, `view.audioZoomOut`,
  `view.audioLoopIn`, `view.audioLoopOut`, `view.audioPreviousWord`, `view.audioNextWord` — the keys above.
- **Attach to Chat** — the span as `@utt:<id>#t=1.20,2.35` (W3C Media Fragments) in the composer.
- **Export…** — the hypothesis words as Praat TextGrid, NIST CTM or WebVTT (with per-word timestamps).
- `audio.sign`, `audio.get`, `peaks.get`, `spectrogram.get`, `words.get` — the media operations behind the view (people
  only: a signed-in person or a signed link for its viewer; agents, API keys, worker and host tokens read no raw
  audio).

## Playbooks

- **A cell's WER jumped.** Eval report → the cell → the worst utterance → speaker button: listen to the span, read the
  marks, loop the substituted word.
- **The player stopped after a pause.** Press Space again; an expired link is replaced
  ([media-link-invalid](../errors/media-link-invalid.md)).
- **Discuss a moment with the agent.** Drag across it, **Attach to Chat**, ask.
- **No reference track.** The golden set has no alignment yet: run `pipelines/align-reference.yaml` on it (an
  aligner must cover its language; otherwise the references stay unaligned and the track shows them as text).

## Sources

- docs/spec/08-resolutions.md R25 (audio serving), R51 (one audio view, many tracks), R52 (spectrograms).
- docs/spikes/S5-audio-view.md (renderer per window, tile format, budgets).
- W3C Media Fragments URI 1.0, temporal dimension.
