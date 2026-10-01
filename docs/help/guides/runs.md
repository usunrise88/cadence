---
title: Training runs, calibration, checkpoints and GPU budgets
summary: How a training run pins its start, mix and recipe, how calibration turns estimates measured, how checkpoints register and are averaged, how to resume or start a new stage, and how GPU spend is budgeted.
contexts: [guide:runs, panel:run, panel:metrics, panel:checkpoints, error:family-unavailable, error:recipe-mismatch, error:no-training-state]
---

## What this is

A **run** (`run_…`) is one optimisation stage, reproducible from four references:

| Reference | What the run records |
| --- | --- |
| Start (`init`) | `base`: a base model version at its pinned revision; `checkpoint`: a checkpoint (`ckp_…`) of an earlier run, whose run becomes the **parent** |
| Mix | The mix revision and the content hash of its rendered `input_cfg` (the `mix` artifact: groups, weights, sampling probabilities and the dataset artifacts training reads) |
| Recipe | The pipeline (`pipelines/train-stage.yaml` by default, `training.pipeline`) at the commit it was read at |
| Step budget and seed | The train step's `steps` and `seed` parameters |

The run executes its recipe as one **pipeline run** and mirrors its state as the run's status: `queued` (a step waits
for a card), `running` (a worker holds a step), `paused` (its step job is paused), `done`, `failed`, `cancelled`.
Nothing in a run names a model family: the base model's family descriptor (published by its runtime's worker) says
which step kind trains (`train` role), calibrates (`calibrate`) and averages (`average`), and the recipe must contain
a step of the train kind.

## Place in the loop

Train: mix → calibrate → dry run (estimate) → run → metrics and checkpoints → average, resume or a new stage →
evaluate (phase 3).

## Fields and defaults

- **Estimate** (`runs.new?dryRun=true`): the lease overhead plus steps × seconds per step, the ± on the steps
  only. The overhead (model load, final validation and saves of one lease) is the calibration's
  `leaseOverheadSeconds`, else `estimates.lease_overhead_seconds` (120 s). The seconds per step come from the newest
  calibration of (base model, card class, memory cap, precision) — `basis: measured`, ± from the measurement's
  spread (`estimates.measured_plus_minus` when it reports none) — or else from the estimate table in
  `defaults.yaml` (`basis: table`). The answer carries the card, the data volume, the mix hash and today's GPU use
  against the project's budget.
- **Calibration** (`runs.calibrate`): runs the family's calibrate step on the mix's data under the card's memory
  cap; its `calibration` artifact (seconds per step, batch sizes, bucket configuration) is cached by the
  `calibration` output hook.
- **Checkpoints**: the `checkpoint` output hook registers each checkpoint of a run with its step, validation WER,
  family and weights hash, ranks them by validation WER and keeps the top k (`training.keep_top_k`, 3). Each new
  one goes out as `checkpoint.saved` on `run.{id}.checkpoints`.
- **Departures**: parameters that differ from defaults are listed per step on the run (`departures`); a stage's
  `parentDiff` lists where its train-step parameters, mix, pipeline and start differ from its parent's.
- **Parameters** (`params`): overrides of the train step. One its kind marks `shared` in the step schema (the data's
  language `target_lang`, `precision`, `min_duration`, the CUDA reserve) also reaches every other step of the stage
  that declares it, so the calibration measures what the training will run.
- **Language check**: `runs.new` and its dry run refuse (422) a stage whose language the base model does not know —
  its `locale:` tags list the languages it was trained with. The language is the train step's language parameter
  when set, else each locale of the mix. Pick a close language it knows, or import the data transliterated
  (`dataset_import`'s `transliterate`).
- **OOM**: an out-of-memory failure gets one automatic retry at 0.75× batch; the run's timeline shows the attempts,
  `oomRetries` and the batch scale.
- **GPU budgets**: spend is lease time on GPU cards — per project per day (the project's budget, from
  `budgets.gpu_hours_per_project_per_day`, in the instance timezone) and per agent session
  (`budgets.agent_gpu_hours_per_session`); what is left also subtracts the work already queued or running. An agent's
  `runs.new`, `runs.calibrate`, `runs.resume`, `runs.stage`, `checkpoints.average`, `pipelines.run`,
  `pipelineRuns.retry` and `jobs.resume` whose estimate exceeds what is left of either — or whose GPU cost is
  unknown — answers `202` with an approval id; a person
  approves it in the Approvals panel and the request runs as the agent. People are not budget-gated; their dry
  runs say `withinDailyBudget`.

## Commands

- `runs.new` (dry run first), `runs.list`, `runs.get`.
- `runs.calibrate` — measure seconds per step; estimates switch to `basis: measured`.
- `jobs.pause`, `jobs.resume`, `jobs.cancel` — on the run's `currentJobId` (the Run panel's Pause, Resume, Stop).
- `runs.resume` — continue a cancelled or failed run from its last training state (same optimiser state).
- `runs.stage` — a new run from a checkpoint (default the best kept one) with an explicit peak learning rate.
- `checkpoints.list`, `checkpoints.get`, `checkpoints.average` — the averaged checkpoint is a new checkpoint of the
  run (`kind: averaged`, `averagedFrom`).
- `metrics.get` — series by name on the step, epoch, wall-time or GPU-hours axis; long series are binned
  server-side (each point a bucket's mean with min and max); `afterStep` appends live points.

## Playbooks

- "Fine-tune from a dataset version": make a mix, `runs.calibrate`, `runs.new?dryRun=true` (read the estimate
  and the budget), `runs.new`, watch `runs.get` and `metrics.get`, then `checkpoints.list`.
- Agents: always dry-run a spending command first; a `202` means wait for `approval.decided`, not retry.

## Sources

- docs/spec/04-blocks.md Block 2; docs/spec/08-resolutions.md R12 (estimates), R13 (mix hash), R19 (windows),
  R41 (families), R44 (init, gpus), R53 (chart data is contract data).
- Cadence recommendation: the top k and the measured ± defaults.
