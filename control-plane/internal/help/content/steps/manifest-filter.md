---
title: manifest_filter (step kind)
summary: Drops segments that would hurt training — wrong length, text that does not fit the audio, no text, the bot's channel, disputed pseudo-labels, the wrong language, little speech, crosstalk.
contexts: [step:manifest_filter, artifact:segments]
---

## What this is

`manifest_filter@2` is a runtime-neutral core step kind (CPU, job kind `data`). It reads a `segments` artifact and keeps
the segments that pass every rule; each dropped segment is counted under its first failing reason in the header's
`filtered` (added to what an earlier filter counted). Reasons, in the order they are checked:

| Reason | Dropped when |
| --- | --- |
| `duration` | outside `[min_duration, max_duration]` |
| `role` | the channel role is not in `roles` (the bot's channel by default) |
| `origin` | the transcript origin is in `drop_origins` (disputed pseudo-labels go to triage, never to training) |
| `empty_text` | no transcript and `require_text` |
| `chars_per_second` | characters (without spaces) per second outside `[min_chars_per_second, max_chars_per_second]` |
| `language` | `languages` is set and the segment's language (primary subtag) is not in it |
| `lid_mismatch` | language identification (`lid.language`, which `pseudolabel_ensemble` writes on every row it has evidence for) names another language than the segment's: another primary subtag, outside every group of `lid_equivalents` |
| `speech_ratio` | the voice-activity share is below `min_speech_ratio` |
| `crosstalk` | another channel speaks during more than `max_crosstalk` of the segment |

## Place in the loop

Data — `pipelines/data-ingest.yaml`, after `text_normalise`, before `speaker_disjoint_split`. `datasets.preview` shows
the same kinds of filters on a registered version without running anything.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `min_duration` | `data.filter_min_duration_s` (0.5) | Cadence recommendation | 0–60 s |
| `max_duration` | `data.filter_max_duration_s` (30) | NeMo ASR recipes | 1–600 s |
| `min_chars_per_second` | `data.filter_min_chars_per_second` (1) | NeMo SDP | 0–50 |
| `max_chars_per_second` | `data.filter_max_chars_per_second` (30) | NeMo SDP | 1–100 |
| `require_text` | `true` | Cadence recommendation | true, false |
| `roles` | `[caller, mono]` | phase-4 plan (the caller is the target) | `caller`, `bot`, `mono` |
| `drop_origins` | `[pseudo-label:disputed]` | phase-4 plan decision 6 | origins |
| `languages` | `[]` (any) | Cadence recommendation | BCP 47 |
| `drop_lid_mismatch` | `true` | spec 04 Block 1 | true, false |
| `lid_equivalents` | `pseudolabel.lid_equivalents` (`[[sr, hr, bs]]`) | Cadence recommendation (Valk and Alumäe 2021) | groups of primary subtags (version 2) |
| `min_speech_ratio` | `data.filter_min_speech_ratio` (0.2) | Cadence recommendation | 0–1 |
| `max_crosstalk` | `data.filter_max_crosstalk` (0.5) | Cadence recommendation | 0–1 |

## Commands

- `pipelines.run` with `data-ingest`; `pipelineRuns.get` — the step's log lists what was dropped and why.
- `datasets.preview` — hours per language and split under filters, after registration.

## Playbooks

- Most segments dropped as `empty_text`: the audio is untranscribed — add the pseudo-label step before normalising.
- Many `chars_per_second` drops on a pre-segmented corpus: transcripts and audio files are misaligned; check the
  corpus before loosening the bounds.

## Sources

- docs/spec/04-blocks.md Block 1 (filter on duration, characters per second, empty or mismatched-language segments).
- NVIDIA NeMo Speech Data Processor (duration and character-rate filters).
