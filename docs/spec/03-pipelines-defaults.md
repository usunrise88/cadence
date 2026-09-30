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
  artifact"), `train-stage`, and `eval-matrix` and `data-ingest`, which name step kinds of phases 3–4 and fail
  validation until those are published.
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
- Validation at `dryRun` (`pipelines.run?dryRun=true`, `Engine.Prepare`): every `kind@version` is published, wiring
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
  runs), and advances the dependents, all in one transaction; a refusing hook fails the step and keeps nothing of it.
  Hooks also run for reused outputs and are idempotent per artifact hash.
- Reuse and retries: a ready step whose input hash (kind, version, resolved parameters, input hashes) equals a
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

```yaml
# pipelines/train-stage.yaml in the project repository
name: train-stage
inputs: { mix: mix, base: base_model }          # pipeline inputs by artifact type
steps:
  - id: calibrate
    kind: oomptimizer_calibrate@1
    in: { base: $inputs.base, data: $inputs.mix }
  - id: train
    kind: nemotron_finetune@1
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
| `eval-report`, `deployable` | Scores per cell; an export (format, files, serving metadata) |

Also: `text` (the `echo` check), manifest, waveform peaks, correction batch. A directory artifact carries `meta.layout: dir` (a file `layout: file`), which the worker adds to every output it releases. Scorers, gates, Diff, Audio, Shadow, triage and the Transcription panel read only these types.

### Runtimes, model families and latency profiles

- A runtime is a registry version: a container image pinned by digest, its environment lock (CUDA, PyTorch, the framework, Lhotse) and the worker plugin version. A worker process lives in one runtime and advertises it with its cards; card slots belong to the control plane per host and card. A framework gets its own image even when its wheels would fit another's. v1 ships one, NeMo Speech 26.07 (`nvcr.io/nvidia/nemo-speech:26.07`, runtime `nemo-speech`, `worker/Dockerfile`), plus the CPU runtime `toy` for CI and trying the seams (`worker/Dockerfile.toy`); the descriptors are baked into the images (`worker/runtime/*.json`), and compose runs one worker service per runtime (`worker`, profile `gpu`; `worker-toy`, profile `toy`). Adding a runtime is `runtimes.new` with approval, deferred with the packs beyond NeMo (R40).
- A model family is a versioned descriptor the runtime publishes beside its step kinds (R41): framework and architecture; checkpoint and export formats and what loading needs; input (sample rate, channels) and features; tokenizer kind; capabilities (streaming, word timestamps, confidence, boosting method, language prompting, train modes `finetune | adapter | scratch`); latency profiles; the step kind for each role (calibrate, train, average, transcribe, export, parity reference); its `defaults.yaml` section; help and skill slugs. Base models, checkpoints and model versions carry a family reference; the UI and MCP render family options from the descriptor's schemas.
- No control-plane or web code branches on a family or runtime name; the Nemotron family is named only in the worker's NeMo pack, `defaults.yaml` data, templates and docs, and a test greps for it.
- A latency profile has a name, the algorithmic latency, chunk and left context in milliseconds, the family parameters that realise it and a label. A family without streaming has one profile, `offline`. Eval matrices, the primary cell and eval records name profiles; families line up by milliseconds, not by parameter spelling (R43).

The first family, Nemotron 3.5 streaming (cache-aware FastConformer RNNT, NeMo):

| Profile | `att_context_size` | Label | Role |
| --- | --- | --- | --- |
| `80ms` | `[56,0]` | 80 ms · [56,0] | Eval axis |
| `160ms` | `[56,1]` | 160 ms · [56,1] | Primary cell |
| `320ms` | `[56,3]` | 320 ms · [56,3] | — |
| `560ms` | `[56,6]` | 560 ms · [56,6] | — |
| `1120ms` | `[56,13]` | 1120 ms · [56,13] | Eval axis |

Latency is 80 × (r + 1) ms for `[56,r]`, with a left context of 56 frames (4.48 s). Its step kinds: `oomptimizer_calibrate` (calibrate), `nemotron_finetune` (train), `checkpoint_average` (average), `nemotron_transcribe` (transcribe: file decode in streaming simulation at a profile → `hypotheses`), plus `checkpoint_register`; export and parity join in phase 5. They arrive with the NeMo pack (phase 2 wave 2); until then the `nemo-speech` runtime publishes only the neutral core kinds.

The toy pack's family `toy-ctc` (runtime `toy`) has the profiles `offline` and `320ms` and the kinds `toy_calibrate`, `toy_train`, `toy_average` and `toy_transcribe`, with its defaults under `packs.toy`.

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

### Seams for later training modes

Built in phase 2, used later (R44): `runs.new` carries `init: base | checkpoint`, an enum that can grow (`scratch`); step resources carry `gpus` (1 in v1); a checkpoint names the tokenizer it was trained with (the base model's). Deferred without a phase: training from scratch with a Tokenizer registry kind, gang leases of several cards, adapter (LoRA) runs, multi-node training.

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
| A scorer or normalizer | A versioned step kind; golden sets pin the version | Eval results carry the version |
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
| Eval latency | `[56,1]` (160 ms) as the primary cell; `[56,0]` and `[56,13]` also run | NVIDIA guide: evaluate at deployment latency |
| Training stage (Nemotron family) | `init_from_nemo_model`, bf16, 3 000 steps, peak LR 2e-4, warmup 100, grad clip 5, clips ≤ 40 s | Community fine-tune kit; North Sami fine-tune |
| Continuation stage | New optimiser, peak LR 2e-5 | Community fine-tune kit (trial setting) |
| Batch | Bucket sizes from the family's calibrate step (OOMptimizer for Nemotron) under the card's memory cap; 24 GB on the shared staging card, an RTX PRO 5000 Blackwell 48 GB with vLLM resident (≈ 24 GB), so memory fraction 0.5 | NeMo Lhotse docs; this deployment (inventoried 2026-09-30) |
| Replay | 15% of samples from the base model's other locales, drawn from `dataset/replay-base` (see Replay below); phase 2 caps it at ≈ 1 h per locale | NVIDIA guide recommends replay; share and caps are a Cadence recommendation |
| Checkpoints and windows | A checkpoint and training state every 20 minutes, so a window close or a preemption loses at most 20 minutes; compute is always available unless windows are set (R19) | Cadence recommendation |
| Data filters | 0.5–40 s, ≤ 30 characters per second, language-ID match, speaker-disjoint 2% validation | Cadence recommendation |
| Text style | Punctuated, cased, spoken-form numbers; per-locale normalizer (ivrit.ai normalizer for he-IL) | NVIDIA guide; ivrit.ai leaderboard |
| Golden set | ≥ 2 h stratified telephone sample from own calls plus the locale's FLEURS split | Cadence recommendation |
| Gate | Target locale WER must beat baseline; replay locales may regress ≤ 0.5 absolute points; deletions may not fall while insertions rise | Cadence recommendation |
| Shadow before canary | ≥ 20 h of replayed calls | Cadence recommendation |
| Sampling policy | 10% of calls plus every low-confidence utterance | Cadence recommendation |
| Retention, PII | 90 days; NER spans masked in text and cut from audio | Proposed default, awaiting confirmation |
| Budgets | 8 GPU-hours per project per day; 200 turns per agent session; 3 min inactivity timeout | Cadence recommendation |
| Manual tests | Nothing stored; one session per user, 15 min, 5 min idle; files up to 15 min of audio; 1 GPU-hour per project per day (R47–R49) | Owner (nothing stored); the limits are a Cadence recommendation |
| Audio views | 25 ms Hann window, 10 ms hop, mel scale to 8 kHz (4 kHz for 8 kHz audio), 80 dB range, magma colormap (R52) | Kaldi, Lhotse and NeMo feature framing; librosa `top_db`; perceptually uniform colormaps |
| Cache | High-water mark 80% of local NVMe; 40% quota per project | Cadence recommendation |
| Significance | 1 000-sample bootstrap 95% confidence interval on every WER delta, resampling whole calls (or speakers) rather than utterances (R54); a gate counts a gain or a regression only when the interval excludes zero | Bisani & Ney, ICASSP 2004; Liu & Peng, arXiv:1912.09508 (blockwise bootstrap) |
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
name: finetune-from-dataset
title: Fine-tune from a dataset version
inputs:                                    # asked for, or taken from the project
  dataset: { type: dataset_version, required: true }
  base:    { type: base_model, from: project }   # the project's default base model
  steps:   { type: integer, defaultRef: training.steps }
  replayShare: { type: number, defaultRef: mix.replay_share }
chain:                                     # pipeline runs or commands, in order
  - id: mix
    command: mixes.new                     # dataset + dataset/replay-base at the replay share
  - id: calibrate
    command: runs.calibrate
  - id: train
    command: runs.new                      # the train-stage pipeline; checkpoints register through its hook
  - id: eval
    command: evals.new                     # from phase 3
    phase: 3
  - id: gate
    command: evals.gate
    phase: 3
stop:                                      # conditions that end the playbook
  - gate: failed
  - budget: exceeded
prompt: |                                  # rendered with the inputs and the project facts
  Fine-tune {{ base }} on {{ dataset }} …
```

- Inputs carry a type and either a `defaultRef` into `defaults.yaml` or `from: project` (a project fact), so a playbook asks only for what has neither.
- The estimate is the sum of the chain's step estimates (R12), shown before the session starts; steps whose phase has not shipped are listed and skipped.
- Stop conditions: a failed gate, an exhausted GPU or agent budget, a denied approval, a step failed after its retries.

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
- Entities pin the pack's commit SHA: an eval records which normalizer version scored it; a run records which normalizer shaped its text.
- Packs move between projects by copying the directory; no registry version, because a pack is opinionated project configuration, not data.

### Hot words

- Static boosting: the project's boost lists are applied at decode through NeMo's context biasing for RNNT (phrase boosting), each list with a weight; the Triton model repository carries the lists as decoding configuration, so updating a list is a config release, not a new model version.
- Dynamic boosting: Эра may pass per-call candidates (the person's name, the debtor's address) with the request; the inference contract defines the field and a cap on list size.
- Boosting is evaluated, not assumed: an eval run scores the golden-set subset that contains listed terms with and without boosting (entity recall) and checks general WER for the regression over-boosting causes; the gate uses both.
- Windows: Language pack (document) with the boost-list editor and a "test a phrase" box that decodes a golden-set utterance with and without the list. Agent tools: `langpacks.get`, `langpacks.edit`, `boost.edit`, `boost.evaluate`.

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
- Evaluation runs the golden set through each profile as a robustness matrix (profile × latency); the gate can require a maximum WER degradation under `telephony`.
- Windows: the Recipe document renders a profile as a schema-driven form with a listen preview (apply to a sample utterance and play). Agent tools: `augment.preview`, `augment.evaluate`.
- Sources: NeMo's lossy-codec augmentation and Lhotse's augmentation transforms are the base; suppressor and codec emulation chains are Cadence additions, validated by the robustness matrix.

## Interoperability

Data arrives in three formats and models may need to leave; both directions go through step kinds, so a new format is one module.

| Direction | Formats |
| --- | --- |
| Import | NeMo manifest JSON lines; Lhotse CutSet and Shar; Hugging Face datasets with an audio feature; Common Voice, FLEURS and ivrit.ai through SDP; a folder of audio with a CSV of transcripts; stereo call recordings split per channel |
| Export | NeMo manifest; Lhotse Shar; a dataset pushed to the Hugging Face Hub with its generated card after a licence check; a model to the Hub as `.nemo` plus ONNX with its model card, and in the transformers format where supported; a Cadence bundle (project repository, `data.lock`, the referenced registry versions) for moving a project between instances |

Imports index in place when the source is on a mount and register a Source with the licence the format carries; exports are jobs with the same approval rules as any registry publication.
