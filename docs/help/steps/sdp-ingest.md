---
title: sdp_ingest (step kind)
summary: Indexes audio on a mount in place — decode, split stereo calls per party, resample to 16 kHz, voice activity, segments with mount URIs and canonical hashes; no audio is copied.
contexts: [step:sdp_ingest, artifact:segments]
---

## What this is

`sdp_ingest@3` is a runtime-neutral core step kind (every runtime image, CPU, job kind `data`). It walks a path on a
mount, decodes every audio file (WAV in pure Python; μ-law WAV, FLAC, MP3, OGG/Opus, M4A through ffmpeg), splits a
stereo call into one track per party, resamples each track to 16 kHz, finds speech per channel with an energy voice
activity detector and cuts it into segments. It writes a **`segments`** artifact: where each segment lives on the
mount (`mount://` URI with its time range and channel) and its **canonical hash** — nothing else. The audio stays on
the mount until a dataset version is frozen (`dataset_freeze`, mode `cut`).

A segment's canonical hash is the BLAKE3 of its canonical WAV: 16 kHz, mono, 16-bit PCM with a 44-byte header, cut
from the whole channel resampled once (SciPy's polyphase filter), samples `[round(start·16000), round(end·16000))`. It
equals the content hash of the WAV `dataset_freeze` cuts later, so an utterance has its identity, its leakage check and
its fingerprint before anything is copied.

**The source file's fingerprint.** Every segment also carries `file-b3`: the canonical hash of the whole track it was
cut from (the file's channel, or the file mixed down) — exactly what `dataset_import` hashes when it imports that file
as one utterance (its `audio-b3`). `dataset_freeze` writes it into the draft's fingerprints, and the leakage check
matches `file-b3` against `audio-b3`: a golden set imported from the Hub (FLEURS test) is found in a draft that re-cut
the same files from a mount by voice activity, though no segment's own hash equals an imported one.

**The test split stays out.** `exclude` leaves out every file under a `test/` directory below `path` by default (globs
on the path relative to `path`, fnmatch: `*` crosses `/`): a corpus's test split is where golden sets come from. Pass
`exclude: []` to read everything; pointing `path` at a `test/` directory itself excludes nothing below it — the freeze's
leakage check still refuses golden-set audio.

**A list of files (version 3).** `files` names the exact files to read under `path` (relative paths, in that order);
`pattern` is then ignored, while `exclude`, `max_files` and `max_hours` still apply. A listed file that does not
exist, lies outside `path` or is not audio fails the step. The control plane uses it for a night's shadow replay: it
walks the deployment's calls mount, picks the newest calls not replayed yet up to `deploy.shadow_replay_max_hours` and
passes them here. Leave it `[]` (the default) to read every file `pattern` matches.

**Pre-segmented corpora.** A corpus of one utterance per file (FLEURS, Common Voice) takes `segmentation: file`: each
file stays one segment, whose hash is the hash an import of the same file gets. Version 1 cut such files at their
pauses.

**No licence, no ingest.** `source` names a registered source (`sources.new`); the pipeline engine refuses the run when
the source is missing, archived or has no usable licence (the parameter is marked `x-cadence.registry: source`).

**Tracks.** A mono file is one track (role `mono`, unless the sidecar names one). A multi-channel file is split when
its roles are known — from the sidecar or `channel_roles` — or when `channels: split`; otherwise it is mixed down (the
mean of every channel at the native rate) and its segments have `channel: -1` and no `ch` in their URI.

**Segments.** With `segmentation: vad` a track's speech runs (frames above the channel's noise floor + `vad_margin_db`
and above `vad_floor_db`; pauses shorter than `vad_min_silence_ms` closed, runs shorter than `vad_min_speech_ms`
dropped) are padded by `vad_pad_ms`, joined where they touch, split at their quietest frame when longer than
`max_segment_s`, and dropped when shorter than `min_segment_s`. With `segmentation: file`, or when a `<stem>.txt`
transcript is present for a single-track file, the track is one segment. A channel with a TTS script in the sidecar
(the bot) takes its segments from the script's turns and its text from the script (origin `model:tts-script`): the
bot's channel is self-labelled.

## Place in the loop

Data — the first step of `pipelines/data-ingest.yaml` (ingest → `text_normalise` → `manifest_filter` →
`speaker_disjoint_split` → `dataset_freeze`, draft) and of `pipelines/pseudo-label.yaml` for audio without
transcripts (then `segments_cut`, the members and `pseudolabel_ensemble` before `text_normalise`).
`datasets.preview` and `datasets.freeze` follow; the "Adapt a new language" playbook runs either.
`pipelines/calls-ingest.yaml` is this step alone, for stereo call recordings with sidecars (`channels: split`, roles
from the sidecar, else `channel_roles`): it ends at the `segments` artifact, the frame of an annotation batch
(`batches.new` with `segments: b3:…`, the index step's output in `pipelineRuns.get`) — the callers have no text yet,
so `data-ingest`'s filter would drop them and its draft would refuse them.

## The segments artifact

Type `segments`, format `cadence.segments/1`, a directory artifact. Every step that rewrites segments keeps the keys it
does not know, so later steps (pseudo-labels, LID) can add their own.

| File | Content |
| --- | --- |
| `segments.json` | `format`, `source: {name}`, `root` (the mount URI walked), `language?`, `files`, `counts: {segments}`, `hours`, `roles`, `splitRule?`, `sourceInfo?: {licence?, url?, revision?}` (from `SOURCE.yaml` at the root), `steps` (each step's `kind@version`), `filtered?: {reason: count}` |
| `segments.jsonl` | One segment per line, keys sorted (below) |
| `files.jsonl` | One line per source file: `uri`, `duration`, `sampleRate`, `channels`, `roles` (per track), `codec?`, `speech` (per track, `[[start, end], …]` in absolute seconds) — what the noise bank and annotation read |

A segment:

| Key | Meaning |
| --- | --- |
| `uri` | `mount://<mount>/<path>#t=<start>,<end>&ch=<n>` (no `ch` for a mixed-down track) |
| `file` | The source file's mount URI without fragment |
| `file-b3` | `b3:<hex>` of the canonical WAV of the whole track the segment was cut from (version 2) |
| `hash`, `bytes` | `b3:<hex>` of the canonical WAV, and its size in bytes |
| `start`, `end`, `duration` | Seconds in the source file, on the 16 kHz sample grid |
| `channel` | The channel index; `-1` = every channel mixed down |
| `role` | `caller`, `bot` or `mono` |
| `language?` | BCP 47 (sidecar, else `language`) |
| `text?`, `origin?`, `confidence?` | A transcript and where it came from: `human` (a `.txt` sidecar), `model:tts-script` (the bot's script), later `pseudo-label` or `pseudo-label:disputed` |
| `speaker?` | Speaker id from the sidecar |
| `split?` | `train`, `validation`, `test` (`speaker_disjoint_split`) |
| `vad` | `{speech: [[a, b], …] seconds from the segment's start, ratio: speech share 0–1}` |
| `level` | `{rmsDb, peakDb, clipping}` — clipping is the share of samples at full scale |
| `crosstalk?` | Share of the segment during which another channel of the same file has speech (split files only) |
| `eou?` | End of utterance (split files only), seconds from the segment's start: `{speechEnd, nextSpeech?, gapS?}` — its last speech end by the channel's VAD and the other channels' next speech start within 10 s; a barge-in gives a negative gap, nobody speaking next leaves `nextSpeech` and `gapS` out |
| `sourceRate`, `codec?` | The file's native sample rate and codec |
| `lid?`, `hypotheses?`, … | Added by later steps (language identification, pseudo-label members) |

## Sidecars

Files beside an audio file `<stem>.<ext>` (written by the corpus fetch scripts, never by Cadence):

| File | Content |
| --- | --- |
| `<stem>.txt` | The whole file's human transcript; the file becomes one segment |
| `<stem>.cadence.json` | `{roles?: ["caller", "bot"], speakers?: ["spk-1", ""], language?: "sr-RS", script?: [{channel: 1, start: 0.8, end: 3.1, text: "…"}]}` — roles and speakers per channel; `script` turns are the bot's segments with their text |
| `SOURCE.yaml` (at the ingest root) | `licence`, `url`, `revision` of the corpus, recorded as `sourceInfo` |

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `source` | required | spec 04 Block 1 (no licence, no ingest) | a registered source |
| `path` | required | phase-4 plan (mount layout `<source>/<revision>/`) | `mount://<mount>/<path>` (a directory or one file) |
| `pattern` | `**/*` | Cadence recommendation | a glob; audio suffixes only |
| `exclude` | `[test/*, */test/*]` | spec 04 Block 3 (golden-set audio never reaches training) | globs on the path under `path`; `[]` reads everything |
| `language` | `""` | Cadence recommendation | BCP 47; the sidecar's wins |
| `channels` | `auto` | spec 04 Block 1 (one track per party) | `auto`, `mono`, `split` |
| `channel_roles` | `[]` | Cadence recommendation | `caller`, `bot`, `mono` per channel |
| `segmentation` | `vad` | Cadence recommendation | `vad`, `file` |
| `vad_frame_ms` | `data.ingest_vad_frame_ms` (20) | Cadence recommendation | 10–100 ms |
| `vad_margin_db` | `data.ingest_vad_margin_db` (12) | Cadence recommendation | 3–40 dB |
| `vad_floor_db` | `data.ingest_vad_floor_db` (−55) | Cadence recommendation | −90 – −20 dBFS |
| `vad_min_speech_ms` | `data.ingest_vad_min_speech_ms` (250) | Cadence recommendation | 0–5000 ms |
| `vad_min_silence_ms` | `data.ingest_vad_min_silence_ms` (300) | Cadence recommendation | 0–5000 ms |
| `vad_pad_ms` | `data.ingest_vad_pad_ms` (100) | Cadence recommendation | 0–1000 ms |
| `max_segment_s` | `data.ingest_max_segment_s` (20) | Cadence recommendation | 1–60 s |
| `min_segment_s` | `data.ingest_min_segment_s` (0.3) | Cadence recommendation | 0–10 s |
| `files` | `[]` (every file `pattern` matches) | spec 03 "Shadow replay" (phase 5) | relative paths under `path`, at most 20 000 |
| `max_files` | `0` (all) | Cadence recommendation | ≥ 0 |
| `max_hours` | `data.max_hours` (0 = all) | Cadence recommendation | 0–10000 h |

## Commands

- `sources.new` — register the corpus with its licence first.
- `pipelines.run` with `data-ingest` — runs this step (dry run first: the plan shows departures from defaults).
- `datasets.preview`, `datasets.freeze` — after the draft is registered.

## Playbooks

- A pre-segmented corpus (one utterance per file, FLEURS): `segmentation: file`; with a transcript per file, a `.txt`
  beside every file.
- "is not on the mount; … holds: …": `path` names a revision that is not there (a template's `set-me-revision`, or a
  revision fetched under another name); set it to one the message lists.
- Stereo calls: write `<stem>.cadence.json` with `roles` (and the bot's `script`), or set `channel_roles`.
- A file changed on the mount after ingest no longer matches its hashes: `datasets.freeze` fails on it; ingest again.
  Running the pipeline again is enough: the control plane lists the files under `path` (path, size, modification
  time; the `exclude` globs applied) into the step's input hash, so a changed, added or removed file runs the ingest
  again, and an unchanged mount reuses the last one (no `fresh: true` needed).

## Sources

- docs/review/2026-10-03-phase-4-plan.md, decisions 3–4 and "Interfaces between streams" (M → D, D → X).
- docs/spec/04-blocks.md Block 1; W3C Media Fragments URI 1.0 (`#t=`).
- docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)", Shadow replay (`files`, version 3).
- NVIDIA NeMo Speech Data Processor (the processor chain this step follows).
