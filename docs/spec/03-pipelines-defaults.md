# Cadence spec — Pipelines, defaults, language packs, augmentation, interoperability

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Pipelines and extension points

Every block's process is a pipeline of typed steps declared in the recipes repository and executed by a step registry in the worker; adding or changing a step touches one Python module and its schema, and the API, the MCP tool and a generic UI derive from that schema.

### Model

- Pipeline: a YAML file `pipelines/<name>.yaml` in the project repository, parsed strictly (unknown keys fail):
  `name` (equals the file name), `description`, `inputs` (name → artifact type) and `steps`, each with an `id`, a step
  kind pinned as `kind: name@version`, `in` (every consumed input wired from `$inputs.<name>` or `<step>.<output>`;
  outputs come from the kind's `produces`) and `params` holding only departures from defaults. Execution order is
  topological (file order breaks ties); a cycle is refused. A pipeline's version is the commit that last changed its
  file (`template-<sha256 prefix>` for a bundled template). A project without its own file of a name runs the bundled
  template (`control-plane/templates/pipelines`): `echo` (worker check), `import` and `replay-base` (02 "The dataset
  artifact"), `train-stage`, `eval-matrix` (rewritten in phase 3 to the eval pipeline's shape, "The eval pipeline"
  below; `evals.new` generates its own pipeline per eval) and `data-ingest`, which names step kinds of phase 4 and
  fails validation until those are published.
- Step kind: a Python entry point in the worker (`cadence.steps` group, the same idea as SDP processors) declaring a
  JSON Schema for parameters (`x-cadence` on each, `defaultRef` into `defaults.yaml`), the artifact types it consumes
  and produces, resources (`gpu`, `gpus`, `memoryGb`, `diskGb`, `jobKind`), the family role it fills or `neutral`, the
  secret names it needs, a help slug and a `run()`; the worker publishes the registry to the control plane at start
  (06 "Worker protocol"), and the pipeline engine reads registry versions of kind `step_kind`, newest non-deprecated
  first.
- Runtime: every step-kind version names the runtime (a pinned worker image) it runs in; a worker leases only its
  runtime's step kinds (R40). Runtime-neutral core kinds (`echo`, `dataset_import`) ship in every runtime image, and
  the scheduler may lease one to any runtime that publishes the same kind, version and schema hash; framework step
  kinds exist in one runtime only.
- Validation at `dryRun` (`pipelines.run?dryRun=true`, `Engine.Prepare`): every `kind@version` is published — by a
  registered worker now, not only once: when the kind's runtime has registered workers and none of their latest
  registrations lists the pinned version (a newer image dropped it), the step would wait in the queue for ever, so
  planning refuses it and names the versions the workers publish (re-pin the file, or `projects.sync` for the bundled
  pipelines; a runtime with no registered worker is not judged) — wiring
  types equal the kinds' `consumes`/`produces`, the run's inputs match the declared types, parameters resolve
  (`x-cadence.defaultRef` into `defaults.yaml` › `x-cadence.default` › schema default) and fit the kind's JSON Schema
  and mapping `x-cadence.range`, and unknown parameters or required ones without a value fail. Every problem is listed
  in one `pipeline-invalid` (422) with field paths. A valid dry run answers `200` with the plan: resolved parameters,
  departures `{param, value, default}`, and the sum of known step estimates (seconds; GPU-hours over steps with
  `resources.gpu`) with the steps that have none. The estimate feeds the policy's budget check, so spending over the
  budget answers `202` with an approval.
- Pipeline run (`plr_`, migration 0013): `pipelines.run` takes the pipeline's version as `If-Match` (`*` accepts any)
  and answers `201` with the run and its steps (`pls_`); the run records the commit the file was read at and keeps the
  parsed definition, so retries never re-read the repository. Step states `waiting → queued → running → done |
  reused | failed | skipped | cancelled`; run states `running → done | failed | cancelled`; events on
  `pipeline_run.{id}` (`pipeline_run.started`, `.step_changed` with `{pipelineRunId, runState, step}`,
  `.state_changed`) carry the project. Each ready step is a River job of kind `step` (its own River queue, `steps`)
  whose args are the step spec; it turns `running` when a worker is granted its lease. Its handler waits for the lease
  outcome, then records the outputs in the artifact index (they must match the kind's `produces` and be in the
  store), runs the output hooks by artifact type in a savepoint (`dataset` now; `checkpoint` and `calibration` with
  runs; `scores` with evals, phase 3), and advances the dependents, all in one transaction; a refusing hook fails the step and keeps nothing of it.
  Hooks also run for reused outputs and are idempotent per artifact hash.
- Reuse and retries: a ready step whose input hash (kind, version, runtime version, resolved parameters, input hashes) equals a
  finished step's in the same project is `reused` (outputs copied) unless the run asks for `fresh`. An `oom` error
  gets one automatic attempt at 0.75× batch (`overrides.batchScale`), `lost` one retry, anything else fails the step
  and the run and skips the steps that never started. `pipelineRuns.retry` runs a failed or cancelled step (or every
  failed step) again as a new attempt and reopens the run; `pipelineRuns.cancel` cancels waiting steps and running
  step jobs; `pipelineRuns.wait` serves agents. A sweep every minute treats steps whose job ended without an outcome
  (the control plane stopped while waiting) as `lost`.
- Inputs: a pipeline input is an artifact reference `{hash, type}` that must already be in the content store,
  resolved by the facade that starts the run — a mix revision renders to a `mix` artifact with its resolved
  `input_cfg`, a base model version to a `base_model` artifact. Facades (runs, evals, playbooks) start pipeline runs
  through the Go API, `pipelines.Engine.Start` with a pipeline name or a parsed pipeline, inputs, parameter overrides,
  per-step estimates, the run id, priority and `fresh`. A run whose training step reads an eval-only dataset version
  (a `dataset` input, or a `mix` input referencing one) is refused with `eval-only-dataset` (R18).
- For facades (phase 2, stream R): a step input may take several artifacts as `<input>.<n>` (averaging wires
  `checkpoints.0`, `checkpoints.1`, … as the step contract has it); an input declared `base_model` also accepts a
  `checkpoint` (R44: one train-stage recipe for `init: base` and `init: checkpoint`; the step reads its input's type);
  a facade is told about every change of a pipeline run with a run id inside the changing transaction (the run
  observer); `pipelineRuns` retries can continue a step from a `training-state` artifact (`overrides.resumeFrom`,
  attempt reason `resume`, kept by the automatic OOM and lost retries).

```yaml
# pipelines/train-stage.yaml in the project repository
name: train-stage
inputs: { mix: mix, base: base_model }          # pipeline inputs by artifact type
steps:
  - id: calibrate
    kind: oomptimizer_calibrate@1
    in: { base: $inputs.base, data: $inputs.mix }
  - id: train
    kind: nemotron_finetune@2
    in: { base: $inputs.base, data: $inputs.mix, calibration: calibrate.calibration }
    params: { steps: 500 }                        # only departures from defaults are written
```

### Artifact types

Framework code stops at the role steps of a model family; everything after them reads neutral, self-describing types (R42). An artifact is a blob or a directory manifest in the content store (06).

| Type | Holds |
| --- | --- |
| `dataset` | A directory artifact: `dataset.json` (header `cadence.dataset/1` with source, split rule, counts, hours), `manifest.jsonl` (one line per utterance: `audio` as a path inside the artifact, duration, sample rate, language, speaker, text, origin, split) and the audio files (02 "The dataset artifact"); the `dataset` hook registers a dataset version from it (R18) |
| `shar` | Lhotse Shar shards: audio and text only, never features (mel bins, frame rate and normalisation belong to a family) |
| `mix` | A rendered mix revision: the resolved `input_cfg` and its content hash |
| `base_model` | An upstream checkpoint at its pinned revision, with its family |
| `calibration` | Measured bucket batch sizes and seconds per step for (base model, card class, cap, precision, bucket config); the `calibration` hook caches them for estimates |
| `checkpoint` | The family's payload, what loading it needs (config, train arguments, tokenizer reference) and neutral metadata `{step, valWer, family, weightsHash}` |
| `training-state` | Optimiser and sampler state, used only to resume |
| `hypotheses` | JSON lines per utterance: text; words with start, end and confidence; the decoding config and its hash; family and weights hash; partial events (audio offset, emit time, text) for streaming decodes |
| `analysis` | float16 arrays with frame rate and axis labels: model input features and per-frame emissions |
| `normalizer` | A scoring normalizer version rendered by the control plane: the `NormalizerPayload` as JSON (`application/json`, `meta: {versionId}`), as a `base_model` artifact is rendered from its version (R21; 02 "Evaluation entities") |
| `boost_list` | One boost list of a language pack (`lang/<locale>/boost/<domain>.txt`) with its weight, an optional input of a transcribe step; the decoding hash includes its hash and the weight (R24). Rendered by the control plane as JSON `{terms, weight}` (`format: cadence.boost_list/1` optional, other keys ignored); the NeMo pack also reads the pack file itself and caps a list at 10 000 terms of ≤ 100 characters, weight 0–100 |
| `scores` | A directory artifact written by a scorer: `summary.json` and `utterances.jsonl` ("Scorers and metrics" below); the `scores` hook writes the eval record. R42's "eval report" is the `evals.get` view over an eval's cells, not an artifact |
| `metric_scores` | A directory artifact written by a metric scorer beside WER (`entity_score`, `latency_score`): `summary.json` (`cadence.metric-scores/1`) and `utterances.jsonl`; the `metric_scores` hook keeps the summary beside the cell's eval record (stream R) |
| `augment_profile` | An augmentation profile (`augment/<name>.yaml` at a commit) resolved with the `augment.*` defaults, with its seed and content hash, rendered by the control plane for `augment_dataset` (stream R) |
| `itn` | A language pack's `itn.yaml` (classes with patterns and examples) rendered as JSON at the commit that last changed it, read by `entity_score` (stream R) |
| `vad` | Speech segments per utterance of a dataset (JSON lines: `speech`, `speechEndS`) after a header naming the VAD model; written by a VAD step (`frame_vad`), read by `latency_score` (stream R) |
| `deployable` | An export (format, files, serving metadata); phase 5 |

Also: `text` (the `echo` check), manifest, waveform peaks, correction batch. A directory artifact carries `meta.layout: dir` (a file `layout: file`), which the worker adds to every output it releases. Scorers, gates, Diff, Audio, Shadow, triage and the Transcription panel read only these types.

### Runtimes, model families and latency profiles

- A runtime is a registry version: a container image pinned by digest, its environment lock (CUDA, PyTorch, the framework, Lhotse) and the worker plugin version. A worker process lives in one runtime and advertises it with its cards; card slots belong to the control plane per host and card. A framework gets its own image even when its wheels would fit another's. v1 ships one, NeMo Speech 26.07 (`nvcr.io/nvidia/nemo-speech:26.07`, runtime `nemo-speech`, `worker/Dockerfile`), plus the CPU runtime `toy` for CI and trying the seams (`worker/Dockerfile.toy`); the descriptors are baked into the images (`worker/runtime/*.json`), and compose runs one worker service per runtime (`worker`, profile `gpu`; `worker-toy`, profile `toy`). Adding a runtime is `runtimes.new` with approval, deferred with the packs beyond NeMo (R40).
- A model family is a versioned descriptor the runtime publishes beside its step kinds (R41): framework and architecture; checkpoint and export formats and what loading needs; input (sample rate, channels) and features; tokenizer kind; capabilities (streaming, word timestamps, confidence, boosting method, language prompting, train modes `finetune | adapter | scratch`); latency profiles; the step kind for each role (calibrate, train, average, transcribe, materialize, export, parity reference; `materialize` turns a `base_model` artifact into a `checkpoint`, so an eval always transcribes a checkpoint, decision 6 of the phase-3 plan); its `defaults.yaml` section; help and skill slugs. Base models, checkpoints and model versions carry a family reference; the UI and MCP render family options from the descriptor's schemas.
- No control-plane or web code branches on a family or runtime name; the Nemotron family is named only in the worker's NeMo pack, `defaults.yaml` data, templates and docs, and a test greps for it.
- A latency profile has a name, the algorithmic latency, chunk and left context in milliseconds, the family parameters that realise it and a label. A family without streaming has one profile, `offline`. Eval matrices, the primary cell and eval records name profiles; families line up by milliseconds, not by parameter spelling (R43).
- Spike A5 (`docs/spikes/A5-live-transcription.md` "Result", surprise 2) found that Nemotron 3.5 was trained at
  `[56,0]`, `[56,3]`, `[56,6]` and `[56,13]` (80, 320, 560, 1120 ms) only: `160ms` (`[56,1]`) is an interpolated
  look-ahead NeMo warns about and runs anyway. Its WER sits between its neighbours (A3 saw the same). Whether the
  primary cell stays at `160ms` or moves to `320ms`, and a `trained` flag on latency profiles, are open for the owner
  (07 "Open questions").

The first family, Nemotron 3.5 streaming (cache-aware FastConformer RNNT, NeMo):

| Profile | `att_context_size` | Label | Role |
| --- | --- | --- | --- |
| `80ms` | `[56,0]` | 80 ms · [56,0] | Eval axis |
| `160ms` | `[56,1]` | 160 ms · [56,1] | Primary cell |
| `320ms` | `[56,3]` | 320 ms · [56,3] | — |
| `560ms` | `[56,6]` | 560 ms · [56,6] | — |
| `1120ms` | `[56,13]` | 1120 ms · [56,13] | Eval axis |

Latency is 80 × (r + 1) ms for `[56,r]`, with a left context of 56 frames (4.48 s). Its step kinds: `oomptimizer_calibrate` (calibrate), `nemotron_finetune` (train), `checkpoint_average` (average), `nemotron_transcribe` (transcribe: file decode in streaming simulation at a profile → `hypotheses`; `@2` in phase 3 adds static RNNT phrase boosting from an optional `boost_list` input, R24; `@3` decodes through NeMo's streaming pipeline, the decoder live sessions use), `checkpoint_from_base` (materialize, phase 3); export and parity join in phase 5. No `checkpoint_register` kind: the control plane's checkpoint hook registers every `checkpoint` output of a run (07, stream N).

NeMo pack as built (phase 2, `worker/packs/nemo`, distribution `cadence-nemo`, help `guides.nemo-pack`):

- The family is published as `nemo.fastconformer-rnnt.cache-aware` (the seeded base model's `familyId`); defaults under `packs.nemo`. The image installs the pack and makes NeMo's editable install readable by the worker's non-root user.
- `oomptimizer_calibrate` (consumes `base`: base_model, `data`: mix): OOMptimizer re-implemented in the pack with the prompt model's fixes (five-tensor batch with the language's prompt index, no endless loop when batch 1 does not fit, cuFFT failures count as out of memory), buckets from the longest down and merged, then timed optimiser steps on the mix; the `calibration` artifact carries `secondsPerStep`, `secondsPerStepStd`, `plusMinus`, `batchSize`, `batchSizes` and `bucketConfig`.
- `nemotron_finetune` (consumes `base`, `data`, `calibration`; produces `checkpoint`, `checkpoint_best`, `state`): NeMo + Lightning, bf16-mixed, AdamW + NoamAnnealing from `peak_lr` (the scale is derived and posted), the mix read from the content store in place as Lhotse `input_cfg` groups, the language prompt per clip (`unified` mode) with the locale tag appended, the augmentation profile on the fly, metrics through the step context, a training state every `state_every_minutes` and at a stop, resume from `overrides.resumeFrom`.
- `checkpoint_average` averages the `.nemo` weights on the CPU; `nemotron_transcribe` streams at a profile (`@3`: NeMo's cache-aware streaming pipeline, the decoder live sessions use) and writes words timed by the emissions, token-derived word confidence and partial events.
- Every GPU step caps PyTorch's allocator at the lease's cap minus `cuda_context_reserve_mb`, so the whole process stays under the cap nvidia-smi sees.
- Phase 3 (stream Y): `checkpoint_from_base@1` (materialize) and `nemotron_transcribe@2`, whose optional `boost`
  input applies NeMo's GPU phrase-boosting tree on the greedy label-looping decoder (`context_score` 1.0,
  `depth_scaling` 2.0, its state carried across streaming chunks): score = acoustic + weight × tree score, the list's
  weight or else `packs.nemo.boost_weight` (0.5). Unwired, the decode and its decoding hash equal version 1's.
  Measured on FLEURS he_il test (120 utterances, base model at `160ms`, 40 missed words listed): entity recall 0.11 →
  0.29 / 0.36 / 0.50 / 0.52 / 0.54 and WER 0.466 → 0.462 / 0.460 / 0.461 / 0.490 / 0.623 at weight 0.3 / 0.5 / 0.7 / 1
  / 2; above ≈ 0.7 listed words replace others (help `steps.nemotron-transcribe`).
- One decoder for live and evals (spike A5, "What the spec should change" 2), as built: `nemotron_transcribe@3`
  decodes through NeMo's streaming pipeline API (`nemo.collections.asr.inference`), the decoder `nemotron_live` runs,
  with the pack's shims (the per-stream language prompt, stripping the trailing locale tag, whole-window features, a
  feature buffer of cache plus chunk). Its decoding config names the decoder (`"decoder": "nemo-pipeline-cache-aware"`
  with `stopHistoryEouMs`), so its records never mix with `@1`/`@2` records (NeMo's cache-aware loop). Utterances of a
  batch step together (`packs.nemo.transcribe_batch_size`, 8 for evals; batch 1 gives exactly a live session's words);
  on the stand it matches version 2's WER at every profile (help `steps.nemotron-transcribe`).

The toy pack's family `toy-ctc` (runtime `toy`) has the profiles `offline` and `320ms` and the kinds `toy_calibrate`, `toy_train`, `toy_average`, `toy_transcribe` and, in phase 3, `toy_checkpoint_from_base@1` (materialize; a framework kind's name belongs to one runtime, so it is not `checkpoint_from_base`), with its defaults under `packs.toy`.

### Framework packs and the conformance suite

- A framework pack is the unit of extension: a runtime image and its environment lock; the worker plugin (entry points `cadence.steps`, `cadence.families`); the role step kinds, exporters and the transcribe step; pipeline templates and playbooks; a `defaults.yaml` section; help articles and an agent skill. The worker publishes the whole pack at start; each runtime, family and step kind is a registry version named by its published JSON, so a new digest or a changed kind is a new version (R45).
- Every pack passes one conformance suite on fixtures (`python -m cadence_worker.conformance --runtime <runtime>`, through the real harness path with a local store and no control plane): it checks schemas (`x-cadence` complete, help present, profiles declared, every required role mapped to a published kind that declares it), then imports the pack's fixtures with `dataset_import` (a `folder-csv` folder) and runs calibrate → train a few steps → stop → resume → average → transcribe (every latency profile; partial events for streaming ones) → score. Export and parity join in phase 5; the NeMo pack's run grows with the phases.
- CI runs it for two packs: a CPU `toy` pack (a tiny CTC model trained in seconds, existing only to keep the seams honest) on every pull request, and the NeMo pack nightly on the staging card. Packs beyond NeMo (sherpa-onnx first, then Hugging Face transformers, k2/icefall) are deferred without a phase.

### Step contract (phase 2, as built)

- A step runs in its own process per lease. Its inputs are materialised from the content store into a scratch directory (hard links; a directory artifact is a manifest of blobs); its outputs are hashed into the store at release with neutral meta (R42) and `layout: file|dir`. An input name may receive several artifacts as `<name>.0`, `<name>.1`, ….
- The step context reports progress, metric points (`name, value, step, epoch`), log lines and output meta, and exposes the card (device 0 through `CUDA_VISIBLE_DEVICES`; none for CPU steps), its memory cap (`CADENCE_MEMORY_CAP_MB`, applied with `set_per_process_memory_fraction` when torch is present), read-only access to blobs an input references (`ctx.blob(hash)`), the OOM retry's batch scale and the training state to resume from.
- Errors are typed: card out-of-memory → `oom` (one retry at 0.75× batch), bad inputs or parameters → `input`, anything else → `step`. A stop request (cancel, pause, a closing window) reaches the step as SIGTERM and `should_stop()`; a training step writes its `training-state` and the lease is released `cancelled` with it; after `CADENCE_STOP_GRACE_SECONDS` (60) the process group is killed. A train-role kind resumes from `overrides.resumeFrom` up to its total `steps`; a transcribe-role kind takes a `profile` parameter naming one of its family's latency profiles.
- Every parameter's default either is a literal with its source or comes from `defaults.yaml` through `x-cadence.defaultRef`; pack defaults sit under `packs.<pack>`, read from the control plane's own file, copied unchanged into each image (`CADENCE_DEFAULTS_FILE`). Ranges are enforced before `run`.
- Secrets named by the kind reach only the step process's environment and are redacted from its forwarded logs.
- A framework pack passes the conformance suite (`python -m cadence_worker.conformance --runtime <runtime>`, R45); the CPU toy pack (runtime `toy`, family `toy-ctc`) runs it on every pull request (`make conformance`). A new step is one module, its schema, `docs/help/steps/<kind with _ as ->.md` and an entry point.
- Phase 3 (stream Y): a step kind's descriptor may list `optionalInputs` (`StepKindDescriptor.optionalInputs`); the
  pipeline plan skips an optional input a pipeline leaves unwired, and the step then runs without it (the NeMo
  transcribe kind's `boost`). Like `optionalOutputs` it does not change the kind's schema version. The conformance
  suite requires the `materialize` role beside calibrate, train, average and transcribe, reads the base model from
  the family descriptor's `conformance.base_model`, and reports the materialized base model's WER after the trained
  model's.

### Seams for later training modes

Built in phase 2, used later (R44): `runs.new` carries `init: base | checkpoint`, an enum that can grow (`scratch`); step resources carry `gpus` (1 in v1); a checkpoint names the tokenizer it was trained with (the base model's). Deferred without a phase: training from scratch with a Tokenizer registry kind, gang leases of several cards, adapter (LoRA) runs, multi-node training.

### The eval pipeline (phase 3)

`evals.new` (plan stream E) generates one pipeline run per eval holding only the missing cells; a cell is
`(model, golden set, latency profile, decoding)`, and one whose eval-record key exists is linked, not computed (02
"Evaluation entities"). For each missing cell:

```
materialize-<m>   family.roles.materialize   in: {base}                                   once per base model version
transcribe-<c>    family.roles.transcribe    in: {model: checkpoint, data: golden dataset, boost?: boost_list}
                                             params: {profile, target_lang}
score-<c>         wer_score@2                in: {hypotheses, data: golden dataset, normalizer}
```

- Step kinds and versions come from the published family descriptor (R41): Go never names them. A base model version
  is first turned into a checkpoint by the family's `materialize` kind (NeMo `checkpoint_from_base@1`), so an eval
  always transcribes a checkpoint.
- Transcription always runs the real streaming decoder at the profile, never an offline decode relabelled (R43).
- The `scores` output hook writes the eval record in the scoring step's transaction and the eval links the cell;
  `eval.{id}.progress` carries cells done and total and the eval's state.
- Axes: models (checkpoints, model versions, base model versions; the baseline's cells are added), golden sets,
  profiles (default `eval.matrix_profiles`, replay golden sets at the primary profile only), decoding (R24) and, in
  wave 2, the augmentation profile (an `augment_dataset@1` step before transcription).
- The bundled `eval-matrix.yaml` template is rewritten to this shape; the phase-2 playbook's `evals.new` and
  `evals.gate` steps go live with it.

As built (stream E; `internal/evals`):

- `evals.new` (`POST /projects/{p}/evals`) answers `201` with the eval, not `202` with a job (the eval is the handle,
  as a run is; 00 decision log); `200` for its dry run (cells, cached and to compute, the estimate) and `202` when an
  agent's estimate needs an approval (`gpu-spend`). The estimate is the audio hours of the missing cells ×
  `eval.gpu_hours_per_audio_hour` (0.025, basis `table`: measured on the stand, the pipeline decoder at batch 8 runs
  at RTF 0.0165, plus a 1.5× margin). An eval whose cells are all cached is done
  at once.
- Step ids are `materialize-m<n>`, `transcribe-u<n>` and `score-u<n>` (one unit per missing record key; a subject
  equal to its baseline computes once), plus `augment-g<n>a<k>` and the metric steps of stream R below. The pipeline
  run's `RunID` is the eval id, so step metric points arrive on `run.evl_….metrics`.
- Golden sets: the request's (`ver_…`, `@alias`, a collection, a `*` pattern), else those `gates.yaml` names (target
  and replay), else every adopted golden set. Patterns match adopted versions only; an explicitly named collection the
  project has not adopted falls back to its latest version.
- Profiles: the request's, else `eval.matrix_profiles` every compared family declares, plus the primary profile
  (matched by name, else by latency). Replay golden sets (the gate's replay list, or — without lists — a language
  outside the project's locales) run at the primary profile only.
- Events: `eval.created`, `eval.status_changed`, `eval.gated` on `entity.eval.{id}`; `eval.progress` on
  `eval.{id}.progress`. The pipeline run's state is the eval's status; when it is done every subject cell's delta is
  computed against the baseline's cell of the same golden set, profile, decoding and augmentation.
- `evals.get` (`GET /evals/{id}`) filters with `worst` (worst-N utterances), `cell`, `goldenSet`, `profile` and
  `role`; `evals.list` with `status` and `subject`.

### Scorers and metrics (phase 3)

Scorers are neutral core step kinds (CPU, every runtime): they read `hypotheses`, the golden set's `dataset` and its
`normalizer` artifact, and write `scores`. The normalizer interpreter is Python, driven by the payload (02
"Evaluation entities"). Rates are fractions (0.123), never percent.

`wer_score@2` writes a directory artifact (version 2, 2026-10-02: CER without spaces, the final not counted as a partial):

| File | Content |
| --- | --- |
| `summary.json` | `{schema: "cadence.scores/1", scorer: "wer_score@2", normalizer: {versionId}, language, utterances, refWords, refChars, wer, cer, werNoPunct, sub, del, ins, charErrors, buckets: [{lo, hi, utterances, refWords, wer}], stability?: {partialWords, unstableWords, ratio, editsPerSecond}}` |
| `utterances.jsonl` | One row per utterance in dataset order: `{audio, speaker?, group, durationS, ref, hyp, refWords, sub, del, ins, refChars, charErrors, ops: [[op, ref, hyp]]}`, `op` one of `=`, `S`, `D`, `I` (Diff reads them); `group` is the bootstrap unit: the call id when the dataset has one, else the speaker, else the audio hash |

| Metric | Definition | Scorer, phase |
| --- | --- | --- |
| WER, CER, S/D/I | Word and character error rate after the scoring normalizer, with substitution, deletion and insertion counts from the alignment | `wer_score@2`, 3 |
| `werNoPunct` | The same normalizer plus punctuation removal; equal to WER when the normalizer already strips punctuation (decision 5) | `wer_score@2`, 3 |
| Duration buckets | WER per utterance-duration bucket, bounds `eval.duration_buckets_s`, the last open | `wer_score@2`, 3 |
| Partial stability | Unstable partial word ratio: the share of words shown in partials that the final changed or dropped (Shangguan et al., Interspeech 2020), with edits per second beside it; from the `hypotheses` partial events (R54) | `wer_score@2`, 3 |
| Latency to final | Time from utterance end to the final that covers it, audio fed at real-time pace, p50 and p95; utterance ends from a frame-VAD model until per-channel VAD (phase 4), else the aligned reference's end, reported unavailable without either (R54, R26) | Wave 2 (plan stream R), 3 |
| Entity accuracy | Number classes (numbers, dates, phone numbers, amounts) compared after the pack's ITN; names and addresses need annotated spans (phase 4) | `entity_score@1`, wave 2 (plan stream R), 3 |
| Emission delay | Time from a word's aligned end to its first appearance in a partial, PR50 and PR90 (Yu et al., FastEmit, ICASSP 2021) | Needs aligned references, 4 |
| End-of-utterance, RTF, streams per card | 04 "Task and streaming metrics" | 4 (VAD); 5 (benchmark) |

Live tests, paced replays and eval runs compute the streaming metrics from the same partial events, so they agree
(R54). The conformance suite scores through `wer_score`.

As built (stream Y; help `steps.wer-score`):

- `summary.json` gains additive keys: `normalizer.hash`, `groups` (the unit used) and `hypotheses` (`family`,
  `weightsHash`, `decodingHash`, `profile`). Readers ignore keys they do not know; the schema stays
  `cadence.scores/1`.
- `group` is decided per dataset, all or nothing: the call id only when every row has a `callId`, else the speaker
  only when every row has one, else the audio hash — so one golden set never mixes resampling units.
- The word alignment is Levenshtein with ties broken towards fewer substitutions; CER is the character edit distance
  over the normalized texts with every space removed (`wer_score@2`; `@1` counted spaces, which inflated the CER the
  gate reads for `eval.character_error_languages`; FLEURS and Whisper report CER without spaces).
- Partial stability, positionally: a word is *shown* when a partial puts it at a position the previous partial held
  empty or held another word, and *unstable* when the final does not have it at that position; `ratio` = unstable /
  shown; `editsPerSecond` counts words a later partial (or the final) changes or drops per second of audio. Both on
  normalized text. The decode's last event (`final: true`) is not a partial: words only the final has never enter the
  denominator (`@2`; `@1` counted them as shown and stable).
- The scorer is part of the record key, so `wer_score@2` recomputes every eval record once (the `@1` records stay,
  unread).
- Utterances are joined to hypotheses by audio hash; a dataset utterance without a hypothesis fails the step
  (`input`).

As built (plan stream R; help `steps.augment-dataset`, `steps.entity-score`, `steps.latency-score`, `steps.frame-vad`):

- **Robustness axis.** `evals.new` `augmentations: [{profile: none | augment/<name>.yaml@<commit>, seed?}]` (none is
  always index 0; target golden sets only). The control plane renders the profile with `augment.*` defaults into an
  `augment_profile` artifact; the core kind `augment_dataset@1` applies it deterministically (per-utterance seed from
  the profile's seed and the audio hash; speed, noise from a noise bank, level, band-limit, G.711 μ-law/A-law; GSM-FR,
  AMR-NB and Opus are left out of the draw and reported) and writes a `dataset` of purpose `augmented`, which is never
  registered. The augmentation (kind, profile hash, seed) enters the cell's decoding hash, so unaugmented records keep
  their keys. `evals.get` answers the robustness matrix (each augmented cell's WER against the same cell without).
- **Metrics beside WER** are a second artifact type, `metric_scores` (`summary.json` `cadence.metric-scores/1` and
  `utterances.jsonl`), not an extension of `scores` (so `wer_score@1` and its records stay as they are). They are kept
  in `eval_metrics` beside the record (key: model × golden set × decoding hash × scorer × the configuration read) and
  shown per cell under `metrics`; reported, not gated.
- **Entity accuracy** (`entity_score@1`): the classes of the pack's `itn.yaml` (rendered as an `itn` artifact at the
  commit that last changed it), both texts first converted with the pack's examples (spoken → written), overlaps
  resolved longest first, multiset match per class.
- **Latency to final** (`latency_score@2`): the first partial whose text is final minus the utterance end from a
  `vad` artifact. Eval decodes run faster than real time, so emit times at real-time pace are simulated from the
  decode's own compute: the hypotheses row's `steps` (`[audio available ms, compute ms]` per chunk, silent chunks
  included, compute = the batch step's wall time over its streams) give `done[k] = max(available[k], done[k−1]) +
  compute[k]`, and a partial (its `step`) is emitted when its chunk is done; hypotheses without `steps` fall back to
  `@1`'s event times (`emit[k] = max(audioOffset[k], emit[k−1]) + Δemit[k]`, which charged a partial the silent chunks
  before it and the batch's other streams); a decode marked `pace: realtime` is used as is. Utterance ends come from the NeMo pack's `frame_vad@1`
  (NVIDIA Frame-VAD Multilingual MarbleNet v2.0, NVIDIA Open Model License, checked against R26 on 2026-10-02); Go
  finds the VAD as the newest kind that turns a `dataset` into a `vad`. Without one, latency is reported unavailable.
  The VAD model is pinned in `defaults.yaml` `packs.nemo.vad_*` (model, revision, onset, offset, minimum speech and
  silence), not as a registry base model; its model card names no Hebrew, so Hebrew utterance ends are the model's
  multilingual guess until per-channel VAD (phase 4).
- The gate reads augmentation 0 (none) only; robustness, entity accuracy and latency are reported, not gated (04
  "Block 3"). Augmentation runs on target golden sets only (replay sets get none).
- Reported-only metrics never fail an eval (phase-3 audit fixes): the VAD, entity and latency steps are `optional`
  pipeline steps — a failing optional step skips the (optional) steps that read it and the run still ends done — and
  the eval marks those metrics `unavailable` with the step's error; the WER cells and the gate go on.

As built (phase-3 audit fixes, eval correctness):

- **Optional steps.** A pipeline step may say `optional: true` (contract `PipelineStepDefinition.optional`): its failure
  (after the automatic OOM and lost-lease retries) skips the waiting steps that read it, which must be optional too
  (`pipelines.Check`), and the run ends done once every other step has.
- **Decoding hash.** A cell's decoding hash covers the transcribe kind, the family's own name for the column's
  profile, the decode language, every other transcribe parameter as the run resolves it against `defaults.yaml`
  (so a changed default such as `packs.nemo.live_stop_history_eou_ms` makes new records; only
  `cuda_context_reserve_mb` is left out — the batch size is not, since at 80 ms batch 1 and batch 2+ can differ by a
  word), the materialize kind of a base model, the boost list and the augmentation. Records computed before this
  change are not found again (with `wer_score@2` every key changed anyway); the record's descriptive `decoding` JSON
  names the decode `language`.
- **Profiles by latency.** Families share a profile when they declare its milliseconds (R43); a column carries the
  subject family's name and each transcribe step decodes at its own family's name for it. The gate resolves
  `primaryProfile` against the eval's columns by name, else by the milliseconds its name states, as planning does.
- **Languages.** `evals.new` refuses (422, path `/languages/<locale>`) a golden set whose decode language a compared
  model's base model lacks among its `locale:` tags, naming `languages` as the fix (as `runs.new` does for training).
- **CER where it is gated.** The model card and the robustness matrix (`EvalRobustnessRow.unit: char`) report CER for
  the golden sets of `eval.character_error_languages`.

### What derives from the schema

| Surface | How it appears |
| --- | --- |
| API | `pipelines.list|run` (`GET /projects/{p}/pipelines?ref=`, `POST /projects/{p}/pipelines/{name}:run`), `pipelineRuns.list|get|cancel|retry|wait` (`GET /projects/{p}/pipeline-runs`, `GET /pipeline-runs/{id}`; retry re-runs one failed step), `artifacts.get` (`GET /artifacts/{hash}`), `stepKinds.list|get`, `runtimes.list|get`, `modelFamilies.list|get` (`/registry/…`); block endpoints such as `runs.``new` or `evals.``new` are thin facades over the same engine |
| MCP | `pipelines.list`, `pipelines.run` (dry run first), `pipelineRuns.*`, `stepKinds.list|get`; an agent reads a kind's parameter schema from `stepKinds.get` and the resolved parameters from the dry run (the per-kind tool description is not generated) |
| UI | The Pipeline run panel shows any pipeline; Inspector renders parameters as a form from the schema; a dedicated panel is optional polish |
| Events | `pipeline_run.{id}` carries step status changes; `job.{id}` and `job.{id}.log` each step job's state and log |

### Extension points

| To add | Touch | Nothing else changes because |
| --- | --- | --- |
| A processing, training, eval or export step | One step-kind module with its schema | Registry publishes it; pipelines reference it by name and version |
| A training framework or model family (icefall, Hugging Face, ESPnet, …) | A framework pack: runtime image, step kinds for the family's roles, family descriptor with latency profiles, pipeline templates and playbooks, a `defaults.yaml` section, help, an agent skill (R40–R45) | Scorers, gates, Diff, Audio, triage and the registry read neutral artifacts; the conformance suite proves the pack before it ships. Packs beyond NeMo are deferred; the seams are built in phase 2 (R45) |
| A deployment target | A target descriptor listing the families and formats it serves, its repository builder and parity check (R46) | Promotion checks family and format against the target |
| An audio track or a chart | A track in the shell's audio view or a chart in the catalogue, reading a contract resource (R51–R53) | Panels compose tracks and charts; the numbers come from the API that agents read |
| A flywheel signal | A signal provider in the same registry, producing Signal rows | Triage reads signals generically |
| A scorer | A versioned neutral step kind reading `hypotheses`, `dataset` and `normalizer`, writing `scores` | Eval records key on the scorer's `kind@version` |
| A scoring normalizer | A new registry version of kind `normalizer` (R21) | Golden sets pin the version; eval records key on it |
| A storage backend | A mount driver (see Storage and mounts) | Utterances carry URIs, not paths |
| An agent | A driver in the agent host implementing the ACP-shaped interface | Chat and events are driver-agnostic |
| A window | A panel manifest | The registry and workspaces are data-driven |
| A command | A registry entry bound to an API operation | Palette, menus and MCP pick it up |

Rules: step kinds are versioned and a pipeline pins the versions it was validated with; a step declares idempotence by an input hash so re-running a pipeline skips finished work; no step reads the database directly — inputs and outputs are artifacts.

## Defaults

Every parameter in Cadence ships with a default, a one-line description, a source and a safe range, so a person who is not deep in ASR can create a project with three fields and run the whole loop; experts change what they want, and every change is visible as a departure from the default.

Rules:

- Defaults live in one versioned file, `defaults.yaml`, read by the wizard, the pipeline templates and the step schemas; a step schema carries `x-cadence: {default, description, source, range}` for every parameter, and the UI and MCP tool descriptions render from it.
- A value without a published source is marked "Cadence recommendation" and is re-checked after the spikes; a value with a source names it.
- Departures from defaults are recorded on the entity ("peak LR 5e-4, default 2e-4") and shown in Compare, so a bad result can be traced to a changed knob.
- "Reset to recommended" exists on every form and every pipeline.

### Key defaults

| Area | Default | Source |
| --- | --- | --- |
| Base model | `nvidia/nemotron-3.5-asr-streaming-0.6b`, pinned revision | Model card |
| Eval latency | Primary cell `160ms` (`[56,1]`, `eval.primary_profile`); the matrix runs `80ms`, `160ms` and `1120ms` (`eval.matrix_profiles`, those the family declares); replay golden sets at the primary profile only (R17, R20, R43) | NVIDIA guide: evaluate at deployment latency |
| Scoring | Normalizer `normalizer/basic` when a golden set names none (`eval.normalizer`); duration buckets from 0, 2, 5, 10, 20 s, the last open (`eval.duration_buckets_s`) | R21; buckets are a Cadence recommendation |
| Training stage (Nemotron family) | `init_from_nemo_model`, bf16, 3 000 steps, peak LR 2e-4, warmup 100, grad clip 5, clips ≤ 40 s | Community fine-tune kit; North Sami fine-tune |
| Continuation stage | New optimiser, peak LR 2e-5 | Community fine-tune kit (trial setting) |
| Batch | Bucket sizes from the family's calibrate step (OOMptimizer for Nemotron) under the card's memory cap; 22 GB on the shared staging card, an RTX PRO 5000 Blackwell 48 GB with vLLM resident (23.8 GB): a PyTorch allocator of ≈ 20.5 GiB keeps the process under 22 GB | NeMo Lhotse docs; docs/spikes/A3-nemotron-finetune.md |
| Replay | 15% of samples from the base model's other locales, drawn from `dataset/replay-base` (see Replay below); phase 2 caps it at ≈ 1 h per locale | NVIDIA guide recommends replay; share and caps are a Cadence recommendation |
| Checkpoints and windows | A checkpoint and training state every 20 minutes, so a window close or a preemption loses at most 20 minutes; compute is always available unless windows are set (R19) | Cadence recommendation |
| Data filters | 0.5–40 s, ≤ 30 characters per second, language-ID match, speaker-disjoint 2% validation | Cadence recommendation |
| Text style | Punctuated, cased, spoken-form numbers; per-locale normalizer (ivrit.ai normalizer for he-IL) | NVIDIA guide; ivrit.ai leaderboard |
| Golden set | ≥ 2 h stratified telephone sample from own calls plus the locale's FLEURS split | Cadence recommendation |
| Gate | Target golden sets must beat the baseline at the primary cell (`gate.target_rule` `beat-baseline`); replay golden sets may regress ≤ 0.5 absolute points (`gate.replay_max_regression` 0.005); deletions may not fall while insertions rise (`gate.deletions_insertions`); a project departs from these in `gates.yaml` (04 "Block 3") | Cadence recommendation |
| Shadow before canary | ≥ 20 h of replayed calls | Cadence recommendation |
| Sampling policy | 10% of calls plus every low-confidence utterance | Cadence recommendation |
| Retention, PII | 90 days; NER spans masked in text and cut from audio | Proposed default, awaiting confirmation |
| Budgets | 8 GPU-hours per project per day; 200 turns per agent session; 3 min inactivity timeout | Cadence recommendation |
| Manual tests | Nothing stored; one session per user, 15 min, 5 min idle; files up to 15 min of audio; 1 GPU-hour per project per day (R47–R49) | Owner (nothing stored); the limits are a Cadence recommendation |
| Audio views | 25 ms Hann window, 10 ms hop, mel scale to 8 kHz (4 kHz for 8 kHz audio), 80 dB range, magma colormap (R52) | Kaldi, Lhotse and NeMo feature framing; librosa `top_db`; perceptually uniform colormaps |
| Cache | High-water mark 80% of local NVMe; 40% quota per project | Cadence recommendation |
| Significance | 1 000-sample paired bootstrap 95% confidence interval on every WER delta (`eval.bootstrap_samples`, `eval.confidence`, seed `eval.bootstrap_seed` = 1), resampling whole calls, else speakers, else utterances (the golden set's `groups`, R54); a gate counts a gain or a regression only when the interval excludes zero | Bisani & Ney, ICASSP 2004; Liu & Peng, arXiv:1912.09508 (blockwise bootstrap) |
| Cards | Every dataset version gets a generated dataset card and every registered model a model card: composition, licences, lineage, eval records, departures from defaults | Datasheets for Datasets (Gebru et al., CACM 2021); Model Cards (Mitchell et al., FAT* 2019) |

### Replay

Replay exists to stop catastrophic forgetting in the base model's other 39 locales, so it needs breadth, not volume (R17).

- Corpus: a capped, licence-cleared sample per locale from public training splits (FLEURS train, Common Voice, Granary where it covers the locale), frozen as `dataset/replay-base` and adopted by every project; each source's licence is checked at adoption for commercial use.
- Caps: the full corpus is ≈ 5 h per locale (≈ 195 h across 39 locales). Phase 2 imports a capped sample, ≈ 1 h per locale from FLEURS train, through the import pipeline; the ≈ 5 h corpus is a later re-freeze of `dataset/replay-base` by the same pipeline.
- Replay golden sets: FLEURS test per locale, ≤ 300 utterances each, imported in phase 2 and evaluated at the primary latency only from phase 3. The gate measures the delta against the base model, so possible exposure of FLEURS in the base model's training does not bias it; 39 × 300 utterances is a small fraction of one eval run, and the bootstrap interval keeps small sets honest.
- A mix takes replay as groups flagged `replay`, which together get the replay share.

### Playbooks

A playbook is a pipeline chain with defaults filled in, a prefilled agent prompt, and an estimate; it appears as a button on the Project home and as an MCP tool (`playbooks.list|get|run`), and runs as a playbook session (05). Version 1 ships five:

| Playbook | Chain | Typical cost |
| --- | --- | --- |
| Adapt a new language | Ingest → freeze → mix with replay → calibrate → train → eval matrix → gate | 4–8 GPU-hours |
| Improve on telephony | Attach call recordings → telephony augmentation → continuation stage → eval on the phone golden set | 2–4 GPU-hours |
| Fix names and terms | Boost list from the project glossary → correction batch → short continuation | 1–2 GPU-hours |
| Weekly flywheel | Schedule: replay → signals → triage → correction batch → continuation → eval → shadow | 2–3 GPU-hours per week |
| Fine-tune from a dataset version | Mix with replay → calibrate → train → register checkpoints → (from phase 3) eval matrix → gate | 1–4 GPU-hours |

"Fine-tune from a dataset version" is the phase-2 gate and the core that "Adapt a new language" later prefixes with ingest and freeze.

Format (R16): a template version (`templateKind: playbook`) at `templates/playbooks/<name>.yaml`, copied into projects like other templates. Not built yet: phase 2 wave 2 (`playbooks.list|get|run`, playbook sessions) implements this format; no playbook template ships in wave 1.

```yaml
name: finetune-from-dataset                # = the file name
title: Fine-tune from a dataset version
description: …
typicalCost: 1–4 GPU-hours
availableFrom: 2                           # the roadmap phase from which it runs; later ones are listed, not run
inputs:                                    # in order; exactly one of required, defaultRef, from
  dataset: { type: dataset_version, multiple: true, required: true }
  replay:  { type: dataset_version, from: adoption, collection: dataset/replay-base }  # when the project adopted it
  base:    { type: base_model, from: project }   # the project's default base model
  steps:   { type: integer, defaultRef: training.steps }
  replayShare: { type: number, defaultRef: mix.replay_share }
chain:                                     # commands in order; the command's success ticks the step
  - { id: mix, command: mixes.new, accepts: [mixes.edit] }
  - { id: calibrate, command: runs.calibrate, estimate: { gpuHours: 0.1, minutes: 6, plusMinus: 0.5 } }  # a hint until runs.calibrate estimates
  - { id: train, command: runs.new, with: { baseModel: $inputs.base, steps: $inputs.steps, datasets: [$inputs.dataset, $inputs.replay] } }
  - { id: watch, command: runs.get, accepts: [jobs.wait], until: terminal }  # ticks when the run's status has ended
  - { id: checkpoints, command: checkpoints.list }         # top-k registered by the checkpoint hook
  - { id: eval, command: evals.new, estimate: { gpuHours: 0.5, minutes: 30, plusMinus: 0.5 } }  # live since phase 3
  - { id: eval-wait, command: evals.get, until: terminal }
  - { id: gate, command: evals.gate }
stop: [ { gate: failed }, { budget: exceeded }, { step: failed }, { approval: denied } ]
next: { done: …, stopped: … }              # the next-step suggestion written when the chain ends
prompt: |                                  # Go text/template over .Inputs.<name> (as text) and .Project.{Name,Slug,Locales}
  Run the playbook "Fine-tune from a dataset version" in project {{ .Project.Name }} … Base model: {{ .Inputs.base }} …
```

- Inputs carry a type and exactly one source: `required`, a `defaultRef` into `defaults.yaml` (the value and its safe range), `from: project` (the project's base model) or `from: adoption` (the project's adopted version of a collection), so a playbook asks only for what has none of them.
- The estimate is the sum of the chain's step estimates (R12), shown before the session starts: an operation with its own estimator gets the step's `with` (`runs.new` from the table or the calibration; `runs.calibrate` from the calibrate kind's plan over a mix; `runs.stage` and `runs.resume` from the runs service's plans over a parent run); a step whose operation cannot plan yet (the mix or the parent run exists only during the session) and other spending steps use the step's `estimate` hint; steps whose phase has not shipped are listed and skipped; the rest spend nothing.
- Stop conditions: a failed gate, an exhausted GPU or agent budget, a denied approval, a step failed after its retries.
- As built (phase 2, stream K): `playbooks.list|get` (`?project=` fills project facts and estimates with them; the ETag of `get` is the template version) and `playbooks.run` (`POST /projects/{p}/playbooks/{name}:run`, If-Match the version or `*`; `dryRun` answers the resolved inputs, the estimate, the plan and the rendered prompt). Five templates ship: "Fine-tune from a dataset version" runs in phase 2; the other four are listed with `availableFrom: 4` and `playbooks.run` answers `playbook-unavailable`. Templates are validated at start and in CI (`internal/playbooks`): every operation of a step that can run now is an implemented contract operation; later-phase steps only need `<entity>.<verb>` with a vocabulary verb. Agents cannot start playbook sessions (preset rule `sessions-are-for-people`); `playbooks.get` shows them the estimate.
- Phase 3 (stream E): the fine-tune playbook's eval steps run — `evals.new` (dry run first, a 0.5 GPU-hour hint until
  `evals.new` has a playbook estimator), a wait on the eval (`evals.get` until terminal) and `evals.gate`; a failed
  verdict stops it (`gate: failed`), and a passed one ends with "ask a person to register it" (`models.register` is an
  approval for agents). `eval-matrix.yaml` is one cell by hand (transcribe → `wer_score`); `evals.new` generates the
  matrix itself. The build still reports roadmap phase 2 to playbooks (`playbooks.CurrentPhase`), so the four
  `availableFrom: 4` playbooks stay listed only, as they should.

### Smoke project

The wizard offers "Try Cadence": a smoke project on a 2-hour FLEURS subset that runs the full loop — ingest, freeze, 300 training steps, eval, gate, export, parity — in about one GPU-hour. It validates the install and shows the workflow before any real data is attached.

## Language packs and hot words

Everything a locale needs — normalisation, inverse normalisation, transliteration, language-ID settings, boost lists, the golden-set recipe — lives as one directory per locale inside the project repository, versioned by commits like any recipe.

### Language pack

```
lang/he-IL/
  normalizer.yaml     # scoring and training text style: niqqud, spelling variants, punctuation, numbers
  itn.yaml            # spoken → written for output: numbers, dates, phone numbers, amounts
  translit.yaml       # script maps where a locale has two (sr: Cyrillic ↔ Latin)
  lid.yaml            # accepted locales, code-switch handling, thresholds
  boost/              # boost lists by domain: names.txt, products.txt, streets.txt
  golden-recipe.yaml  # how golden sets for this locale are sampled and sized
  README.md           # the locale's known pitfalls, for people and agents
```

- Cadence ships starter packs for he-IL, ru-RU, sr (Latin and Cyrillic), hr, bs, mk, hu, ro and bg; the wizard copies the pack for the chosen locales and `projects.sync` offers updates.
- Entities pin the pack's commit SHA: a run records which text style shaped its targets; an eval records the scoring normalizer *version* that scored it (through its golden sets and eval records), never a pack file.
- Packs move between projects by copying the directory; no registry version, because a pack is opinionated project configuration, not data. The one part that must be comparable across projects, the scoring normalizer, is a registry version the pack only references (R21).

Phase 3 (plan stream L; R21):

- `normalizer.yaml` references a scoring normalizer by collection (`normalizer/he-il`) beside the training text style
  (punctuation, casing, numbers in targets). Changing scoring rules means freezing a new normalizer version, which
  forces a new baseline (02 "Evaluation entities").
- Starter packs ship in `control-plane/templates/lang/<locale>/` for he-IL and sr first (the other locales above
  follow); bootstrap copies the packs of the project's locales and `projects.sync` offers updates.
- `langpacks.get|edit` read and commit a pack's files; `boost.edit` edits one boost list (`boost/<domain>.txt`, phrases
  with a weight). Both go through the recipes service like any file, so agents may also edit them in the worktree.
- The search index normalises text per locale with the pack's scoring normalizer.
- As built (stream L): `langpacks.list|get|edit` and `boost.edit` (`/projects/{p}/langpacks/{locale}[/boost/{domain}]`);
  a pack's version is the last commit that changed `lang/<locale>/` (ETag and If-Match). Edits are checked against
  each file's shape (`internal/langpacks`; unknown keys refused) before they commit; a person's edit commits to main,
  an agent's lands on a branch `langpack/<locale>-<date>` while the draft policy `language_pack` is `draft`. Boost
  list file: a `# weight: <float>` header, one term per line, `#` comments; the `boost_list` artifact is
  `{"terms": [...], "weight": w}` and its sha256 is the list's identity. `projects.sync` offers pack files three-way
  (unedited files update, edited or deleted ones stay, a missing pack comes whole). The search index folds with the
  scoring normalizer's character steps but keeps punctuation (identifiers stay searchable); `internal/textnorm` is
  the Go interpreter of `NormalizerPayload`, the same steps as the worker's scorer.
- Also as built: `langpacks.list` (the project's packs with their versions); the copied packs are recorded in
  `data.lock` as `template/langpack-<locale in lower case>` (`template/langpack-he-il`), like other templates, so `projects.sync` can diff them; defaults
  `langpacks.boost_weight` (1.0, the `# weight:` a new list starts with) and `langpacks.boost_max_terms` (5 000). The
  he-IL pack's scoring normalizer is `normalizer/he-il`; the sr pack references `normalizer/basic`, because Serbian
  golden sets are imported transliterated to Latin (`sr-Cyrl-Latn`). In the Language pack document, Commit is offered
  only for text that passed Check (the server's dry run) unchanged.
- Not built: the "test a phrase" box (below); starter packs beyond he-IL and sr.
- `boost.evaluate` and `augment.evaluate` are not operations (R1): boosting and augmentation are `evals.new` axes.

### Hot words

- Static boosting: the project's boost lists are applied at decode through NeMo's context biasing for RNNT (phrase boosting), each list with a weight; the Triton model repository carries the lists as decoding configuration, so updating a list is a config release, not a new model version.
- Dynamic boosting: Эра may pass per-call candidates (the person's name, the debtor's address) with the request; the inference contract defines the field and a cap on list size.
- Boosting is evaluated, not assumed (R24): decoding is an eval axis, `evals.new` takes `decoding: [{boost: none}, {boost: <list SHA>, weight}]`, and the transcribe step decodes with static RNNT phrase boosting (`nemotron_transcribe@2` and `@3`, an optional `boost_list` input). Boosted and unboosted cells score the golden-set subset that contains listed terms (entity recall) and general WER, because over-boosting shows up as insertions of listed terms; the gate is to use both (the entity-recall check waits for `entity_score@1`, 07 "Open questions"). As built:
  boosted and unboosted cells are computed and compared on general WER; recall of the listed terms is not a scorer
  yet (`entity_score@1` reads the ITN number classes, not boost lists), so a boosted cell's term recall is measured by
  hand (the numbers in "Framework packs" above).
- Windows: Language pack (document) with the boost-list editor and a "test a phrase" box: a one-utterance eval with and without the list until the Transcription tool lands, then a two-target transcription, boost on and off (R47). The box is not built yet (phase 3, stream U left it out); the boost-list editor is. Agent tools: `langpacks.get`, `langpacks.edit`, `boost.edit`; boosting is evaluated with `evals.new`.

## Augmentation

Telephony audio differs from the training corpora in noise, handset processing and codecs, so a project has an augmentation profile — a versioned file in the recipes repository — applied on the fly during training and used as a stress test in evaluation.

### Transforms

| Family | Transforms | Parameters and sources |
| --- | --- | --- |
| Background noise | Mix real noise: a noise bank mined from the non-speech regions of the project's own call recordings (per-channel VAD), plus public noise sets with compatible licences (MUSAN, CC BY 4.0) | SNR 0–20 dB; own-call noise preferred for realism |
| Room and handset | Convolve with room impulse responses (public RIR sets, synthetic rooms); handset frequency response | Small-room and car profiles |
| Aggressive noise suppression | Run clean audio through open-source suppressors (RNNoise, DeepFilterNet) at aggressive settings and through spectral gating to reproduce musical noise and over-suppression artifacts | Suppression strength swept |
| Codecs | G.711 μ-law and A-law, GSM-FR, AMR-NB at low bitrates, Opus at 6–16 kbps, double transcoding through ffmpeg; band-limit to 8 kHz and resample | One or two codecs in a chain |
| Network | Packet loss with concealment (Opus PLC), short gaps, jitter-induced stretches | Loss 0–5% |
| Level and dynamics | Gain jitter, clipping, automatic gain control and compressor emulation, DC offset | Realistic ranges from the noise bank statistics |
| Speed and pitch | Speed perturbation 0.9–1.1, small pitch shifts | NeMo and Lhotse defaults |

### Application

- A profile lists transforms with per-utterance probability and parameter ranges; `telephony` is the default profile, `clean` disables all; the seed is recorded on the run so an augmentation draw is reproducible.
- Training applies the profile on the fly through Lhotse transforms and NeMo augmentors; nothing is written to disk except the noise bank, which is a registry asset with its licence.
- One resampler (spike A5, "What the spec should change" 7): band-limiting and codec stages resample with the same
  polyphase filter as imports and the live path (`resample_poly`, which also streams frame by frame), so the
  Transcription tool's telephony simulation equals what training and the robustness matrix saw. The training
  augmentation's telephone stage still uses an FFT resampler (it cannot stream) and moves to polyphase with stream T.
- Evaluation runs the golden set through each profile as a robustness matrix (golden set × profile × latency): the augmentation profile is an `evals.new` axis, applied by a CPU core step `augment_dataset@1` (codec, band-limit, noise bank; seeded, the seed recorded) before transcription (phase 3, plan stream R); the gate can require a maximum WER degradation under `telephony` (not in `gates.yaml` yet, 07 "Open questions").
- Windows: the Recipe document renders a profile as a schema-driven form with a listen preview (apply to a sample utterance and play). Agent tools: `augment.preview`; robustness is evaluated with `evals.new` (R1).

### Profile file (phase 2)

A profile is `augment/<name>.yaml` in the project repository. Every value starts from `defaults.yaml` `augment.*`
(description, source, safe range there); the Recipe form's "Reset to recommended" writes those values back, and Save
commits the file to main (`recipes.edit`; `recipes.new` creates one). Keys the form does not know and comments stay.

```yaml
name: telephony
seed: 1234                      # augment.seed — recorded on the run
transforms:
  codec:      { probability: 0.5, codecs: [g711-ulaw, g711-alaw, gsm-fr, amr-nb, opus] }   # augment.codec_*
  band_limit: { probability: 0.5, cutoff_hz: 3400 }       # augment.band_limit_* (ITU-T G.712 passband)
  level:      { probability: 0.3, gain_db: [-10, 6] }     # augment.level_*
  speed:      { probability: 0.3, factor: [0.9, 1.1] }    # augment.speed_* (Ko et al. 2015)
```

Each transform applies to an utterance with its probability; ranges are drawn uniformly per utterance. Noise, room,
suppression and network transforms join the same file when their step kinds arrive.
- Sources: NeMo's lossy-codec augmentation and Lhotse's augmentation transforms are the base; suppressor and codec emulation chains are Cadence additions, validated by the robustness matrix.

## Interoperability

Data arrives in three formats and models may need to leave; both directions go through step kinds, so a new format is one module.

| Direction | Formats |
| --- | --- |
| Import | NeMo manifest JSON lines; Lhotse CutSet and Shar; Hugging Face datasets with an audio feature; Common Voice, FLEURS and ivrit.ai through SDP; a folder of audio with a CSV of transcripts; stereo call recordings split per channel |
| Export | NeMo manifest; Lhotse Shar; a dataset pushed to the Hugging Face Hub with its generated card after a licence check; a model to the Hub as `.nemo` plus ONNX with its model card, and in the transformers format where supported; a Cadence bundle (project repository, `data.lock`, the referenced registry versions) for moving a project between instances |

Imports index in place when the source is on a mount and register a Source with the licence the format carries; exports are jobs with the same approval rules as any registry publication.
