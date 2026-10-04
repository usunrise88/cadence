---
title: noise_mine (step kind)
summary: Mines a noise bank from the silences of call recordings — per-channel voice activity from sdp_ingest, clips away from any speech, without digital silence or missed speech — registered as a frozen noise_bank version.
contexts: [step:noise_mine, artifact:segments, artifact:dataset, entity:noise_bank]
---

## What this is

`noise_mine@1` is a runtime-neutral core step kind (every runtime image, CPU, job kind `data`). Background noise for
augmentation is most realistic when it comes from the project's own calls (docs/spec/03 "Augmentation"): this step
takes it from the stretches of each recording where nobody speaks.

It reads the `segments` artifact of an ingest (`sdp_ingest`): `files.jsonl` lists every recording with its tracks,
their roles (`caller`, `bot`, `mono`) and each track's speech runs. For each recording:

1. the silences are the stretches at least `edge_margin_ms` away from speech on **any** track of the file, so the
   other party's crosstalk and echo stay out;
2. for each track whose role is in `roles`, every silence is cut into clips of at most `max_clip_s`; pieces shorter
   than `min_clip_s` are dropped;
3. a clip quieter than `min_rms_db` is digital silence (a TTS bot channel, a muted line) and one louder than
   `max_rms_db` is probably speech the detector missed — both are dropped;
4. at most `max_clips_per_file` clips are kept per recording, and the step stops after `max_hours`.

Clips are cut from the same 16 kHz track the ingest hashed and written as canonical WAV; each keeps its `mount://` URI
(`#t=<start>,<end>&ch=<n>`) and role. The output is a `dataset` artifact of purpose `noise` whose header names only the
**registered** source and says how it was mined (`mined: {stepKind, segments, root, roles, files, clips, dropped,
…}`). The control plane's `dataset` hook checks the source (no licence, no ingest:
[source-unlicensed](../errors/source-unlicensed.md)) and registers a frozen version in `noise-bank/<name>` (registry
kind `noise_bank`, tags `noise-bank`, `mined`, `source:<name>`) — no utterances, and a noise bank never enters a mix.

## Place in the loop

Data → Train and Evaluate: augmentation profiles name the noise bank (`transforms.noise.bank`), and the robustness
axis of an eval mixes from it. `pipelines/noise-from-calls.yaml` runs `sdp_ingest` then this step; licensed public sets
(MUSAN, CC BY 4.0) come in through `dataset_import` with `purpose: noise` (`pipelines/noise-bank.yaml`).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `name` | `""` (`<source>-calls`) | Cadence recommendation | collection without `noise-bank/` |
| `roles` | `[caller, bot]` | phase-4 plan stream I | some of `caller`, `bot`, `mono` |
| `min_clip_s` | `data.noise_min_clip_s` (1.0) | Cadence recommendation | 0.2–30 s |
| `max_clip_s` | `data.noise_max_clip_s` (10.0) | Cadence recommendation | 1–120 s |
| `edge_margin_ms` | `data.noise_edge_margin_ms` (200) | Cadence recommendation | 0–2000 ms |
| `min_rms_db` | `data.noise_min_rms_db` (−75) | Cadence recommendation | −120 – −30 dBFS |
| `max_rms_db` | `data.noise_max_rms_db` (−25) | Cadence recommendation | −60 – 0 dBFS |
| `max_clips_per_file` | `data.noise_max_clips_per_file` (20) | Cadence recommendation | 1–1000 |
| `max_hours` | `data.max_hours` (0 = all) | Cadence recommendation | 0–10000 h |
| `tags` | `[]` | Cadence recommendation | ≤ 20; `noise-bank` and `mined` are added |

## Commands

- `pipelines.run` with `noise-from-calls` (dry run first).
- `registry.list` / the Library (kind `noise_bank`) — the mined version, its clips, hours and how it was mined.

## Playbooks

- No clips: the step says how many it dropped as quiet, loud or over the cap. Synthetic calls whose caller channel is
  clean read speech have little line noise: lower `min_rms_db`, or mine real calls once they exist.
- Telephone noise only: `roles: [caller]` (the bot's channel of a real deployment is the PBX's, often digital silence).

## Sources

- docs/spec/03-pipelines-defaults.md "Augmentation" (a noise bank mined from the non-speech regions of the project's
  own call recordings, per-channel VAD).
- docs/review/2026-10-03-phase-4-plan.md decision 9 (synthetic stereo calls) and stream I.
