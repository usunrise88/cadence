---
title: Run
summary: One training run — status and stage timeline, config diff against the parent run, departures, final metrics, the best checkpoint and GPU-hours; pause, resume, stop, resume from checkpoint, new stage with an explicit peak LR.
contexts: [panel:run, command:runs.stage, command:runs.resume, command:datasets.materialize]
---

## What this is

A document (centre of the Training workspace). A **run** is one optimisation stage: a base model (or a checkpoint of
a parent run) trained on one mix revision with one recipe (`pipelines/train-stage.yaml` by default). It mirrors one
pipeline run, so its status is that pipeline run's: queued (a step waits for a card), running (a worker holds a
step), paused (its step job is paused), done, failed or cancelled.

| Part | Meaning |
| --- | --- |
| Stage timeline | Each step of the pipeline run (calibrate, train, …) with its state, the family role it fills, attempts, automatic OOM retries and the batch scale they run at, wall time or estimate, and a link to its log |
| Final metrics | What the train step reported when it ended (`val_wer`, …); Metrics shows the curves live |
| Spend and checkpoints | GPU-hours the run's leases used against the start estimate (basis table or measured), the checkpoint count and the best kept checkpoint |
| Against the parent run | For a stage: the parent run and the train-step parameters that differ from it (this run's value highlighted) |
| Departures from defaults | Every parameter, per step, that differs from defaults.yaml, with its default |
| Needs materialize | A run that is not done whose train step would read a dataset version the cache evicted (its pipeline run's `needsMaterialize`), and the stage form's estimate when the new stage would (`runs.stage` dry run, warning `needs-materialize`): the version, the bytes to copy back and from which mounts, and **Materialize** (`datasets.materialize`). Training reads only what the cache holds, so resuming or starting is refused (`artifact-missing`) until it is back |

Details lists the run's fields (base model, family, mix revision and its hash, recipe commit, runtime digest, card);
Lineage links the mix, the parent run and the pipeline run. Live on `run.{id}.status` and `run.{id}.checkpoints`.

## Place in the loop

Training · Run → Review → Decide. Watch the run, then decide: evaluate its best checkpoint (phase 3), continue with
a new stage, or resume it after a failure.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Steps | `training.steps` (3000) | The stage's step budget |
| Precision | `training.precision` (bf16) | |
| Peak LR (new stage) | none — required | A continuation needs its own peak learning rate; usually lower than the first stage's |
| Checkpoint (new stage) | the run's best kept checkpoint | Where the new stage's weights start |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Pause / Resume | `jobs.pause`, `jobs.resume` on the run's current step job | A paused training step saves its training state and resumes from it |
| Stop | `jobs.cancel` | Two clicks; the run ends cancelled |
| Resume from checkpoint | `runs.resume` | A failed or cancelled run continues its train step from the newest training state (`409 no-training-state` when there is none) |
| New stage from checkpoint | `runs.stage` | Checkpoint, explicit peak LR, optional steps; **Estimate** (dry run) first, then **Start stage** opens the new run |
| Pipeline run / Logs | — | Opens the run's pipeline run, or a step's log |

An agent's run over the project's daily GPU budget answers an approval instead of starting; the document says so.

## Playbooks

- **Continue a good run.** In Checkpoints pick the best checkpoint, "New stage from here", enter a lower peak LR,
  Estimate, Start stage. The new run's "Against the parent run" shows exactly what changed.
- **A run failed with an out-of-memory error twice.** The timeline shows the OOM retry at 0.75× batch; lower the batch
  in the recipe, then Resume from checkpoint.
- **The estimate says "Needs materialize".** The cache evicted a dataset version of the mix. **Materialize** copies it
  back from its mount (the notice names the bytes); when it is back the estimate is asked again and Start works.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Run); docs/spec/04-blocks.md Block 2.
- docs/spec/08-resolutions.md R12 (estimates), R13 (mix revisions), R44 (init base or checkpoint, one card).
