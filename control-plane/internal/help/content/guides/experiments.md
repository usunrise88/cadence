---
title: Experiments and sweeps — many runs for one question
summary: An experiment pins a mix revision and a base model; sweeps.run turns a grid or random draw over recipe parameters into runs queued one after another under a GPU-hour cap, and experiments.get compares them.
contexts: [guide:experiments]
---

## What this is

Tuning a fine-tune takes more than two runs. An **experiment** (`exp_…`) groups the runs that answer one question —
"does a lower peak learning rate keep the replay locales?" — on one fixed mix revision and one base model version, so
the only differences between its runs are the parameters you vary. A **sweep** (`swp_…`) generates those runs:

- `mode: grid` — every combination of the listed values (the first parameter varies slowest); `runs` cuts it.
- `mode: random` — `runs` points drawn with `seed` from each parameter's `values`, or from `min`–`max` on a `linear`
  or `log` scale (`integer` rounds); the same seed draws the same points.
- Parameters are the train step's own (`peak_lr`, `warmup_steps`, `augmentation`, … — whatever its kind declares
  with an x-cadence range; the engine checks every value against it) and `replayShare`, the mix's replay share,
  rendered into the run's mix artifact while the mix revision stays the same.

How a sweep runs:

1. The dry run prepares every point exactly as `runs.new` would (recipe, parameter ranges, language, estimate) and
   answers the points, their estimates, the total against `gpuHourCap` (`withinCap`, `fits`) and today's budget.
2. The real call refuses a total over the cap (`sweep-over-cap`). The policy weighs the whole estimate like any
   spending command (`gpu-spend`): an agent over the project's or session's GPU budget gets an approval id, and that
   approval covers the sweep's runs.
3. The sweep pins the recipe at the commit the first point read and starts its first run. Each run is an ordinary
   run (`runs.get`, Metrics, Checkpoints) carrying `experimentId` and `sweepId`.
4. When a run ends, the next point starts — after a failed run too. Before each start the sweep checks the
   GPU-hours its runs used plus the next run's fresh estimate against the cap; past it the sweep stops (`stopped`)
   and the remaining points are `skipped`. A run already training is never stopped by the cap.
5. Cancelling the running run (`jobs.cancel` on its current job, Stop in the Run panel) cancels the sweep.
6. One sweep runs per project at a time: its runs share the project's training slot.

Events go out on `entity.experiment.{id}`: `experiment.created`, `sweep.started`, `sweep.progress` (a run started),
`sweep.ended` (done, stopped, cancelled or failed with the reason) and `experiment.run_changed`.

## Place in the loop

Train → compare → evaluate → register. The best run's checkpoint is published with `models.register` like any other:
evaluate it (`evals.new` with `subject.checkpointId`), gate it (`evals.gate`), then register. Until a gated eval of
that checkpoint passed, `best.registrable` is false and `best.reason` names the next command.

## Fields and defaults

| Key | Meaning |
| --- | --- |
| `sweeps.mode` | grid or random when the request names none |
| `sweeps.max_runs` | most runs one sweep may queue |
| `sweeps.random_runs` | points a random sweep draws by default |
| `sweeps.gpu_hour_cap` | the cap when the request names none |
| `sweeps.seed` | the seed of a random draw |

## Commands

- `experiments.new` — name, question, mix (pinned to its current revision unless `mixRevision`) and base model
  (pinned to a version). The dry run checks both.
- `runs.new` with `experiment: exp_…` — a single run of the experiment; it inherits the mix revision and base model
  (naming others is refused).
- `sweeps.run` (`POST /experiments/{id}/sweeps:run`, If-Match the experiment's revision) — dry run first.
- `experiments.get` — the comparison: one row per run with the compared parameters (swept ones first, then any
  train-step parameter a run departs from defaults with; departures marked), the best validation WER and its
  checkpoint, GPU-hours, and the latest eval of that checkpoint with its gate verdict; `best` and whether
  `models.register` would accept it now. `experiments.list`, `runs.list?experiment=exp_…`.

## Playbooks

- Agents: one question per experiment; dry-run the sweep and shrink it to what the cap allows before the real call;
  report the comparison (best validation WER, the parameters that moved it) and evaluate the best checkpoint before
  proposing registration.

## Sources

- docs/spec/04-blocks.md "Experiments and sweeps"; docs/spec/08-resolutions.md R12 (estimates), R22 (registration),
  R53 (the Experiment charts).
- J. Bergstra and Y. Bengio, "Random search for hyper-parameter optimization", JMLR 13 (2012) 281–305.
