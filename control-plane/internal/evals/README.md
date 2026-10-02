# internal/evals
Evaluation (docs/spec/04-blocks.md Block 3; R20–R24, R43, R54; docs/review/2026-10-02-phase-3-plan.md stream E):
evals over a generated pipeline, the global eval-record cache, the paired blockwise bootstrap, the project's gate
(`gates.yaml`) and model registration. Like `internal/runs` it is a facade over the pipeline engine and never names a
model family: the step kinds come from the family descriptor's roles (`materialize`, `transcribe`) and the neutral
scorer `wer_score` (newest published version of each).

- **Plan** (`Prepare`, `evals.new` and its dry run): the subject (exactly one of a project checkpoint, a model version
  or a base model version) and the baseline (the request's, else the project's `@baseline`, else its default base
  model; `eval-baseline-missing`) become `Model`s with a model key — the weights hash, or `base:<versionId>` for a base
  model, whose weights the family's `materialize` step produces. Golden sets: the request's (`ver_…`, `@alias`,
  collection, `*` pattern over adopted sets), else those `gates.yaml` names (target and replay, resolved to the
  project's adopted versions), else every adopted golden set. Profiles: the request's, else `eval.matrix_profiles`
  that every compared family declares (else all they share), plus the primary profile (`gates.yaml`
  `primaryProfile` › `eval.primary_profile`, matched by name, else by latency). Replay golden sets (named in the
  gate's replay list, or — without lists — of a language outside the project's locales) are scored at the primary
  profile only. Decoding: `[{boost: none}]` or boost lists `lang/<locale>/boost/<file>.txt@<commit>` read from the
  project repository (one term per line, `# weight: <float>` header; the request's weight wins, default 1) and
  rendered as `boost_list` artifacts `{terms, weight}`; a transcribe kind that consumes a `boost_list` gets an empty
  list for boost none.
- **Cells and records**: one cell per role (subject, baseline) × golden set × profile × decoding; its record key is
  model key × golden set version × normalizer version × decoding hash × scorer `kind@version`, the decoding hash being
  sha256 of `{transcribe: kind@version, profile, <locale param>: locale, boost?: {list, weight}}`. A key already in
  `eval_records` (any project) links the cell (`cached`); missing keys become units, one per key (a subject equal to
  its baseline computes once).
- **Pipeline**: inputs `base_m<n>` / `model_m<n>`, `data_g<n>` (the golden set's dataset artifact), `norm_g<n>` (the
  normalizer version rendered as its payload JSON, meta `{versionId}`), `boost_d<n>`; steps `materialize-m<n>` (base
  models), `transcribe-u<n>` (params `profile` and the locale parameter, estimate = golden hours ×
  `eval.gpu_hours_per_audio_hour` × 3600 s) and `score-u<n>`, wired by artifact type. The estimate the policy weighs is
  the audio hours of the units × `eval.gpu_hours_per_audio_hour` (basis `table`).
- **Run** (`Create`): the eval and its cells are written first, then `Engine.Start` with the eval id as the pipeline
  run's `RunID`, so the `scores` hook and the observer find the eval even for steps reused inside Start. An eval
  without missing cells is done at once.
- **Scores hook**: a `scores` output of an eval's score step reads `summary.json` (`cadence.scores/1`, else the step
  fails), inserts the record (`ON CONFLICT DO NOTHING`: a record computed meanwhile is linked) and links the waiting
  cells; `eval.progress` on `eval.{id}.progress`.
- **Observer** (`SetNamedObserver("evals", …)`): the pipeline run's state becomes the eval's status (queued until a
  step runs, running, done, failed with the step's error; a cancelled run fails it; a retry reopens it). Done
  computes every subject cell's delta against the baseline's cell of the same golden set, profile and decoding:
  `Pair` + `Compare` over the two `utterances.jsonl` (blockwise over each row's `group`), stored on the cell.
- **Bootstrap** (`bootstrap.go`): paired, blockwise, `samples` resamples of the groups with replacement (PCG seeded
  by `seed`), pooled-rate deltas for WER, deletion and insertion rates, percentile intervals (type 7) at `level`;
  tested against the exact bootstrap distribution of a small set.
- **Gate** (`gates.go`, `verdict.go`): `gates.yaml` at main's head, strict YAML, ranges from `defaults.yaml`
  (`gate-config-invalid`), named collections must be adopted (`CheckAdopted`). `Gate` checks at the primary profile
  and decoding 0: target (passed when the interval lies below zero, failed above, inconclusive across), replay
  (failed when the delta exceeds `maxRegression` and the interval excludes zero), deletions/insertions; the verdict is
  passed only when every check passed. Stored on the eval with the file's commit (`gatesSha`, empty for the
  defaults); `eval.gated` on `entity.eval.{id}`. A significance other than the eval's recomputes the deltas.
- **Gate integrity** (audit 2026-10-02, `standing`): a baseline that is neither the project's `@baseline` nor its
  default base model adds a failed `baseline` check; every golden set `gates.yaml` names, resolved to the project's
  adopted version, that the eval did not score adds a failed target or replay check.
- **Models** (`models.go`): `PlanRegister` takes the checkpoint's latest gated eval (an explicit `evalId` only while
  that latest verdict passed too), refuses a failed or missing verdict (`gate-not-passed`), and builds `ModelPayload` with lineage (run, mix hash, recipe commit, the mix's
  dataset versions) and the Markdown card (gate checks, eval cells, composition, lineage, departures); `Register`
  registers it frozen in `model/<name>` and adopts it into the project (`model.registered` on `entity.model.{id}`).

Tables (migration 0024): `eval_records` (global), `evals`, `eval_cells`. Tests: `evals_test.go` (bootstrap, pairing,
gate parsing and verdicts, boost lists); `internal/server/evals_integration_test.go` runs the loop on the fixture
family (`pipelinestest.RegisterEvaluation`, `RegisterGoldenSet`).

Phase 3 stream R (robustness and streaming scorers; `axes.go`, `metrics.go`, `lineage.go`; migration 0028):

- **Robustness axis**: `evals.new` `augmentations: [{profile: none | augment/<name>.yaml@<commit>, seed?}]`; none is
  always index 0. A profile is read from the project repository at its commit, every value it leaves out taken from
  `defaults.yaml` `augment.*` and checked against the ranges there (unknown transforms or fields refused); a `noise`
  transform names a noise bank (`noise-bank/<name>` or `ver_…`). It renders as an `augment_profile` artifact
  `{format, name, seed, hash, transforms}`, `hash` = sha256 of `{seed, transforms}`. Target golden sets get every
  augmentation, replay sets none only. An augmented cell's decoding hash adds `augment: {kind: augment_dataset@<v>,
  profile: <hash>, seed}`, so the record key changes only for augmented cells (records from before stay valid as
  none); `eval_records.augmentation` describes it. The pipeline gains `augment-g<n>a<k>` (the core kind
  `augment_dataset`) whose `dataset` output (meta `purpose: augmented`, skipped by the dataset hook) feeds the cell's
  transcribe and score steps. `evals.get` answers `augmentations`, each cell's `augmentationIndex` and `robustness`
  (each augmented cell's WER against the same cell without augmentation). Deltas pair cells of the same augmentation;
  the gate reads augmentation 0 only.
- **Metrics beside WER** (reported, not gated): per record key, `entities` (core `entity_score`, with the `itn`
  artifact rendered from the pack serving the golden set's locale: `lang/<pack>/itn.yaml` at main, at the commit that
  last changed it) and `latency` (core `latency_score`, for streaming families, with a `vad` artifact from the newest
  published kind that turns one dataset into a `vad`, `vad-g<n>a<k>`). Both write `metric_scores`
  (`cadence.metric-scores/1`); the `metric_scores` hook stores the summary in `eval_metrics`, keyed by model key ×
  golden set × decoding hash × scorer × config (the ITN artifact hash; the VAD kind and version id) and shared like
  the records. A cell's plan (`eval_cells.metrics`) names the scorer, config and step, or why the metric is
  unavailable; cached records get metric steps from their stored hypotheses. `evals.get` answers `metrics`.
- **Lineage** (`LineageSource`, wired in `cmd/cadence`): an eval is built from its subject, baseline, golden sets,
  noise banks and the records of its cells; a record from its golden set, normalizer and model (checkpoints and model
  versions with its weights hash, or the base model version). Test: `evals_robustness_integration_test.go`.
