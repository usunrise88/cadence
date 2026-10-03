---
title: Experiment
summary: The runs that answer one question on a fixed mix revision and base model — sweeps under a GPU-hour cap, the parameters × metrics comparison with departures from defaults, Compare N, the best run by validation WER, evaluate it and register it.
contexts: [panel:experiment, command:experiments.new, command:sweeps.run, command:models.register, command:evals.new]
---

## What this is

A document of the Training workspace. An **experiment** asks one question ("Does a lower peak LR help Hebrew?") and
pins what every run of it trains on: one mix revision and one base model version. Its runs come from **sweeps** —
a grid (every combination of the listed values) or a seeded random draw over recipe parameters — or from `runs.new`
with `experiment`. A sweep's runs queue one after another on the project's training slot and stop before a run would
pass the sweep's GPU-hour cap.

| Part | Meaning |
| --- | --- |
| Question, mix, base model | What the experiment answers and what every run trains on (the mix revision's replay share beside it) |
| Best run | The run with the lowest validation WER, its checkpoint and that checkpoint's latest eval |
| Sweeps | Each sweep's state (running, done, stopped at the cap, cancelled, failed), runs ended of its points, GPU-hours its runs used against its cap, why it stopped, the run training now |
| Comparison | One row per run (oldest first): status, one column per compared parameter — every swept parameter (marked *swept*) and every train-step parameter some run departs from defaults with — best validation WER, GPU-hours against the run's estimate, eval verdict. A highlighted cell departs from the default (the column header's tooltip names it); ◆ marks the best run |
| Parameter against validation WER | Scatter of a numeric parameter (choose it) against each run's best validation WER, one series per sweep |
| Parallel coordinates | Every swept parameter and the validation WER as axes, one line per run; the best run's line is wider and solid. Learning rates spanning a decade or more get a log axis; objects (augmentation profiles) a category axis |

Every chart has a Table view and Copy CSV. Live on `entity.experiment.{id}` (a run, a sweep or the experiment
changed). Details lists the ids; Activity lists the experiment's events.

## Place in the loop

Training · Prepare → Run → Review → Decide. Ask the question, sweep, compare, evaluate the best checkpoint against the
baseline, then register it when its gate passed.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Mode | `sweeps.mode` (grid) | grid or random |
| Parameters | — | Train-step parameters (`peak_lr`, `warmup_steps`, `augmentation`, … — any with an x-cadence range) or `replayShare` (the mix's replay share; the mix revision stays fixed). Values comma-separated, or JSON (`{"profile": "clean"}, {"profile": "telephony"}`); random: values or a min–max range on a linear or log scale, optionally whole numbers |
| Runs | grid: every combination; random: `sweeps.random_runs` (8) | At most `sweeps.max_runs` (16) |
| GPU-hour cap | `sweeps.gpu_hour_cap` (8) | The whole estimate must fit it; the sweep stops before a run would pass it |
| Seed | `sweeps.seed` (1) | The random draw's seed (the same request draws the same points) |
| Steps per run | `training.steps` | Step budget of every run |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| New experiment | `experiments.new` | From the empty Experiment panel: name, question, mix (at its current revision); the base model is the defaults' |
| Run sweep / New sweep | `sweeps.run` | **Estimate** (dry run) lists the points with their estimates and the total against the cap and today's budget; **Start sweep** sends exactly that request and is offered only when it fits the cap. An agent's sweep over the GPU budget waits for an approval |
| Compare N | — | Select runs, then Compare N: the table and both charts show only them; Show all returns |
| Evaluate best | `evals.new` | The Run eval form for the best checkpoint (axes as on Checkpoints); Plan first, then Start eval opens the Eval report; gate it with `evals.gate` |
| Register best | `models.register` | Enabled when the best checkpoint's latest gated eval passed (otherwise the button says why); Show registration (dry run) names the model collection, Confirm register publishes it (approval for agents) |
| Stop a sweep | `jobs.cancel` on its current run | Cancelling the run that trains now cancels the sweep |

## Playbooks

- **Tune the peak LR.** New sweep → grid, `peak_lr` = `0.0001, 0.0002, 0.0004`, cap 4 → Estimate → Start sweep. When
  the runs ended, read the scatter, Evaluate best, gate it, Register best.
- **A sweep stopped at the cap.** The sweep's line says how much its runs used and what the next run would have cost;
  start another sweep with fewer points or a higher cap.

## Sources

- docs/spec/04-blocks.md "Experiments and sweeps"; docs/spec/11-ui-panels.md "Panel catalogue" (Experiment).
- docs/spec/08-resolutions.md R22 (registration needs a passed gate), R53 (charts: scatter, parallel coordinates).
- J. Bergstra and Y. Bengio, "Random search for hyper-parameter optimization", JMLR 13 (2012).
