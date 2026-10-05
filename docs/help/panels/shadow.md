---
title: Shadow
summary: A shadow deployment's nightly replays — how far the candidate's transcripts of the night's calls diverge from the comparison model's, night by night with its interval, the hours replayed against what a canary needs, and the most divergent segments, which open in Diff and Audio.
contexts: [panel:shadow, entity:deployment, entity:shadow_replay, command:shadowReplays.list, command:shadowReplays.get, command:shadowReplays.new, command:deployments.get, error:shadow-volume-short, topic:shadow, topic:deploy]
---

## What this is

A tool panel (bottom of the Ops workspace). It shows one shadow deployment: the one the active document names (a
deployment, or the shadow of the model version in the active Model document), else the one picked in its toolbar
from the project's deployments.

- **Progress**: the model version, its latency profile and the staging target; the model it is compared with (the
  slot's production version, or the project's baseline before any production — a base model is decoded with the
  family's own decoder, an exported model version through the staging server); the hours of calls replayed against
  `deploy.shadow_min_hours` (what a canary needs), calls, nights and segments; the newest night's divergence; when the
  next nightly replay starts.
- **Divergence per night**: one point per finished night — the WER of the candidate's transcripts against the
  comparison model's over every replayed segment — with its bootstrap interval (resampled by call). A rising line
  means the two models disagree more; it says nothing about which one is right.
- **Nights**: each replay with its state (`running`, `done`, `failed`, `skipped`), calls, hours, segments and
  divergence. A skipped night says why (no calls not replayed yet, the mount or the staging server down); a night past
  `deploy.shadow_artifact_retention_days` says its texts were cleared — its summary stays.
- **Most divergent segments** of the selected night (up to `deploy.shadow_worst_segments`): the call and the window,
  both transcripts and their WER. **Diff** opens the segment in the Diff panel (the comparison model's words over the
  candidate's, aligned after the basic normalisation the scorer uses) and plays it in the Audio panel.
- **Replay now…** answers the calls the replay would take and its estimate first; **Start replay** runs it (GPU spend
  under the usual policy: it may wait for an approval). The nightly replay at `deploy.shadow_replay_at` does the same
  by itself; one replay of a deployment runs at a time.

Marking a segment for triage comes with the flywheel's triage path (phase 5, stream F2); it is not here yet.

The texts are derived from production audio: they are kept `deploy.shadow_artifact_retention_days` (the
captured-sample class, R28) and never go to an LLM judge before PII redaction (R29).

## Place in the loop

Block 4, Deploy: export → parity → benchmark → **shadow** → canary → production.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| `shadow.hours` | — | Replayed call audio; each call counts once, by its duration |
| `deploy.shadow_min_hours` | 20 h | What a canary needs (`shadow-volume-short` otherwise) |
| `deploy.shadow_replay_at` | 02:00 | When the nightly replay starts, in `policies.timezone` |
| `deploy.shadow_replay_max_hours` | 4 h | The most call audio one night takes, newest calls first |
| `deploy.shadow_worst_segments` | 50 | Segments a night's report lists |
| `deploy.shadow_artifact_retention_days` | 90 | Days the texts and segment audio are kept |

## Commands

| Action | Operation | Notes |
| --- | --- | --- |
| (the nights) | `shadowReplays.list` | Newest first; live through `shadow.{deployment}` |
| (a night's segments) | `shadowReplays.get` | The most divergent segments while the texts are kept |
| Replay now… / Start replay | `shadowReplays.new` | Dry run first; GPU spend under the usual policy |
| (the deployment) | `deployments.get` | Progress, comparison model, next replay |

## Playbooks

- Read the divergence with the interval: a canary needs the hours, and a person decides whether the divergence is
  acceptable from the segments, not from the number alone.

## Sources

- docs/spec/03-pipelines-defaults.md "Shadow replay"; docs/spec/02-domain-projects-registry.md "Deployments";
  [Shadow volume short](../errors/shadow-volume-short.md); [Diff](diff.md); [Audio](audio.md).
