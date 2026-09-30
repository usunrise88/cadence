# internal/runs
Training runs (docs/spec/04-blocks.md Block 2; R12, R13, R19, R41, R44): runs, calibration, checkpoints, metric
series and GPU budgets. A run is a facade over one pipeline run of the pipelines engine; no code here names a model
family — the base model's family descriptor (registry `model_family`, published by its runtime's worker) names the
step kind of each role (`calibrate`, `train`, `average`).

- **Runs** (`run_…`, table `runs`, migration 0016): one optimisation stage — `init` `base` (a base model version) or
  `checkpoint` (a `ckp_…`; its run becomes `parentRunId` and its base model the run's), the family (name + registry
  version), the mix revision with its rendered artifact's hash (R13), the recipe (pipeline, source, ref, commit,
  version), the pipeline run (`pipelineRunId`), the step of the train role (`trainStep`), step budget, seed, gpus
  (1), precision, the train step's runtime (name, registry version, image digest), the card of the estimate, the
  estimate it started with, `status`, `error`, `resumedFrom`, `rev`.
- **Prepare / Create** (`runs.new`, `runs.stage`): resolve the start and family; `ResolveMix` + `RenderMix` put the
  `mix` artifact (format `cadence.mix/1`: `input_cfg` groups with weights, sampling probabilities from the mix
  preview and the dataset artifacts; every dataset trainable now, `data.Trainable`, and with a content-store
  artifact) in the CAS; `RenderBaseModel` puts the `base_model` artifact (`cadence.base_model/1`: HF repository,
  revision, checkpoint file, family). The recipe (`training.pipeline`, default `train-stage`) is read from the
  project repository and must contain exactly one step of the family's train kind (`recipe-mismatch`). Pipeline
  inputs are filled by declared type (`inputsFor`): `mix`, `dataset` (the mix's only dataset), `base_model` (the base
  model, or the start checkpoint — `pipelines.Accepts`), `checkpoint`. Request fields become train-step overrides
  when the kind has the parameter: `steps`, `seed`, `precision`, and for a stage the peak learning rate
  (`peak_lr` › `learning_rate` › `lr`). `Engine.Prepare` validates; the estimate uses the resolved `steps`; `Create`
  starts the pipeline run with the run id, inserts the row, registers outputs of steps reused inside
  `Engine.Start` (`registerExisting`) and emits `run.created`.
- **Status** (`Observe`, installed as the engine's `RunObserver`, and `JobChanged` from jobs.pause|resume): derived
  from the pipeline run — done | failed | cancelled when it ended; while it runs, `paused` when an active step's job
  is paused, `running` when a worker holds a step (step job leased), else `queued`. A change bumps `rev` and emits
  `run.status_changed` on `run.{id}.status` (payload `{run: {id, status, error, rev, pipelineRunId, …}}`).
- **Resume** (`PlanResume`, `Resume`): a failed or cancelled run continues its train step from the newest
  `training-state` still in the CAS — from released leases of its step jobs (a stop saves one) or the train step's
  own outputs — as a new attempt of the same pipeline run (`Engine.Retry` with `ResumeFrom`, reason `resume`).
  None: `no-training-state` (409). The estimate covers the steps left (budget − the state's `step`).
- **Stage** (`StageNew`): a new run with init checkpoint from a checkpoint of the parent (default its best kept
  one), the parent's mix revision and pipeline unless named, and an explicit `peakLr`.
- **Calibration** (`PlanCalibration`, `StartCalibration`, `calibrationHook`, table `calibrations`): the family's
  calibrate kind (newest published version) as a one-step inline pipeline `calibrate` over the mix and base model;
  `calibration_requests` records the key (base model collection, card class, memory cap, precision) for the hook.
  The hook caches `secondsPerStep` (meta, else the metric `seconds_per_step`), ± (meta `plusMinus` | `spread` |
  2 × `secondsPerStepStd` / mean, else `estimates.measured_plus_minus`), batch sizes and the bucket configuration;
  a run's own calibrate step is keyed by the run. `EstimateRun` answers `basis: measured` from the newest
  calibration of the key, else the defaults table (`basis: table`), else `estimate-unavailable`.
- **Checkpoints** (`ckp_…`, table `checkpoints`, `checkpointHook`): a `checkpoint` output of a run's pipeline run is
  registered once per (run, artifact) with `step`, `valWer` (meta, else the metric `val_wer`), `family`,
  `weightsHash`; `kind` `averaged` (with `averagedFrom`) when it comes from a `checkpoints.average` pipeline run.
  Ranked by validation WER; the top `training.keep_top_k` are `kept`. `checkpoint.saved` on `run.{id}.checkpoints`.
- **Average** (`PlanAverage`, `StartAverage`): the family's average kind over chosen checkpoints of one run, wired
  as `<input>.0`, `<input>.1`, … (the step contract's several artifacts per input) in an inline pipeline with the
  run id, so the output registers as a checkpoint of the run.
- **Metrics** (`Metrics`, `metrics.get`): `telemetry.Get` series by run, x = step | epoch | wall (seconds since the
  run's first point) | gpuHours (lease time on GPU cards before the point, wall hours without GPU leases); a series
  longer than `maxPoints` is cut into equal x buckets answering mean value, min, max, count and the last step
  (R53); `afterStep` for live append; checkpoint marks.
- **Spend** (`spend.go`): GPU-hours = lease time of leases on a card (`card_index` set), clipped to a window.
  `ProjectBudgetOf` (the project's `budgets.gpuHoursPerDay`, today in the policies timezone), `SessionBudgetOf`
  (the session's `budget.gpuHours`, else `budgets.agent_gpu_hours_per_session`), `RunGPUHours`. `Meter` is the policy
  engine's budget (`policy.Budget` + `policy.SessionBudget`); `Spend` is the digest's `notify.SpendFunc`.

Response shapes for the Run, Metrics and Checkpoints panels: `View` (contract `Run`: status, `timeline` of
`StageEntry` — step, kind, role, state, attempts, batch scale, OOM retries, job, paused — `currentJobId` for
Pause/Resume/Stop via jobs.*, `finalMetrics`, `departures` per step, `parentDiff`, `bestCheckpointId`,
`checkpointCount`, `gpuHours`, the start estimate), `Checkpoint`, `SeriesSet`.

Tests: `pipelines/pipelinestest` has a fixture family (`fixture-family`: `fx_calibrate`, `fx_train`, `fx_average`)
and base model; `internal/server/runs_integration_test.go` (fake worker) and `runs_worker_integration_test.go` (the
real worker protocol).
