---
title: Mix
summary: The training mix — groups of frozen dataset versions with weights, temperature and replay share, the preview of hours per language, and agents' drafts to accept or revert.
contexts: [panel:mix]
---

## What this is

A mix says what a training run reads: named **groups** of frozen dataset versions, each with a **weight**, a
sampling **temperature** and a **replay share**. It is project work with revisions (docs/spec/08-resolutions.md
R13): every save is a new revision, and a run records the revision it trained on. The **preview** shows the training
hours per language, source (dataset version) or group as a bar chart and a table, with the share of samples each
language and group gets — computed from dataset-version metadata, without reading audio or using a GPU. While you edit,
the preview follows your unsaved values (`mixes.preview`, nothing is saved) and says so; the chart has a table view with
CSV copy. **Launch a run with this mix** opens the launch card: the estimate first (`runs.new?dryRun=true`: GPU-hours and
duration with their range, steps × seconds per step and whether that is measured or from the table, the card, the
data hours, today's budget), an optional step budget, then **Start run**, which opens the new Run document. A mix with
unsaved changes must be saved first; an agent's run over budget waits for an approval.

An agent's edit does not change the mix: it lands as a **draft** — the dashed accent outline above the table, with
the agent's badge (for example "opencode · session 9"), the changes on hover, and **Accept** and **Revert**. While an
agent is editing, the header shows "agent editing" and the table waits, so the two of you never race.

## Place in the loop

Training · Prepare → Check. You shape the mix (or accept an agent's draft), check the hours per language, then
calibrate and dry-run a training run with it (runs arrive in phase 2).

## Fields and defaults

| Field | Meaning | Default |
| --- | --- | --- |
| Group | A name, one or more frozen dataset versions (`ver_…`, `@alias` or a collection name), a weight and a replay flag | weight `mix.group_weight` = 1 |
| Replay | Replay groups hold the base model's other locales; together they get the replay share of the samples | off |
| Temperature | A group is drawn with probability ∝ weight^(1/temperature): 1 follows the weights, larger values flatten | `mix.temperature` = 1 |
| Replay share | Share of samples from replay groups (0 when the mix has none): a slider and a number, with "Why this default?" and a note when it departs from the default | `mix.replay_share` = 0.15 |
| Sample share | Probability that a training sample comes from a group or a language | computed |
| Train hours | The train split's hours of each dataset version, split evenly between a version's locales | from metadata |

Defaults and safe ranges come from `defaults.yaml` (the `defaults://` resource). Every referenced dataset version
must exist and be frozen; a replay share needs a replay group; a mix needs a group that is not replay.

## Commands

- `mixes.edit` — Edit mix: change weights, replay flags, temperature, replay share or groups and **Save**. Your save
  carries the revision you edited; if the mix moved on meanwhile you get the conflict notice (`412
  precondition-failed`): **Reload** discards your changes, **Reapply my changes** puts them on top of the new
  revision for another Save. Nothing is ever overwritten silently.
- `mixes.new` — New mix… (palette): a name, a target dataset version and an optional replay one.
- `mixes.preview` — the preview without saving: the panel calls it while you edit; agents use it before editing.
- `runs.new` — Launch a run with this mix: a dry run for the estimate, then the run on this revision.
- `drafts.accept` — Accept an agent's draft: it becomes the next revision, attributed to you, with the draft as its
  cause. A draft made on an older revision is stale (`412 draft-stale`): revert it or ask the agent to redo the edit.
- `drafts.revert` — Revert (discard) the draft; the mix stays as it is.

## Playbooks

- **Ask an agent to rebalance a mix.** The agent calls `mixes.preview`, then `mixes.edit`; its draft appears here
  live with the session badge. Hover "N changes" to see the diff, then Accept or Revert.
- **Add replay for a new language.** Add a group with the other locale's dataset version, tick Replay, and keep the
  replay share near 0.15 (docs/spec/03-pipelines-defaults.md "Key defaults").

## Sources

- docs/spec/08-resolutions.md R13 (mix as project work with revisions; draftable); R53 (charts: ECharts bars with a
  table view).
- docs/spec/06-platform.md "Real-time model" (drafts, attribution, presence, concurrency).
- Replay share: NVIDIA recommends replay; the 15 % share is a Cadence recommendation. Temperature sampling over
  groups: Cadence recommendation.
