---
title: shadow_score (step kind)
summary: The neutral judge of a night's shadow replay — compares the candidate's decode of the callers' segments with the current model's, call by call, and writes a shadow_report with the night's divergence, its interval and the most divergent segments.
contexts: [step:shadow_score, artifact:shadow_report, artifact:hypotheses, artifact:segments, command:deployments.new]
---

## What this is

`shadow_score@1` is the last step of the `shadow-replay` pipeline the control plane runs every night for each shadow
deployment (03 "Shadow replay"). It runs on the CPU in every runtime and never names a family. Inputs:

| Input | Type | What |
| --- | --- | --- |
| `segments` | `segments` | The night's calls as `sdp_ingest` indexed them (`cadence.segments/1`): each segment's hash, mount URI, time range and role; `files.jsonl` gives each call file's duration |
| `candidate` | `hypotheses` | The shadow deployment's export, decoded through the staging server |
| `current` | `hypotheses` | The comparison model: the slot's production version, or the project's baseline before any production |

Both decodes key their rows by the segment's audio hash (`segments_cut` cuts the callers' segments; the bot's scripted
turns are not decoded and are not counted). It writes `report` (`shadow_report`, `cadence.shadow/1`), a directory:

- `report.json` — `calls`, `hours`, `utterances` (segments both models decoded), `missing` (segments only one side
  decoded), `divergence` `{wer, ci: [lo, hi], level, samples, words, errors}`, the mean `confidence` of each side when
  its rows carry one, `callList` `[{call, duration, segments, wer, errors, words}]` and `worst`, the most divergent
  segments (most word errors first) with both texts;
- `segments.jsonl` — one row per compared segment: `audio`, `uri`, `call`, `start`, `end`, `duration`, `wer`,
  `errors`, `words`, `candidate`, `current`, `candidateConfidence?`, `currentConfidence?`.

**Divergence** is the word error rate of the candidate's text with the current model's text as the reference, pooled
over every compared segment, after `normalize` (`basic`: NFC, case folded, punctuation stripped). It is not an error
rate against the truth — nobody transcribed these calls — but how far the candidate would change what production
hears. Its interval is a percentile bootstrap that resamples **whole calls** (segments of one call share a speaker, a
line and a topic, so they are not independent; R54).

**Hours.** A call counts once, by its file's duration, when at least one of its segments was compared; the control
plane adds the night's hours to the deployment's `shadow.hours`, which a canary promotion needs to reach
`deploy.shadow_min_hours` (`shadow-volume-short` otherwise).

**Retention.** The texts are derived from production audio: the report lives under the captured-sample retention
class and the control plane evicts it after `deploy.shadow_artifact_retention_days` (the night's summary on the
deployment stays). No LLM judge reads it before PII redaction (R28, R29).

## Place in the loop

Block 4, Deploy: export → parity → benchmark → **shadow** (`deployments.new`, then nightly at
`deploy.shadow_replay_at`) → canary → production. The Shadow panel draws the divergence over nights and opens the
worst segments in Diff and Audio.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `worst_segments` | `deploy.shadow_worst_segments` (50) | Cadence recommendation | 0–1000 |
| `bootstrap_samples` | `eval.bootstrap_samples` (1000) | Bisani & Ney, ICASSP 2004 | 100–100000 |
| `confidence` | `eval.confidence` (0.95) | spec 03 "Key defaults" (Significance) | 0.5–0.999 |
| `seed` | `eval.bootstrap_seed` (1) | Cadence recommendation | 0–2147483647 |
| `normalize` | `basic` | Cadence recommendation | `basic`, `none` |

## Commands

`deployments.new` (a shadow deployment with its `replay` mount); `deployments.get` (`shadow`: hours, nights,
divergence); `artifacts.get` on a night's report while it is kept.

## Playbooks

- A night with `missing` segments: one serve step returned fewer rows than the other (a stream that failed); the
  divergence covers what both decoded.
- A high divergence with a narrow interval across nights is a real behaviour change: open the worst segments in Diff
  and Audio before a canary.

## Sources

R28, R29, R54; docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)" (Shadow replay); Bisani &
Ney, "Bootstrap estimates for confidence intervals in ASR performance evaluation", ICASSP 2004.
