# internal/experiments
Experiments and sweeps (docs/spec/04-blocks.md "Experiments and sweeps"; phase-3 plan stream X; help
`guides/experiments`). Like `internal/runs` it never names a model family; every run goes through the runs service's
`Prepare` and `Create` (the `runs.new` path).

- **Experiments** (`exp_…`, table `experiments`, migration 0027): name (unique per project), question, tag, a pinned
  mix revision and base model version. `Prepare` checks the mix revision, the base model and its family; `Create`
  writes it with `experiment.created`. `ForRun` turns a `runs.new` request with `experiment` into a run of it (init
  base, the experiment's mix revision and base model; others are refused) — the run row carries `experiment_id`.
- **Sweeps** (`swp_…`, table `sweeps`): `Points` makes the points — grid (every combination, first parameter
  slowest, cut to `runs`) or random (`runs` draws, PCG seeded by `seed`, from values or a min–max range on a linear
  or log scale, four significant digits); at most `sweeps.max_runs`. Parameters are train-step parameters (the
  engine checks names and ranges) and `replayShare`, which `runs.NewInput.ReplayShare` renders into the run's mix
  artifact (the mix revision stays; checked against `mix.replay_share` and the mix's replay groups).
  - `Plan` prepares every point as a run and sums the estimates: `withinCap`, `fits` (points that fit in order), the
    basis, and today's remaining project budget. The server weighs the total with the policy (`gpu-spend`).
  - `Start` refuses a total over the cap (`sweep-over-cap`) and a second running sweep in the project (one per
    project: the runs share its slot; a partial unique index backs it), pins the recipe at the first point's commit,
    raises the experiment's revision and starts the first run.
  - `advance` starts the next point when the current run ended (a failed one included) — in a savepoint, after a
    fresh `Prepare`, only while the GPU-hours the sweep's runs used (`runs.RunGPUHours`) plus that estimate fit the
    cap; else the sweep is `stopped`. A run that ends at once is followed by the next point in the same call. Inside
    `sweeps.run` a point that cannot start fails the command; later it fails the sweep (`failed`), never the
    transaction that ended the previous run.
  - `RunChanged` is the runs service's `OnStatus` hook (installed by `Install`): every created run or status change
    of an experiment's run emits `experiment.run_changed`; the end of a sweep's current run advances the sweep, a
    cancelled one cancels it.
- **Comparison** (`Get`): runs oldest first; the compared parameters are the swept ones (oldest sweep first) and
  every train-step parameter a run departs from defaults with; a run's value is its train step's resolved parameter
  (the point's value for `replayShare`, else the mix revision's); departures come from the pipeline run's
  departures. Per run the best checkpoint by validation WER, GPU-hours, the start estimate and the latest eval of
  that checkpoint. `best` is the lowest validation WER (earliest on a tie) with `registrable` when the checkpoint's
  latest gated eval passed — `models.register` checks the same — else the next command in `reason`. `List` names
  the best run with one query instead of the comparison.

Events on `entity.experiment.{id}`: `experiment.created`, `experiment.run_changed`, `sweep.started`,
`sweep.progress` (a run started), `sweep.ended`.

Tests: `experiments_test.go` (points, parameter checks, tags); `internal/server/experiments_integration_test.go` (the
loop on the worker protocol: cap refusal, a stop at the cap after metered lease time, the next run on a run's end,
cancel; the comparison; a random sweep on the in-process fake worker).
