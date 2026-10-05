# Cadence spec — Domain model, projects, registry, storage

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Domain model

Fifty entities across the five blocks, the project layer and the registry (three added by R15, R40–R41; Auxiliary model and Dataset export in phase 4; Deployment target in phase 5, R46); everything a model was built from is reachable from the model by lineage links, and nothing referenced by a promoted model can be deleted.

| Entity | Block | What it is | Links to |
| --- | --- | --- | --- |
| Source | Data | A corpus with licence, languages, kind (public, production, synthetic) | Utterances |
| Utterance | Data | One audio segment, identified by the BLAKE3 hash of its canonical 16 kHz PCM16 WAV (the file in the content store, or the segment of a longer file it was indexed from on a mount): duration, language, speaker, sample rate, channels, `mount://` URIs | Source, Transcripts, Mounts |
| Transcript | Data | Text for an utterance with origin (human, pseudo-label, model id) and confidence | Utterance |
| Recipe | Data, Training, Eval | A versioned file in the recipes repository: SDP config, mix, training YAML, eval-set definition | Commit SHA |
| Dataset version | Data | Fingerprinted selection with splits, statistics and lineage: a **draft** indexed in place on a mount, then **frozen** (cut into the content store) and immutable; quality checks, card and shards (phase 4) | Utterances, Recipe, Sources |
| Base model | Training | Upstream checkpoint: Hugging Face repo, revision, licence, model family; its registry version is also the first baseline model version ("Evaluation entities" below) | Model family |
| Mix | Training | Groups, weights, temperature and replay share over dataset versions | Dataset versions, Recipe |
| Run | Training | One optimisation stage (`run_…`): `init` (`base` or `checkpoint`, R44), model family, mix revision with the content hash of its rendered `input_cfg`, recipe (pipeline at a commit), card, step budget, seed, runtime version (image digest), status mirrored from its pipeline run, parent run for a stage | Base model or Checkpoint, Mix, Recipe, Pipeline run, parent Run |
| Job | All | A unit of work: a River job (`job_`); a pipeline step runs as a job of kind `step` that waits in the step queue and is leased to a worker (`lse_`) on one card, with resources (`gpu`, `gpus`, memory, disk, job kind), priority, pause, a log file, state | Run, Eval run, Export, Pipeline run |
| Checkpoint | Training | A `checkpoint` artifact at a step (`ckp_…`): the family's payload, what loading it needs, the tokenizer it was trained with, validation WER, weights hash; trained or averaged (from other checkpoints of the run); ranked by validation WER with the top k kept; optimiser state is a separate `training-state` artifact (R42) | Run, Model family, Artifact, averaged-from Checkpoints |
| Golden set | Eval (registry) | Frozen held-out test set per language and domain (registry kind `golden_set`, collection `golden-set/<name>`): an eval-only dataset version tied to one scoring normalizer version, never trainable; frozen by `goldenSets.freeze` with approval | Dataset version, Normalizer |
| Normalizer | Eval (registry) | A scoring normalizer (registry kind `normalizer`, collection `normalizer/<name>`): the versioned text normalisation both sides of a WER are compared after (R21); not the training text style, which is the language pack's | Golden sets, Eval records, Language packs that reference it |
| Eval | Eval (project) | One evaluation (`evl_`): models (checkpoints, model versions, base model versions) × golden sets × latency profiles × decoding configs, plus the baseline's cells; one cell (`evc_`) per combination, each linked to an Eval record; progress on `eval.{id}.progress` | Checkpoints, Model versions, Golden sets, Eval records, Pipeline run |
| Eval result | Eval | A cell's `scores` artifact: WER, CER, substitutions, deletions, insertions, `werNoPunct`, duration buckets, partial stability, per-utterance rows with alignment (03 "Scorers and metrics") | Eval record |
| Gate | Eval (project) | The project's `gates.yaml` in its repository: primary profile, target and replay golden sets, allowed regression, the deletions/insertions check, significance (04 "Block 3"); an eval's verdict records the file's SHA | Golden sets, Evals |
| Model version | Eval, Deploy (registry) | A checkpoint published by `models.register` once its eval passed the gate (registry kind `model`, collection `model/<name>`, R22): weights hash, family, base model, gate verdict with the gates SHA, lineage, model card; exports (a `deployable` per latency profile with its parity and benchmark reports) attach in phase 5 ("Deployment entities" below) | Checkpoint, Eval, Base model |
| Deployment | Deploy (project) | A model version's export on a target's slot at a stage: shadow (staging target, nightly replay), canary with traffic share, production (`dep_`, phase 5) | Model version, Model export, Deployment target |
| Deployment target | Deploy (registry) | Where models are served (`dtg_`, R46): `staging` (the compose Triton Cadence reaches) or `delivery` (a production server reached only by a person running a delivery script); the families, formats and latency profiles it serves, server version, target concurrency, card class, boost support | Deployments, Promotion records |
| Promotion | Deploy | An append-only, hash-chained record signed with the instance's Ed25519 key of who moved which model version's artifacts to which stage of a delivery target, why, and the receipt that confirmed the delivery script ran (`prm_`, R33) | Deployment, Approval, Deployment target |
| Production sample | Flywheel | An utterance captured from calls, PII-redacted, with the production hypothesis; published monthly into a registry Source | Deployment |
| Signal | Flywheel | Why a sample matters: model disagreement, low confidence, judge flag, operator correction | Production sample |
| Triage item | Data, Flywheel (project) | A segment in the review queue with candidate transcripts and consensus: from phase 4 a pseudo-label the ensemble could not settle (`tri_`), from phase 5 also production samples | Segments artifact, Signal |
| Correction batch | Flywheel | Accepted triage items packaged as a source for the next dataset version | Triage items, Source |
| Agent session | Cross-cutting | One agent conversation: driver, model, worktree, status, spend, transcript | Commands it issued |
| Approval | Cross-cutting | A pending or decided request for a gated action, scoped to a project or to the registry | Command, actor |
| Project | Cross-cutting | Unit of work: locales, default base model, repository, adoptions and aliases, budgets, gates, agent profile, workspaces | Every project-scoped entity |
| Mount | Data (registry) | A storage location (`mnt_`): kind (`local`, `nfs`, `smb`, `s3`, `hf`), root, credentials (a secret's name), read-only flag, licence hint, health, last scan | Utterance URIs, blob copies, exports |
| Auxiliary model | Data, Eval (registry) | A model a step uses beside the trained one — LID classifier, pseudo-label member, aligner (registry kind `auxiliary`, collection `auxiliary/<name>`): roles, licence, whether its outputs may be used commercially, languages, Hub weights or a running service (phase 4) | Step kinds that read it, Projects that adopted it |
| Dataset export | Data (project) | One export of a frozen dataset version (`dex_`): format (Lhotse Shar, NeMo manifest, Cadence bundle, Hugging Face Hub), target, files, mount copies, Hub commit (phase 4) | Dataset version, Pipeline run, Mount |
| Pipeline run | All | Execution of a pipeline version: per-step status, inputs, outputs, logs | Recipe (pipeline SHA), Jobs, artifacts |
| Schedule | Flywheel | A recurring automation: trigger, pipeline or agent task, driver, budget, last and next run | Agent sessions it starts |
| Agent profile | Cross-cutting | A project's agent configuration: driver, model, permission preset, references to the committed config files and instructions template | Project, Agent sessions |
| Compute | Cross-cutting (registry) | A host (`cmp_`) and its cards: card class, memory, memory cap per card, allowed job kinds (training, eval, shadow, export, data), availability windows per job kind (R19; a window without a time zone follows the instance's `policies.timezone`), each card's last worker telemetry, host health from worker heartbeats (`unknown`, `healthy`, `unreachable`); workers (`wrk_`, one per runtime and host) register on it; the control plane owns one slot per card | Jobs, Workers, Mounts reachable from the host |
| Secret | Cross-cutting (registry) | A named credential (Hugging Face, NGC, GitHub, S3, judge API): name, kind, where it lives; the value never enters the database or an agent context | Mounts, project repositories, jobs |
| Eval record | Eval (registry) | Cached result (`erc_`) for one model key × golden set version × normalizer version × decoding hash × scorer version, with its `scores` artifact; evals of every project reuse it and compute only missing cells; the unit an Eval is assembled from | Checkpoint, Model version or Base model; Golden set, Normalizer, `scores` artifact |
| Saved search | Cross-cutting | A named query in the qualifier language, per user per project; appears as a Library view and in the palette | — |
| Help article | Cross-cutting (registry, bundled) | Versioned markdown shipped with the app; addressed by slug from panel manifests, step schemas, error types and skills | Panels, Step kinds, error types |
| Step kind | All (registry) | A versioned worker plugin published by a runtime: parameter schema with x-cadence defaults, artifact types consumed and produced, resource needs, the family role it fills or `neutral`, secret names, help slug | Runtime, Pipelines, Pipeline runs, Help article |
| Playbook | All (registry) | A pipeline chain with defaults filled in, a prefilled agent prompt and a cost estimate; a button on the Project home and an MCP tool | Pipeline templates, Agent sessions it starts |
| Template | Cross-cutting (registry) | Bundled, versioned configuration the bootstrap copies into a project: pipeline templates, instruction templates, permission presets, skills; projects.sync diffs a project against the current versions | Projects |
| Credential | Cross-cutting | A session, invitation, agent token, API key, or a service token (agent host `cah_`, egress proxy `cep_`, worker `cwk_`, one per host): kind, scope, hash, expiry, last use | Project or registry scope; Agent session; Annotation batch; Compute host |
| Language pack | Data, Eval, Deploy (project) | A locale's directory in the project repository: the scoring normalizer reference and the training text style, inverse normalisation, transliteration, LID config, boost lists, golden-set recipe (03 "Language packs and hot words") | Commit SHA; Evals and Runs pin it; the Normalizer version it references |
| Annotation batch | Eval, Flywheel (project) | Items sampled from a dataset version's segments (`anb_`), guidelines at a commit, reviewers, double-annotation sample, agreement, adjudication state (04 "Annotation workflow") | Segments, Credential (invitation), Golden set or dataset version it freezes |
| Experiment | Training (project) | A question, a fixed mix and base, the runs that answer it, an optional sweep definition with a GPU-hour cap, the best run | Runs, Mix, Base model |
| Augmentation profile | Training, Eval (project) | A versioned file of transforms with probabilities and ranges; telephony by default | Commit SHA; Runs, Eval runs; Noise bank |
| Noise bank | Data (registry) | Non-speech segments mined from own recordings plus licensed public noise sets, versioned like a dataset: registry kind `noise_bank` (collection `noise-bank/<name>`), registered by `dataset_import` with `purpose: noise` (clips, hours, licence, source, artifact; no utterances); public sets from phase 2 (`pipelines/noise-bank`, MUSAN noise), own-call noise in phase 4 | Sources, Augmentation profiles |
| Notification rule | Cross-cutting (registry) | Event class → channels and timing; quiet hours | — |
| Runtime | All (registry) | A worker container image pinned by digest, its environment lock (CUDA, PyTorch, framework, Lhotse) and the worker plugin version; published by the worker at start as a `runtime` version (collection `runtime/<name>`); framework step kinds live in one runtime, neutral core kinds in all (R40) | Step kinds, Model families, Compute |
| Artifact | All | A content-addressed blob or directory manifest (`b3:<hash>`) with a neutral type, size, metadata and the producing step; every step input and output is one (R15, R42); indexed once, linked to every project that produced or consumed it | Pipeline step, Projects; Checkpoints, Dataset versions, Eval records that reference it |
| Model family | Training, Eval, Deploy (registry) | Versioned descriptor published with a runtime (collection `model-family/<name>`): framework and architecture, formats and what loading needs, input and features, tokenizer kind, capabilities (streaming, word timestamps, confidence, boosting, language prompt, train modes), latency profiles, role → step kind (calibrate, train, average, transcribe, export, parity), its `defaults.yaml` section, help and skill (R41, R43) | Runtime; Base models, Checkpoints, Model versions |

## Projects

A project is the unit of work — "Hebrew telephony", "Western Balkans regional" — with its own recipes branch, worktrees, budgets, gates and workspaces; everything else is scoped to it, and sources can be shared across projects read-only.

| Aspect | Rule |
| --- | --- |
| Scope | Work entities carry a `projectId`; registry assets do not (see Registry). Work lives under `/projects/{p}/…`, assets under `/registry``/…`; MCP tokens and agent sessions are bound to one project but may read the whole registry |
| Recipes | One repository per project — a GitHub repository chosen in the wizard or the internal bare repository — with `main` as the project branch; each agent session gets a worktree; a person's edits from the UI commit to `main` directly |
| Base model | A project has a default base model and may adopt several; a run starts from the default or any adopted base model, and the choice is recorded in lineage |
| Golden sets and gates | Golden sets live in the registry; a project adopts the ones its gates use; the gate is the project's `gates.yaml` (thresholds, target and replay golden sets), versioned like any recipe |
| Budgets | GPU-hours per day, agent spend per day and queue priority are set per project (`budgets.gpuHoursPerDay`, `agentTokensPerDay`, `queuePriority` −100…100, default `defaults.yaml` `budgets.queue_priority_per_project` = 0; `projects.new`/`projects.edit`); the queue interleaves projects by priority — waiting step jobs start by the project's queue priority (read live, so an edit reorders jobs already waiting), then the job's own priority, then first come — and one training slot per card still holds |
| Sharing | Everything reusable is in the registry: sources, dataset versions, golden sets, normalizers, base models, registered model versions. Projects own mixes, runs, evals, deployments and flywheel state; a model registered by one project can be adopted as base model by another |
| Workspaces | Layouts are saved per user per project; the URL is `/p/{project}/w/{workspace}` |
| UI | A project switcher in the menu bar; Library, Queue & GPU and Approvals filter to the current project, with an "all projects" toggle for Ops |
| Agent | `AGENTS.md` in the worktree describes the project: languages, base model, gates, active decisions; NOTES.md carries recorded learnings |
| Archive | An archived project keeps its artifacts and lineage read-only; its worktrees are removed |
| First project | Hebrew telephony: locale he-IL, base nvidia/nemotron-3.5-asr-streaming-0.6b at the pinned revision, a telephone golden set from own calls plus FLEURS he, replay across the other 39 locales |

### Project wizard

A project is created by a wizard and bootstrapped by a job; the user never assembles the directory by hand, and everything the wizard set can be changed later in the Agent settings panel and the Project document.

| Step | Choices | What the bootstrap does with it |
| --- | --- | --- |
| 1. Name and languages | Name, slug, locales (`he-IL`), domain (telephony) | `project.yaml` |
| 2. Base model | A catalogue entry with a pinned Hugging Face revision; Nemotron 3.5 ASR is the default | `project.yaml`, the model catalogue entry becomes the project's Base model |
| 3. Agent | Driver (Claude Code or opencode), model (Claude models on the subscription; for opencode any configured provider, including the self-hosted vLLM models), permission preset (the Guardrails table or a stricter one) | `.claude/settings.json`, `opencode.json` (permissions only, R2), the project's Agent profile |
| 4. Instructions | A template for `AGENTS.md` (default, minimal, or a saved custom one); `CLAUDE.md` is always `@AGENTS.md` | Rendered with the project facts and committed |
| 5. Repository | A new GitHub repository created through the API with a stored token, an existing repository URL, or the internal bare repository | Init or link the bare repository on the control plane, `main` as the project branch, the GitHub or linked repository as the remote `main` is mirrored to |
| 6. Storage and budgets | Mounts to attach, GPU-hours per day, agent spend per day | Mount links, project budgets |

Bootstrap job (`projects.bootstrap`, queued by `projects.new`, which answers `202 {jobId}` with the project in state `bootstrapping`): create or link the repository; write `project.yaml`, `AGENTS.md`, `CLAUDE.md`, `NOTES.md`, `data.lock`, the agent config files and the pipeline templates; copy Cadence's skills into `.claude/skills`; commit `bootstrap` (and push it to the remote); adopt the template versions `data.lock` names (the base model is adopted by `projects.new`); create the server-side worktree directory; create the default workspaces; mark the project `active` (or `failed` with the reason). Progress streams on `job.{id}`; the web shell then opens the Project document. Recommended mode needs only a name and a language: every other choice defaults from `defaults.yaml` (section `wizard`, budgets).

Repository (R10): every project has a bare repository on the control plane, served over smart HTTP at `/git/<slug>.git` to API keys and agent session tokens (the token as the basic-auth password); agent tokens push only their `session/<id>` branch. People's and the API's commits to `main` go through a server-side working clone; pushes to `main` from outside are accepted and appear as `recipe.{path}` events. GitHub and linked repositories get `main` mirrored after every change. Merges of session and sync branches are fast-forwards when `main` has not moved, else merge commits; a conflict changes nothing and answers `merge-conflict`. Merged branches are kept 30 days.

Agent settings panel (per project): driver and model, permission preset, the raw `opencode.json` and `.claude/settings.json` with schema validation, `AGENTS.md` editing — every change commits to the project repository, and running sessions pick it up at their next turn. API (phase 1, R1 names): `agentProfile.get|edit` (`/projects/{p}/agent-profile`; an agent's edit is gated), catalogues `baseModels.list`, `agentModels.list` (`/catalog/agent-models`), `templates.list?templateKind=instructions|preset`; `projects.note` appends to `NOTES.md`; `projects.sync` re-renders the template-owned files and commits the difference on a draft branch `sync/<date>`, accepted or discarded with `branches.accept|revert`; `recipes.list|get` read files at a ref with their history; `branches.list|get` show open branches and their diff against `main`.

## Registry

Everything reusable lives in a Cadence-wide registry as immutable versions; a project only references registry versions, records exactly which ones it used in a lockfile, and owns the work built on them.

This is the pattern of [W&B Registry](https://docs.wandb.ai/models/registry): organisation-level registries for models and datasets, collections of immutable artifact versions, aliases and lineage, shared across projects. MLflow's model registry (registered model → versions → aliases) and DVC's pointer files (data versioned by hash outside the code repository) follow the same split.

| Registry — shared, versioned, immutable | Project — the work |
| --- | --- |
| Source, Utterance, Dataset version, Golden set, Normalizer, Base model, Model version, Eval record, Auxiliary model, Noise bank, Mount, Deployment target, Step kind, Runtime, Model family, Pipeline template, Playbook, Instruction template, Permission preset, Skill; artifacts in the content store | Mix, Run, Checkpoint, Eval and its cells, Gate (`gates.yaml`), Annotation batch, Dataset export, Deployment, Promotion, Production sample, Signal, Triage item, Correction batch (until packaged), Agent profile, Agent session, Schedule, Approval, workspace layouts |

Rules:

- Collections and versions: a registry entry is a collection (`dataset/hebrew-calls`) of immutable versions named `YYYY-MM-DD.<sha>`; tags carry language, domain and licence. Versions never change; only aliases move.
- Aliases per project: `@train-current`, `@baseline`, `@production` point a project at a version; a pipeline references an alias, a run records the resolved version.
- Adoption: a project adopts a dataset version, golden set, normalizer, noise bank, auxiliary model, base model or model version by reference. Adoption checks licence and locale ("Registry in full" below); the Mix editor lists only adopted or adoptable datasets.
- Lockfile: Cadence writes `data.lock` in the project repository listing every registry version the project depends on, so a checkout of the repository says exactly which data a run used; pipelines resolve registry parameters through it (phase 4, "Registry in full").
- Publishing: registering a model version publishes it to the registry with its gating eval report; another project can adopt it as base model. Packaging a correction batch creates a registry Source with licence `internal`.
- Lineage both ways: a model version links to dataset versions, mix SHA, recipe SHA and base model; a dataset version links to its sources and pipeline run; every registry entry answers "used by".
- Storage: registry assets live in the content store and on mounts; a project repository holds recipes and the lockfile, never data.
- Deletion: nothing is deleted. A registry version referenced by anything cannot be archived (`version-in-use`); otherwise the admin archives it (`versions.archive`, soft delete).
- Events: work events carry `projectId`; registry events carry none, and a client shows them by reference ("used by this project").
- Adoption re-runs the leakage check: adopting a golden set is blocked (`golden-set-leakage`) if any dataset version the project has trained on overlaps it by fingerprint, until those datasets are re-frozen without the overlap.
- Production samples, once PII-redacted, are published as a registry Source per deployment target and month; any project adopts them like any other source, and retention applies to the source.
- Eval results are cached as registry Eval records keyed by model key, golden set version, normalizer version, decoding hash and scorer version ("Evaluation entities" below); two projects evaluating the same model on the same set never compute it twice.
- Templates and skills: bootstrap copies them into the project; projects.sync later diffs the project against Cadence's current templates and skills and offers the update as a draft commit.
- Runtimes, step kinds and model families are published by workers at start (06 "Worker protocol") and stored as registry versions named by their published JSON; no person registers them, and a runtime beyond the bundled ones needs `runtimes.new` with approval (deferred with the packs beyond NeMo).
- Step kinds: a version can be deprecated but not removed while any pipeline template or project pipeline pins it; deprecation shows as a warning on the Pipeline run and in the Recipe document. As built (phase 4) the worker pack deprecates a kind ("Registry in full").

Windows: Library gains a this-project / all filter and an Adopt action; registry documents (Source, Dataset version, Golden set, Model) show "Used by projects"; a Lineage tool draws the graph around the selection. API (phase 1, R1 names): versions per kind at `/registry/base-models`, `/registry/datasets`, `/registry/templates` (`<kind>.list|get`, each version with "used by"), collections at `/registry/collections` (`collections.list|get`), `registry.search`, `POST /projects/{p}:adopt`, `GET /projects/{p}/adoptions`, `GET|PUT /projects/{p}/aliases/{name}`. Agent tools: `registry.search`, `projects.adopt`, `aliases.set` and the list/get reads.

### Data entities as built (phase 2, R18)

The minimal data entities imports need; phase 4 completes them ("Data in full (phase 4)" below: sources registered first, utterance URIs, drafts and freeze, search, exports). All are registry data (no `projectId`; events on `entity.source.{id}`), in `internal/data`, migration `0014_data.sql`.

| Entity | As built |
| --- | --- |
| Source (`src_…`) | Unique name, licence (required), kind `public \| production \| synthetic`, languages, url, `trainingCleared` with who cleared it and when, `archived`, `rev`. Created by the first import that names it, **eval-only** (`trainingCleared: false`) until a person clears it. `sources.list\|get\|edit\|archive` at `/registry/sources`; `sources.get` adds utterance count, hours and the dataset versions built from it |
| Utterance (`utt_…`) | Identity = content hash, the BLAKE3 (`b3:…`) of its audio file in the content store; duration, language, speaker, sample rate, channels, bytes; owned by the first source that imported it. `utterances.list\|get` at `/registry/utterances` (filters source, dataset version and split, language; oldest first, `after` cursor; `get` by id or hash with fingerprints and memberships) |
| Transcript (`trn_…`) | Text with origin `human \| pseudo-label \| model:<id>` and optional confidence; one row per (utterance, origin, text) |
| Dataset membership | Which utterances a dataset version holds, the split (`train \| validation \| test`) and the transcript it uses; written once when the version is registered |
| Utterance fingerprint | Kind → value per utterance for leakage checks: `audio-b3` from every import; `file-b3` from `sdp_ingest@2` (the canonical hash of the whole track a segment was cut from — `audio-b3` and `file-b3` match each other); steps may add others |

Rules:

- Clearing a source for training is a person's decision: an agent's `sources.edit` is gated (preset rule `registry-changes`, approval); `sources.archive` is the admin's (agents: `no-deletes`). An archived source takes no new imports and cannot be edited; its utterances and versions stay.
- A dataset version is **eval-only** when it was registered `evalOnly` (golden and replay test sets), when its licence or any of its sources' forbids commercial use or derivative works (NC, ND, research only, or no usable licence on a source; `registry.TrainingForbidden`, R26 — read at the time of asking, so neither an earlier clearance nor a mix naming an unadopted `ver_…` gets past it; audit 2026-10-04 M2), or when any of its sources is not cleared *at the time of asking*. Mixes (`mixes.new|edit|preview`, draft accept) and `runs.new` refuse eval-only versions with `eval-only-dataset` (422); clearing a source makes its versions trainable without a re-import. Collections of versions eval-only at registration carry the tag `eval-only`.
- A dataset artifact a training step would read that the cache evicted is refused with `artifact-missing` (409) when the run starts and when the step is queued, naming `datasets.materialize`; Cadence does not materialise it for the caller (a copy back can take hours and counts against the project's quota; audit 2026-10-04 C6). The dry run (`runs.new`, `runs.stage`, `pipelines.run`) answers instead with a `needs-materialize` plan warning carrying the version, the bytes and shards to copy back and the mounts (`NeedsMaterialize`, the `datasets.materialize` plan), so the person sees what to bring back before anything is refused; `pipelineRuns.get` lists the same for a run not done (phase 4 tail).
- The pipeline engine's training guard (`data.TrainableArtifact`, audit 2026-10-02) trusts nothing the caller says about an input a training step (`jobKind: training`) reads: its type and meta come from the artifact index (a different type is `validation-failed`); a `dataset` artifact must be registered by a dataset version (else `eval-only-dataset`), must not be an augmented golden copy (meta `purpose: augmented` → `golden-set-leakage`) and every version registering it must be trainable; a `mix` counts by its content (the `cadence.mix/1` rendering: every `input_cfg` dataset version and artifact), never its meta. The same check runs on a training step's resolved inputs when the engine queues it, so a non-training step cannot pass a golden set through to training (the run fails with the reason).
- A re-import must carry the source's registry licence and kind, else the import step fails.

### The dataset artifact

What an import step produces and the `dataset` output hook reads (artifact type `dataset`): a directory artifact — a content-store manifest `{files: [{path, hash, size}]}` whose files are each their own blob.

| File | Content |
| --- | --- |
| `dataset.json` | Header: `format` (`cadence.dataset/1`), `name?` (collection without `dataset/`), `description?`, `source: {name, licence, kind, languages, url?, revision?, subset?}`, `splitRule` (`speaker-disjoint \| source \| all-train \| all-validation \| all-test`), `counts: {train, validation, test}`, `hours`, `tags?`, `evalOnly?` |
| `manifest.jsonl` | One line per utterance: `audio` (the audio file's path inside the artifact), `duration` (s), `sampleRate`, `channels` (1 when omitted), `language`, `speaker?`, `text`, `origin`, `confidence?`, `split`, `fingerprints?` (kind → value) |
| audio files | `dataset_import` writes `audio/<h2>/<h>.wav`: 16-bit PCM WAV, mono, 16 kHz, byte-identical on every host; the utterance's content hash is the file's blob hash |

The hook runs in the transaction that marks the step done: it reads the artifact strictly (unknown fields, missing audio, an audio twice, or counts and hours that disagree with the lines fail the step), ensures the source, upserts utterances by content hash and transcripts with origin, writes fingerprints, and registers a **frozen** `dataset_version` in `dataset/<step param name | header name | source name>`. Its fingerprint is the sha256 of the sorted `[audio hash, split, transcript text]` tuples, so the same content re-imported (in any order, from any pipeline run) returns the version already there. Its payload is the `DatasetPayload` with `sourceIds`, `licence`, hours and counts per split and language, `artifact` (the hash training steps read), `evalOnly`, `splitRule`, `tags` and `lineage` (pipeline run, step, step kind).

Starter pipelines: `pipelines/import.yaml` (one corpus, FLEURS Hebrew as shipped) and `pipelines/replay-base.yaml` (R17: `dataset/replay-base`, ≈ 1 h per locale of FLEURS train across the base model's 34 FLEURS-covered other locales, and one `dataset/replay-golden-<locale>` per locale, FLEURS test ≤ 300 utterances, `evalOnly`, tags `golden`, `replay`).

### Data in full (phase 4)

Phase 4 completes the data entities above: sources are registered before anything is ingested, utterances are
indexed in place on a mount, and an ingest ends in a **draft** dataset version that a person previews and freezes.
Plan: `docs/review/2026-10-03-phase-4-plan.md` (decisions 3, 4); the pipeline and its step kinds are in
03 "Ingest and pseudo-label pipelines"; Block 1 in 04. Migration `0034_ingest.sql` (`internal/data`).

**Sources in full.** `sources.new` (`POST /registry/sources`) registers a corpus with its licence, kind
(`public | production | synthetic`), languages and URL, eval-only until cleared. `sources.get` adds `clearances[]`
(the clearing history: created, licence changed, cleared, uncleared, by whom and when) and `ingests[]` (each dataset
version an import or ingest registered from it: pipeline run, project, step kind, draft or frozen, utterances, hours).
`sources.edit` refuses (`validation-failed` on `/trainingCleared`) to clear a source whose licence forbids commercial
use or derivative works, or to move a cleared source to such a licence; unclearing is always allowed.
**No licence, no ingest:** the pipeline engine's plan refuses a step parameter marked `x-cadence.registry: source`
whose source is missing, archived or has no usable licence (`unknown`, `none`, `NOASSERTION`; problem
`source-unlicensed`), and the draft hook checks again when the step's output lands.

**Utterance URIs.** An utterance keeps its `b3:` identity and gains where its bytes also live, one row per URI in
`utterance_uris` (migration 0033):

```
mount://<mount>/<path>[#t=<start>,<end>][&ch=<n>]
```

- A whole file, or a segment of one channel of a longer file (seconds, W3C Media Fragments style).
- The identity is the BLAKE3 of the **canonical** segment — a 44-byte WAV header and PCM16 mono 16 kHz samples —
  computed while indexing, so it equals the hash of the WAV the freeze cuts later, and leakage checks and fingerprints
  work before anything is copied.
- The worker resolves a URI to a local path through its lease's mount roots (`cadence_worker.mounts`, 06 "Worker
  protocol"); the draft hook records each member's URI.

**Draft dataset versions and freeze.**

| State | What it holds | How it gets there |
| --- | --- | --- |
| Draft (`frozen: false`) | A `dataset` artifact of format `cadence.dataset-draft/1`: the header and one line per segment with its mount URI, canonical hash, text, origin and split, the quality checks, statistics and card — no audio | `dataset_freeze@1` in `mode: draft`, the end of `pipelines/data-ingest.yaml`; the hook registers utterances by canonical hash, transcripts, fingerprints and memberships |
| Frozen (`frozen: true`) | The cut: a `cadence.dataset/1` artifact the phase-2 training path reads | `datasets.freeze`, or `dataset_import` (frozen at import, phase-2 behaviour) |
| Archived | Nothing changes but the state | `versions.archive` ("Registry in full" below) |

- `datasets.preview` (`POST /registry/datasets:preview`, body `{version, minDuration?, maxDuration?,
  minCharsPerSecond?, maxCharsPerSecond?, languages?, origins?, splits?}`) answers kept utterances, hours and speakers
  per language and split after the filters, and what each filter drops. Metadata only; nothing is written.
- `datasets.freeze` (`POST /registry/datasets:freeze`, body `{version}`: registry versions have no revision, so the
  body names the version instead of `If-Match`) runs the leakage check against every golden set first
  (`golden-set-leakage`), checks the freezing project's quota against the cut's bytes (`storage-quota-exceeded`; dry
  run too), then reruns the draft step's kind@version in `mode: cut` as a CPU pipeline run and answers `202` with it.
  When the cut lands, the hook checks that it holds exactly the draft's members (same canonical hashes, splits and
  texts) and that no golden set overlaps meanwhile, and sets `frozen: true` on the draft itself (the version keeps its
  id).
- Only frozen versions are mixed, trained on (`dataset-not-frozen`, `data.Trainable`), exported or adopted.
- A draft's fingerprint is the sha256 over its members and the recipe commit it was ingested from; a frozen import's
  stays the phase-2 one.
- `DatasetPayload` gains `frozen`, `quality` (`passed`; checks `silence_share`, `clipping_share`, `length_outliers`,
  each `pass | warn` with value and threshold — warnings never block a freeze), `card` (`{hash, bytes}` of the Markdown
  dataset card in the content store), `stats` (R53 series: duration, characters-per-second and level histograms,
  duration percentiles, hours by origin and by channel role, source sample rates, speakers), `shards[]`, `segments`
  (the segments artifact it was ingested from), `recipe` (project, pipeline, commit, step kind, params),
  `contentFingerprint` and `freeze` (pipeline run, started, frozen, actor). The Dataset version panel reads only these
  (11).

**The cut.** Not Lhotse Shar tars: `cadence.dataset/1` as in "The dataset artifact" above — one WAV per utterance at
`audio/<hex[:2]>/<hex>.wav` (byte-identical on every host, its blob hash the utterance's identity) — plus the members
grouped into shards, a Lhotse `MonoCut` manifest each at `shards/cuts.NNNNNN.jsonl.gz` (`data.shard_utterances`,
2 000). A shard is the unit of pinning, eviction and materialisation. The payload records `location: cas` and `pinned:
false` at freeze; `datasets.get` overlays the cache's state now — `cas`, `mount` (evicted, a mount copy brings it back)
or `missing`, and `pinned` while anything pins the version (`datasets.list` returns the recorded values). Real Shar tars are an
export (`shar_export@1`, 03 "Interoperability").

**Utterance search.** `utterances.search` (`GET /registry/utterances:search`) filters by transcript text
(case-insensitive), source, language (`he` matches `he-IL`), speaker, origin, duration bounds, dataset version and
split; oldest first with an `after` cursor. The Source and Dataset version panels and agents use it.

**Exports.** `datasets.export` (`POST /registry/datasets:export`, body `{version, format, project?, target?, hubRepo?,
hubPrivate?}`) exports a frozen version as `lhotse-shar`, `nemo-manifest`, `cadence-bundle` or `hf-hub`: to a
directory on a writable path mount (default `mount://<storage.export_mount>/<collection>/<version>/<format>`, mount
`exports`) or the content store. It answers `200` with the plan on a dry run, `201` with the export, `202` with the
approval for the Hub (preset rule `hub-export`; repositories private by default). Each export is a `dex_` row
(`dataset_exports`, migration 0037; `exports.list|get`) over a pipeline run in a project, reused by its input hash;
audio placed unchanged on a mount is recorded as mount copies of its blobs, so the cache may evict them.
`export-not-allowed` refuses production or unlicensed sources and golden-set data (a version a golden set is built on,
or one sharing any utterance or fingerprint, in any split, with a golden set's data: `data.GoldenShared`). The export
step kinds (job kind `export`: `shar_export`, `dataset_export`, `hf_push`) run only through `datasets.export`: a
pipeline naming one is refused (`export-not-allowed`). The Cadence bundle is per dataset version, not per project.
Step kinds and formats: 03 "Interoperability".

**Annotation batches** (`anb_`, project work, migration 0038) sample a dataset version's segments for people to
transcribe and freeze into a golden set or a training dataset version; the entity, its items, reviewers and freeze are
in 04 "Annotation workflow".

### Auxiliary models (phase 4, R26)

Models a step uses beside the trained one: LID classifiers, pseudo-label members and aligners. Registry kind
`auxiliary`, collection `auxiliary/<name>`, read by `auxiliaries.list|get` (`/registry/auxiliaries`; `get` adds the
projects that adopted it and, for a service, whether its endpoint answers now). `internal/auxiliary`, migration 0035.

- **Payload** (`AuxiliaryPayload`): `roles[]` (`lid | pseudolabel | align`), `licence` (of the weights and, where it
  differs, of their training data), `outputsCommercialUse`, `conditions[]` (attribution, forbidden uses, the languages
  it is fit for), `languages` (primary tags or `*`), either `{hfRepo, revision}` (weights a step loads per job, offline
  from the worker's HF cache) or `service: {kind, endpoint, protocol, tokenSecret?}` (a running service Cadence never
  starts), `engine` (what the pack loads the weights with), and `sources[]` and `checkedAt` of the licence check.
- **Seeds** (the plan's licence table; only rows that passed): `whisper-large-v3`, `whisper-he-ivrit` (Hebrew only,
  attribution, never for voice cloning), `oasis` (the owner's gRPC service, protocol `oasis.v1`), `lid-voxlingua107`,
  `omniasr-ctc-1b` (the aligner). MMS models and unlicensed community fine-tunes are not seeded (CC-BY-NC or
  unverified).
- **Adoption** is an approval for everyone (preset rule `auxiliary-adoption`, registry scope, the admin decides);
  `outputsCommercialUse: false` is refused outright (`auxiliary-licence-refused`).
- **Use.** A step-kind parameter marked `x-cadence.registryRef: {kind: auxiliary, role}` resolves, for a collection
  name the project's `data.lock` lists at the commit the pipeline is read at, to the newest version the lock lists;
  otherwise (no repository or lock, a collection the lock does not list, `ver_…`, `@alias`) to the version the project
  adopted (newest by creation). Either way the version must be adopted (`not-adopted`, 422), fill the role and allow
  commercial use of its outputs; the resolution is stored in `pipeline_steps.auxiliaries`, passed in the
  step spec (`StepSpec.auxiliaries`) and covered by the input hash. A service's endpoint is probed at dry run and at
  start (`auxiliary-unavailable`, 503; for an optional step a plan warning, 03).
- **Seams.** Go never names a framework or a service: the payload is data, and the worker pack that serves the role
  reads it.

### Registry in full (phase 4)

What phase 4 added to the rules above (`internal/registry`, migration 0036).

- **Adoption checks.** `projects.adopt` refuses, dry run included:
  - `licence-forbids-adoption` — no usable licence, `outputsCommercialUse: false`, or a non-commercial or
    no-derivatives licence (NC, ND) on what is trained on or shipped: base models, model versions, noise banks,
    auxiliary models and trainable dataset versions. NC/ND are allowed for golden sets and eval-only dataset versions;
    kinds that hold no outside data or weights pass;
  - `locale-mismatch` — a dataset version, golden set, normalizer or auxiliary model that declares languages (its
    collection's `locale:` tags and the payload's languages; `he` matches `he-IL`), none of them the project's, unless
    the adoption has `purpose: replay`.

  An auxiliary adoption answers `202` with its approval. Runtimes, model families and step kinds are never adopted.
  Resolving a collection name prefers the version the project adopted.
- **Archive.** `versions.archive` (`POST /registry/versions:archive`, body `{version}`) is the admin's soft delete:
  state `archived` is terminal and the version stays readable with its lineage. A version anything uses is refused
  with `version-in-use`, each user a field error: projects that adopted it or default to it, other versions whose
  payload names it, runs, experiments, mix revisions, eval records and pipeline steps pinned to it. Versions workers
  publish (runtimes, step kinds, families) are not archived.
- **`data.lock`.** `projects.adopt` writes the adopted version into the project repository's `data.lock`
  (`resolved: [{kind, collection, version, id}]`). A step parameter marked `x-cadence.registry: <kind>` (any kind but
  `source`) names a collection, `@alias` or `ver_…`; the engine resolves it through `data.lock` at the commit the
  pipeline is read at — through the project's adoptions for bundled templates and projects without a repository — and
  refuses a version the lock does not list (`not-adopted`) and an entry the project never adopted (data.lock pins, the
  adoptions allow). A collection name resolves to the newest listed version by when the registry created it, not by
  its name (two versions of a day differ only in a hash). `aliases.set` on a version the project has not adopted is
  `not-adopted` too. Merging a branch adopts only the template versions its `data.lock` names (a template sync), never
  a `resolved` entry: an edited lockfile skips the licence, locale, leakage and auxiliary checks, so it adopts nothing,
  and an agent session's edit of `data.lock` waits for a person (05 "guarded paths"). The plan reports each resolution
  in `PlanStep.locked` (`LockedReference`); params are not rewritten, and input-hash reuse ignores the resolved version.
- **Step-kind deprecation.** A worker pack marks a kind `deprecated_after` (a date), `replaced_by` and a note when it
  publishes it (`StepKindDeprecation`). Plans warn (`PipelineWarning` `step-kind-deprecated`); after the date a
  pipeline file that does not pin the kind yet is refused when it is saved (`step-kind-deprecated`), while files that
  already pin it keep running. There is no deprecate verb.
- **Library.** Sources are listed with an Adopt action and the this-project / all filter; Source and Dataset version
  are documents (11).

### Evaluation entities (phase 3)

Golden sets, scoring normalizers, eval records and model versions are registry data; evals, their cells and the gate
are project work. The plan `docs/review/2026-10-02-phase-3-plan.md` fixes the shapes (decisions 1–7, recorded in
`00-overview.md`); payload schemas are `GoldenSetPayload`, `NormalizerPayload` and `ModelPayload` in
`api/openapi.yaml`. IDs: golden sets, normalizers and model versions are registry versions (`ver_`); evals `evl_`,
cells `evc_`, eval records `erc_`. Events: `entity.golden_set.{id}`, `entity.model.{id}`, `entity.eval.{id}`,
`eval.{id}.progress`.

**Scoring normalizers (R21).** Scoring and text style are different jobs:

| | Scoring normalizer | Training text style |
| --- | --- | --- |
| What it does | The text both reference and hypothesis are compared after; WER is computed on its output | Punctuation, casing and numbers in training targets |
| Where it lives | Registry kind `normalizer`, collection `normalizer/<name>`, immutable versions named `YYYY-MM-DD.<sha of the canonical JSON>` | The project's language pack (`lang/<locale>/normalizer.yaml`), pinned by commit SHA |
| Who pins it | Golden sets, eval records, the language pack (by reference) | Runs |

- Payload (`NormalizerPayload`): `locale` (BCP 47 or `*`), `unicode` (`NFC | NFKC`, applied first), `casefold`,
  `mappings` (literal `{from, to}` replacements in order, after the Unicode step and before punctuation),
  `punctuation` (`keep | strip`: every Unicode `P*` character becomes a space), `removeMarks` (combining marks such as
  niqqud and accents removed after canonical decomposition), `numbers` (`keep`; spoken/written conversion through the
  pack's ITN comes in phase 4).
- Seeds: `normalizer/basic` and `normalizer/he-il` (collection names are lower case; the payload's `locale` keeps
  BCP 47 case, `he-IL`); a freeze that names none uses `defaults.yaml` `eval.normalizer` (`normalizer/basic`).
  `normalizers.list|get` read them. Serbian scores with `normalizer/basic` (no transliteration step): its golden sets
  are imported already transliterated (`dataset_import@3`, `sr-Cyrl-Latn`).
- Two interpreters of the payload run the same steps: the scorer's (Python, `wer_score`) and `internal/textnorm` (Go),
  which the search index uses for per-locale folding (03 "Language pack").
- The control plane renders a version into a `normalizer` artifact (the payload as JSON, `meta: {versionId}`), as it
  renders a base model version into a `base_model` artifact; scorer steps read only that artifact.
- A changed scoring rule is a new registry version, never an edit; golden sets pin the version, so a new one means a
  new golden-set version whose cells (the baseline's included) are computed afresh: a new normalizer forces a new
  baseline by construction.

**Golden sets.** Registry kind `golden_set`, collection `golden-set/<name>` (decision 7).

- `goldenSets.freeze` takes a frozen, **eval-only** dataset version (02 "Data entities as built") and a normalizer
  version, and registers a `golden_set` version whose payload (`GoldenSetPayload`) holds `datasetVersionId`,
  `datasetHash` (the `dataset` artifact), `normalizerVersionId`, `locale`, `domain`, `utterances`, `hours`, the dataset
  version's `fingerprint` and `groups` (`call | speaker | utterance`: the bootstrap's resampling unit, R54).
- The freeze is a registry action: an approval at registry scope that only the admin decides (05 "Guardrails"); an
  agent's call answers the approval id. Refusals: `golden-set-not-eval-only` (the dataset version is trainable),
  `normalizer-unknown`, `golden-set-leakage` (below).
- `goldenSets.list|get` read them; `get` adds "used by" (the projects that adopted it; gates and evals through
  `registry.lineage`).
- The 34 replay golden datasets (`dataset/replay-golden-<locale>`) and FLEURS he become golden sets by freezing them on
  the stand at the phase-3 gate.

As built (phase 3, stream G; `internal/goldensets`, migration 0023 `golden_sets`):

- Eval-only for a freeze means *registered* `evalOnly` (the dataset header's `evalOnly: true`), not merely eval-only
  because a source is not cleared yet: a version that could become trainable by clearing a source is refused
  (`golden-set-not-eval-only`).
- A golden set holds one locale: a dataset version with utterances in several languages is refused, and the
  normalizer's `locale` must share the dataset's primary language subtag unless it is `*` (`validation-failed`).
- `groups`: the request's, else `speaker` when every utterance names a speaker, else `utterance`; `call` is refused
  until a dataset carries call ids (they arrive with production imports, phase 4).
- The freeze is a registry-scope approval for everyone, people included (preset rule `golden-set-freeze`,
  `everyone: true`); the agents' rule `evaluation-gates` therefore gates only `gates.edit`. It answers `200` for a
  dry run (with the `Cadence-Policy` header naming the approval the real call needs) or when the same content was
  already frozen (that version is returned), `201` for the approved call, `202` with the approval id otherwise.
  Event `golden_set.frozen` on `entity.golden_set.{id}`.
- Refusals for leakage carry every overlap in the problem's `errors[]` ("<dataset> shares N utterances with
  <other>", both versions named), so a person can re-freeze without them. `golden-set-leakage` is in the error lists
  of `mixes.*`, `runs.*` and `pipelines.run`.

Phase 4 additions:

- A golden set can come from an annotation batch: `batches.freeze` writes the accepted items as a draft dataset
  version, freezes it and calls this path with the batch's guidelines commit and inter-annotator WER in the card (04
  "Annotation workflow"). The ingest draft behind a golden-set batch must be eval-only, or the freeze is refused for
  leakage.
- `datasets.freeze` runs the same leakage check before it cuts a draft, against every golden set.
- **Reference alignments** (R51, R54; migration 0039, `reference_alignments`): `align_reference@1` writes an
  `alignment` artifact (`cadence.alignment/1`, word timings of the reference text) for a golden set's dataset; the
  output hook attaches it by dataset hash (the newest wins) and emits `golden_set.aligned`. Evals feed it to
  `latency_score@3` for emission delay (03 "Scorers and metrics"). Aligning is run on request, not at freeze:
  `goldenSets.align` (phase 4 tail) aligns several golden sets in one pipeline run — the project's adopted golden sets
  by default, or named ones (ver_…, collection names, `*` patterns) — with one optional step of the aligning kind (the
  newest published kind that turns one `dataset` into an `alignment`) per dataset artifact, so the batch is one
  GPU-spend decision (rule `gpu-spend`) with a known estimate (03 "Key defaults": `eval.align_step_overhead_s` + audio
  hours × `eval.align_seconds_per_audio_hour` per step). It skips, with the reason, golden sets already aligned
  (`aligned`), with an unfinished aligning step on their dataset (`running`) and those whose locale the resolved
  aligner does not list (`language`); an aligner the project has not adopted, or no live worker of the kind, refuses
  the request. One golden set can still be aligned by hand (`pipelines/align-reference.yaml`); a language with no
  allowed CTC aligner stays unaligned.

**Leakage and training exclusion.** A golden set's utterances never reach training, checked by utterance fingerprint
(`utterance_fingerprints`: `audio-b3` from every import and `file-b3` from `sdp_ingest@2`, which match each other, so a
golden file re-cut from a mount is found; an acoustic fingerprint is not built):

| Where | Check | Refusal |
| --- | --- | --- |
| `goldenSets.freeze` | No fingerprint of the set appears in a dataset version a run has already trained on | `golden-set-leakage` (422) with the overlapping dataset versions and counts |
| Mix validation (`mixes.new`, `mixes.edit`, `mixes.preview`, draft accept) | No dataset version of the mix overlaps a frozen golden set | `golden-set-leakage` |
| Pipeline engine (`pipelines.run`, `runs.new`, every facade) | No `dataset` input or rendered `mix` input of a training step overlaps a frozen golden set | `golden-set-leakage` |
| `projects.adopt` of a golden set | No dataset version the project has trained on overlaps it | `golden-set-leakage` until those datasets are re-frozen without the overlap |

Runs cannot reference golden sets at all, so checkpoint selection can only use validation splits.

As built: the training side of every check counts only the **train and validation** splits of a dataset version
(training reads the first and selects checkpoints on the second; it never reads a test split), so a corpus imported
with its test split beside train — FLEURS — can still have that split frozen as a golden set (found at the gate,
2026-10-02). The freeze compares the set with every dataset version not registered eval-only and every version a
run has trained on, in any project. "Trained on" (freeze and adoption) is the dataset versions of the mix revisions
the project's runs used, plus the `dataset` and rendered `mix` inputs of its training pipeline steps.

**Eval records and the cross-project cache (R22, decision 4).** An eval record is a global row (registry scope, no
project) keyed by:

| Key part | Value |
| --- | --- |
| `modelKey` | The weights hash (`b3:`) for checkpoints and model versions; `base:<versionId>` for a base model version (its weights are materialised by the family's `materialize` step, so the hash is not known before the first run) |
| `goldenSetVersionId` | The golden set version |
| `normalizerVersionId` | The scoring normalizer version (the golden set's) |
| `decodingHash` | The transcribe step's decoding config hash: transcribe kind and version, latency profile, language, boost list hash and weight, augmentation (R24, R43; as built below) |
| `scorer` | The scorer `kind@version` (`wer_score@2`) |

- The `scores` output hook writes the record in the transaction that marks the scoring step done (idempotent per
  artifact hash), and the eval links its cell to it. A cell whose key already has a record is never computed again,
  in any project.
- An eval's pipeline is generated per eval with only the missing cells (03 "The eval pipeline").
- As built (stream E, migration 0024): tables `eval_records` (global, `erc_`), `evals` (`evl_`) and `eval_cells`
  (`evc_`); the decoding hash is the sha256 of `{transcribe: kind@version, profile, <the kind's locale parameter>:
  locale, boost?: {list: <boost_list hash>, weight}, augment?: {kind, profile: <hash>, seed}}` (no `beam` yet: no
  transcribe kind takes one). Eval artifacts are kept by age (owner decision 2026-10-03; 06 "Artifacts, metrics and
  logs", Retention): a record's `scores` and `hypotheses` and the `metric_scores` beside it are evicted
  `eval.artifact_retention_days` (30) after the record's last use, unless a registered model's eval or an unfinished
  eval links it; the record (its summary), the cells' stored deltas and the verdicts stay for ever. `evals.get` marks
  such a cell `evicted {at, retentionDays, note}` and answers no `worst` rows; `evals.gate` reuses a cell's stored delta
  at the eval's significance and otherwise gets the delta error "per-utterance scores evicted (older than N days);
  re-run the eval" (the check is inconclusive); `words.get` answers `410 artifact-evicted`. Planning never links a
  record whose artifacts were evicted: the cell is computed again and the `scores` hook gives the record the new
  artifacts (summary unchanged), so the older evals linking it get their rows back. Metrics beside WER (entity accuracy, latency to final) are kept in `eval_metrics` (`erm_`,
  migration 0028) under the same key plus the scorer's configuration (03 "Scorers and metrics").

**Model versions and registration (R22).** `models.register` publishes a checkpoint as a registry version of kind
`model` (collection `model/<name>`) once an eval of it passed its gate; without a passing verdict it answers
`gate-not-passed`. The payload (`ModelPayload`) holds `checkpointId`, `weightsHash`, `checkpointHash`, `familyId`,
`baseModelVersionId`, `projectId`, `evalId`, `gate {verdict, gatesSha}`, `lineage {runId, mixSha, recipeSha,
datasetVersionIds}` and the generated model card (Markdown: composition, licences, lineage, eval records, departures
from defaults). `models.list|get` read them; another project can adopt a model version as its base model. Export,
parity and benchmark stay in phase 5. Experiments' "register best" is the same call.

As built (stream E): `models.register` is `POST /projects/{p}/models:register` (a project path; the write is a
registry version). It takes the checkpoint's latest gated eval (or the named `evalId`) and refuses a failed or missing
verdict for everyone (`gate-not-passed`); an agent's call additionally waits for a registry-scope approval (preset
rule `registry-changes`), and the fine-tune playbook ends by asking a person to register. The collection defaults to
`model/<project slug>`; tags are the base model's `locale:` tags plus `family:<id>`, the licence is the base model's;
the card is dated by the gate. The new version is frozen and adopted by the project at once (`model.registered` on
`entity.model.{id}`). Answers: `200` dry run (the collection and payload), `201` the version, `202` an approval.

**Baseline (R23, decisions 1–2).**

- The base model's existing `base_model` registry version *is* the baseline model version: the `baseline` alias may
  point at a `base_model` or a `model` version, and the base model is never registered a second time.
- A project whose `baseline` alias is unset uses its default base model.
- Changing the baseline is `aliases.set` with name `baseline`, approval-gated (R8); there is no `baselines.set`.
- Baseline cells are ordinary eval records (`modelKey` `base:<versionId>` or the model's weights hash), computed once
  and reused by every project; an eval computes the ones it lacks (`eval-baseline-missing` when `evals.gate` finds
  none for a gated cell).

**Lineage both ways.** `registry.lineage` (`GET /registry/{id}:lineage`, R1's read-action shape: the id names the
kind by its prefix, so one operation serves every registry kind and project work) answers the graph around a registry
version in both directions: upstream (a model version → checkpoint → run → mix SHA, recipe SHA, dataset versions →
sources; base model; a golden set → dataset version and normalizer) and downstream ("used by": golden sets and eval
records for a normalizer; projects, gates and evals for a golden set; projects that adopted a model version, evals of
it). The Lineage panel draws the same answer.

As built (phase 3, stream L; `internal/lineage`): the walk starts from any registry version, source, run, checkpoint,
mix or pipeline run (`direction`, `depth` 1–10, `limit`), edges point from what was used to what used it with a
`relation`, projects are end points, and nodes in projects the caller cannot see are counted in `hidden`. Registry
versions link by one convention: every entity id (`<prefix>_<uuid>`) a payload names, at any depth, is upstream of the
version, the field path being the relation (`datasetVersionId`, `lineage.runId`), and the same index (migration 0025,
`entity_refs_in`) answers "used by" from the other end — new kinds need no lineage code. Project work in tables
(runs, checkpoints, mixes, pipeline runs) links through its columns; a domain with its own tables adds a
`lineage.Source` to the server's graph. Evals have one (stream R): an eval is built from its subject, baseline,
golden sets, noise banks and its cells' records; a record from its golden set, normalizer and model (a checkpoint or
model version by weights hash, or the base model version).

### Deployment entities (phase 5)

_Specified 2026-10-05, before phase 5 starts (R30, R31, R33, R46; plan `docs/review/2026-10-05-phase-5-plan.md`).
Spike E1 (`docs/spikes/E1-onnx-triton.md`) replaced A3's first numbers. The shapes below are the working contract:
each wave-1 stream adds its operations and schemas to `api/openapi.yaml` under its marker, then runs `make gen`. Model
exports, parity and benchmarks are built (stream D1, migration 0048: `model_exports` and `model_export_checks`);
deployments, their promotion checks and nightly shadow replay are built (stream D4, migration 0051: `deployments`,
`deployment_steps`, `shadow_replays`, `shadow_calls`, `shadow_segments`; "Deployments as built (stream D4)" below)._

A model version never changes. What phase 5 adds attaches to it, as reference alignments attach to a golden set:
exports, their parity and benchmark reports, and the deployments and promotions built on them. IDs: model exports
`mex_`, deployment targets `dtg_`, deployments `dep_`, promotion records `prm_`. Events: `entity.model.{id}`
(`model.exported`, `model.parity_checked`, `model.benchmarked`), `entity.deployment_target.{id}`, `deploy.{id}`
(`deployment.created`, `deployment.stage_changed`, `promotion.pending`, `promotion.confirmed`,
`promotion.withdrawn`), `shadow.{deployment}` (`shadow.replayed` with divergence samples), `approvals`.

**Model exports.** One row per (model version, latency profile, deployable format), table `model_exports`:

| Field | Value |
| --- | --- |
| `modelVersionId`, `profile`, `format` | The key; `format` comes from the family's `export` role (03 "Export, parity and benchmark"), e.g. `triton-tensorrt-cache-aware` |
| `deployableHash` | The `deployable` artifact (`cadence.deployable/1`); the same weights and export kind give the same hash, so asking again returns the row |
| `state` | `exporting`, `exported`, `failed` (with the pipeline run's error) |
| `parity` | The newest parity check: `{state: pending|passed|failed, pipelineRunId, reportHash, werDelta, identicalShare, disagreement, compared: tokens|text, utterances, reasons, sample: {goldenSetVersionId, datasetHash, utterances, selection}, servedBy: {targetId, serverVersion}, error, checkedAt}` (a row of `model_export_checks`) |
| `benchmarks[]` | `{state: running|done|failed, pipelineRunId, reportHash, targetId, stagingTargetId, serverVersion, cardClass, streams, levels, p95ChunkLatencyMs, p95TimeToFinalMs, budgetMs, maxStreamsWithinBudget, contended, verdict: passed|failed|inconclusive, error}`, newest first (`inconclusive`: a contended level at the target concurrency, 06 "Staging serving") |

`models.get` returns a model version with its `exports[]`; the Model document draws them.

| Operation | Path | What it does |
| --- | --- | --- |
| `models.export` | `POST /projects/{p}/models:export` `{version, profiles?, format?}` | Runs the export pipeline for each profile (default `deploy.export_profiles`: the primary profile) in the family's runtime; `dryRun` answers the plan and estimate; `202` with the pipeline run. Job kind `export` |
| `models.parity` | `POST /projects/{p}/models:parity` `{version, profile?, goldenSet?}` | Decodes the fixed parity sample with the family's reference decoder and through the staging target's server, then compares (R31); `202` with the pipeline run. Job kind `eval`. Needs an `exported` export of the profile and a staging target that is up (`serving-unavailable`) |
| `models.benchmark` | `POST /projects/{p}/models:benchmark` `{version, profile?, target?, streams?}` | Streams audio at real-time pace through the staging server at each concurrency level and reports latency and throughput; the verdict is taken at the concurrency and primary profile of `target` (a delivery target; default `deploy.target_concurrency` and the export's profile); `202` with the pipeline run. Job kind `benchmark`, which takes the card alone (06 "Staging serving") |

The paths follow `models.register` and `goldenSets.align`: a project-level collection action whose body names the
version (R1 sketched `/registry/models/{id}:export`), because the project carries the GPU budget, the queue priority
and the approval scope. All three are `gpu-spend` commands under the usual policy; none needs a person within
budget. Refusals: `export-missing` (parity or benchmark before an export), `serving-unavailable`, `target-does-not-serve`.

**Deployment targets (R46).** Instance-wide like mounts and compute (`/deployment-targets`, registry scope, no
project). The payload:

```yaml
name: era-production
kind: delivery                       # staging: Cadence reaches it; delivery: only a person's delivery script does
serves:                              # what promotion checks (R46)
  - family: nemo.fastconformer-rnnt.cache-aware   # a model-family collection name, compared as data
    formats: [triton-tensorrt-cache-aware]
    profiles: [80ms]                 # the first is the primary profile the latency budget is checked at
server: { kind: triton, version: "26.07" }
endpoint: http://triton:8000         # staging only; a delivery target never has one
repositoryPath: /opt/era/triton/models   # delivery: where the script installs model directories
slots: [asr-he-il]                   # the model names the production pipeline calls, one per locale or use
concurrency: 32                      # streams the latency budget holds at (R31; Эра's peak, placeholder)
cardClass: blackwell-96gb            # benchmarks on another card class are shown as such, not refused
boost: { static: true, dynamic: true, maxTermsPerCall: 100 }   # R32 · confirm
```

- `deploymentTargets.list|get|new|edit|archive`. `new` and `edit` are the admin's and an approval for everyone at
  registry scope (preset rule `deployment-targets`, as `mount-registration`), because a target names production.
  Config that a promotion record names (`serves`, `server`, `repositoryPath`, `slots`) changes only by `edit`, which
  appends a `target-changed` record to the target's chain.
- The staging target `staging` is seeded at first start from `defaults.yaml` `serving.staging_target` (kind
  `staging`, the compose service's endpoint); delivery targets are created by the admin.
- A model version can be promoted only to a target whose `serves` lists its family, the export's format and the
  profile (`target-does-not-serve`, 422). Families another target serves can still be registered, evaluated and used
  as oracle or pseudo-labeller (R46).

**Deployments.** Project work (`/projects/{p}/deployments`, `/deployments/{id}`):

| Field | Value |
| --- | --- |
| `modelVersionId`, `exportId`, `profile` | What is deployed |
| `targetId`, `slot` | Where: the staging target for `shadow`; a delivery target's slot for `canary` and `production` |
| `stage` | `shadow`, `canary`, `production`, `retired` |
| `state` | `active`, `pending-delivery` (a promotion waits for its receipt), `rolled-back`, `retired` |
| `trafficShare` | Canary only (`deploy.canary_share`, 0.05) |
| `decoding` | `{boostLists: [{locale, domain, hash, weight}]}`: static lists ship as decoding configuration; a list change is a config-only promotion, not a new model version (03 "Hot words") |
| `shadow` | `{hours, utterances, nights, divergence: {wer, ci}, against: <model version or base model>, lastReplayAt}` |
| `promotions[]` | The records that moved it |

| Operation | Path | What it does |
| --- | --- | --- |
| `deployments.new` | `POST /projects/{p}/deployments` `{version, profile?, replay: {mount, path?}}` | A shadow deployment on the staging target. Allowed for agents (Guardrails); nightly replays start at `deploy.shadow_replay_at` (06 "Staging serving") |
| `deployments.list`, `deployments.get` | `GET /projects/{p}/deployments`, `GET /deployments/{id}` | With shadow progress, stage history and records |
| `deployments.promote` | `POST /deployments/{id}:promote` `{stage: canary|production, target, slot, trafficShare?, decoding?, reason}` | Checks (below), then an approval (an agent's call answers the approval id; a person's call is decided by the confirm modal), then a signed promotion record and its delivery bundle; the deployment waits in `pending-delivery` until `promotions.verify` |
| `deployments.rollback` | `POST /deployments/{id}:rollback` `{reason}` | Same path: approval, a signed rollback record and a small script that routes the slot back to the previous version, which stayed loaded |
| `promotions.list` | `GET /deployment-targets/{id}/promotions` (`project`, `slot` filters) | The target's chain, oldest first, each record re-verified on read (`verified`) |
| `promotions.get` | `GET /promotions/{id}` | The record, its signature and key id, the script's text and, for people only, a signed download link to the bundle (as media links, R25) |
| `promotions.verify` | `POST /promotions/{id}:verify` `{receipt}` | A person pastes the receipt line the script printed; Cadence checks it and appends a confirmation record. People only (rule `delivery-is-for-people`) |

Checks before an approval is even asked (each a 422 with its help page):

| Stage | Requires |
| --- | --- |
| `canary` | The target serves the family, format and profile (`target-does-not-serve`); the export's parity passed (`parity-failed`); a benchmark of the export passed at the target's concurrency and profile (`latency-budget-exceeded`, or `benchmark-missing`); the deployment's shadow reached `deploy.shadow_min_hours` (20 h; `shadow-volume-short`); no other canary or pending promotion on the slot (`conflict`) |
| `production` | The same model version is the slot's confirmed canary (`canary-required`) |
| rollback | The slot has a confirmed earlier production version still loaded (`rollback-unavailable`) |

**Deployments as built (stream D4, 2026-10-05).** `internal/deployments`; what differs from the tables above:

- `deployments.new` takes `{version, profile?, format?, replay: {mount, path?, source, language?, channelRoles?},
  against?}`. `replay.source` is required: the registered source the recordings belong to ("no licence, no
  ingest"; `source-unlicensed`). `language` defaults to the language the model's gating eval decoded its first target
  golden set in, `channelRoles` to `[caller, bot]` (a call's sidecar wins). `against` is the comparison model
  (default: the model of the project's production deployment, else `@baseline`, else the project's default base
  model; never the model itself). One live shadow per export (`conflict`).
- The deployment row: `stage`, `state`, `targetId`, `slot`, `modelName` (the versioned model name on the delivery
  target, `<slot>-<version with dashes>`, e.g. `asr-he-il-2026-11-02-ab12cd`), `trafficShare`, `decoding`,
  `replay`, `shadow` (running totals), `pending` (the record waiting for its receipt) and `history[]` (steps
  `created`, `promotion`, `rollback`, `confirmation`, `withdrawal`, `retired`, `restored`, each with its record).
  Shadow totals change no revision, so a promotion approved while a night ran still matches its `If-Match`.
- `deployments.promote` `{stage, target?, slot?, trafficShare?, decoding?: {boostLists: [{locale, domain, ref?,
  weight?}]}, reason}`; target and slot default to the deployment's own once it is on a delivery target. Promoting to
  the stage the deployment already holds on its slot with a new decoding or share is **config-only**: the record names
  the same deployable, the bundle ships no model (D3's `installed`) and no smoke set, and the receipt reads `0/0`.
- The checks are those of the table plus **engine**: the export's `serving.server.version` and `serving.engine.
  cardClass` must equal the target's `server.version` and `cardClass` when both are known (`target-does-not-serve`:
  the engine does not load on another GPU architecture or server release; a card class missing on either side is a
  warning). A benchmark measured on another card class than the target's is a warning. A benchmark counts at the
  target's concurrency only (`streams` equal to it); an `inconclusive` newest one answers `benchmark-missing`.
  `dryRun=true` answers every check (`checks[]`: `target-serves`, `engine`, `parity`, `benchmark`, `shadow`,
  `slot-free`, `canary`, `rollback`, `not-pending`, each `passed`, `failed` with its problem type, `warning` or
  `skipped`) and the record's body; the real call refuses with the first failing check's problem before the policy.
- The approval is for everyone (preset rule `deployments`, `everyone: true`): a person's own call waits too, and the
  Model document's confirm modal approves it at once (`approvals.approve`), so the signed record always names an
  approver. The approved replay appends the record (`promotions.Append`) and queues the bundle (`delivery.Build`) in
  the deciding transaction; the deployment is `pending-delivery` until `promotions.verify` (`deployments.Confirm`
  moves it) or the withdrawal (`deployments.Withdraw` returns it).
- Record bodies: a promotion carries `stage`, `trafficShare` (canary only), `model {versionId, version: model/<name>@<
  version>, weightsHash, family}`, `deployable {hash, format, profile, modelName, manifestSha256, files}`, `decoding
  {boostLists: [{locale, domain, sha256, weight}]}`, `previous {versionId, modelName}` (the slot's production) and
  `evidence {gate, parity, benchmark, shadow}`. A rollback names the restored version's `model` and `deployable`,
  `stage: production` and `replaces {versionId, modelName}`; its bundle installs nothing (the version stayed loaded).
- On confirmation: a canary takes the slot at its share; a production retires the slot's earlier production
  deployment (`retired`, which stays loaded and is what a rollback restores); a rollback leaves the deployment
  `retired`/`rolled-back` and the restored deployment `production`/`active`. A canary's rollback restores the
  slot's production deployment (no state change).
- Boost lists are read from `lang/<locale>/boost/<domain>.txt` at `ref` (default `main`) and stored as `boost_list`
  artifacts; the bundle's `decoding/<locale>.<domain>.json` files come from the record's step.
- `transcriptions.Deployments` resolves a deployment for manual tests: its export's deployable through the
  deployment's staging target while it is a shadow, else through `serving.default_target`.
- Shadow replays are their own entity (`srp_…`): `shadowReplays.list` (`GET /deployments/{id}/shadow-
  replays`), `shadowReplays.get` (`GET /shadow-replays/{id}`, with the night's worst segments) and
  `shadowReplays.new` (`POST /deployments/{id}/shadow-replays`, a replay now; `gpu-spend`). The nightly replay
  (03 "Shadow replay") runs as the system.

**Promotion records (R33).** A record is JSON in canonical form (RFC 8785, JCS); its hash is the SHA-256 of that
form (hex); its signature is Ed25519 over the 32 hash bytes. Records are append-only (`promotion_records`; a trigger
refuses UPDATE and DELETE) and chained per delivery target: `seq` from 1, `prevHash` = the previous record's hash.

```yaml
schema: cadence.promotion/1
kind: promotion            # genesis | promotion | rollback | confirmation | withdrawal | target-changed | key-rotation
id: prm_…
target: { id: dtg_…, name: era-production, seq: 7, prevHash: "…" }
slot: asr-he-il
stage: canary              # canary | production (promotion); the stage restored (rollback)
trafficShare: 0.05
project: { id: prj_…, slug: hebrew }
model: { versionId: ver_…, version: "model/hebrew@2026-11-02.ab12cd", weightsHash: "b3:…", family: "…" }
deployable: { hash: "b3:…", format: triton-tensorrt-cache-aware, profile: 80ms, modelName: asr-he-il-2026-11-02-ab12cd,
              manifestSha256: "…", files: [{ path, sha256, bytes }] }
decoding: { boostLists: [{ locale, domain, sha256, weight }] }
previous: { versionId: ver_…, modelName: … }   # what stays loaded for an instant rollback
evidence: { gate: { evalId, verdict, gatesSha }, parity: { reportHash, werDelta, identicalShare },
            benchmark: { reportHash, streams, p95TimeToFinalMs, budgetMs, cardClass }, shadow: { hours, divergence } }
requestedBy: { actor: agent|user, id, sessionId? }
approval: { id: apr_…, approver: { id: usr_…, name }, decidedAt }
reason: "…"
createdAt: "2026-11-03T09:12:44Z"
key: { id: "ed25519:3f9a…", alg: Ed25519 }
# stored beside the canonical body: hash, signature (base64)
```

- **Instance key.** An Ed25519 key pair generated when the first delivery target is created. The private key is a
  secret of kind `signing` (`instance-signing-key`), sealed with the master key and backed up like every sealed
  secret; it never appears in an API answer, a job, a log or an agent context. The public key and its id (the first
  16 bytes of its SHA-256) are in every record, in `promotions.get` and in Settings → Deployment targets.
  `cadence admin rotate-signing-key` appends a `key-rotation` record, signed by the old key, that names the new one.
- **Genesis.** Creating a delivery target appends `genesis` (the target's payload and the public key).
- **Pending and withdrawal.** A promotion or rollback record leaves its deployment `pending-delivery`. Without a
  receipt within `deploy.delivery_pending_days` (7) the system appends `withdrawal` and the deployment returns to its
  stage before; promoting again makes a new record and bundle.
- **Delivery bundle.** A `delivery` artifact (`cadence.delivery/1`, a directory) per promotion: `deliver.sh`
  (POSIX sh; needs `sha256sum`, OpenSSL ≥ 3.0 and `curl` to the Triton on localhost), `record.json` (the canonical
  body), `record.sig`, `instance.pub`, `models/<modelName>/…` (the deployable's model directory under its versioned
  name; absent for a rollback or a config-only change), `decoding/` (boost lists as JSON) and `smoke/` (≤ 20
  utterances of the parity sample as 16 kHz WAV with the text the staging server wrote for them). The person copies it
  to the production host; Cadence never reaches that host (non-negotiable 8).
- **What the script does, in order, stopping at the first failure without printing a receipt.**
  1. The record's key must equal the pin `/etc/cadence/instance.pub`, installed once by a person from Settings →
     Deployment targets; with no pin the script stops and says how to install one.
  2. It checks `record.sig` over the SHA-256 of `record.json`, then every file against the record's `files`.
  3. It copies `models/<modelName>` under `repositoryPath`, loads it through Triton's model-control API and leaves
     the previous version loaded.
  4. It streams the smoke utterances through the new model; at least `deploy.parity_min_identical_share` of them must
     match the staging text, or it unloads the new model and exits non-zero.
  5. It routes the slot's traffic (the canary share, or all of it) through Эра's routing mechanism (R32 · confirm).
  6. It prints one line, `CADENCE-RECEIPT 1 <recordHash> <servedSha256> <smoke ok/total> <hostname> <UTC time>`.
     `servedSha256` is the SHA-256 of the sorted `(path, sha256)` list of the model directory as installed.
- **Confirmation.** `promotions.verify` accepts the receipt when its record is the slot's pending one, the record
  hash matches, `servedSha256` equals the record's `manifestSha256` and the smoke passed. It then appends a signed
  `confirmation` record (the receipt verbatim and the confirming person) and moves the deployment to its stage.
  Anything else answers `promotion-receipt-mismatch` and changes nothing.
- **What "signed" guarantees, and what it does not.** The production host runs only records the pinned instance key
  signed; the files it installed are the ones the approver saw; the person who pasted the receipt had the script of
  that record and it ran to the end. The chain makes a removed or edited record visible. A receipt does not prove
  which host ran the script: that rests on the person, who is named in the confirmation.
- A config-only promotion (a changed boost list, same model version) is a `promotion` record with the same
  `deployable.hash`, no `models/` in the bundle and the new `decoding`.

## Storage and mounts

Audio lives where it already is — local disk, a network share or object storage — and Cadence indexes it by content hash; only what a job needs is materialised onto the fast local cache, and only for as long as it is pinned.

### Content store (phase 2)

- Every step input and output is an artifact in one content-addressed store: `b3:<64 hex>` (BLAKE3-256), a file at
  `cas/b3/<ab>/<hash>`, a directory as a manifest blob `{files: [{path, hash, size}]}` whose hash is the artifact's
  (06 "Artifacts, metrics and logs").
- Before mounts exist (phases 2–3) the store on the staging host's NVMe (`CADENCE_CAS_DIR`) is the only tier, shared by
  volume with the worker; imports, shards, checkpoints and reports all land there. From phase 4 a mount is a further
  tier behind the same hash, and an utterance's URI names where its bytes also live; the store stays the only cache.
- The `artifacts` table is the index (hash, type, size, metadata, producing step); lineage and "used by" follow the
  hashes. Metrics are rows in Postgres (`metric_points`) and job logs NDJSON files under `$CADENCE_DATA_DIR/job-logs/`
  kept 14 days — neither lives in the store. Backups mirror the store once per blob. Eviction is narrow: eval artifacts
  by age, training states, from phase 4 dataset shards with a mount copy ("The cache and materialisation" below), and
  spectrogram tile pyramids the control plane built, by last view (`media.tiles_retention_days`, 14; 06 "Media"): they
  are re-derivable but have no mount copy, so the cache sweep never takes them and an age rule like the eval
  artifacts' does (phase 4 tail).
- Compose puts the store on the `artifacts` volume (`/var/lib/cadence/cas`), mounted by the control plane and every
  worker service; per-lease scratch (`/var/lib/cadence/scratch`) sits on the same file system so inputs are hard
  links, not copies.

### Mounts (phase 4)

A mount (`mnt_`, registry data, `internal/mounts`, migration 0033) is a named storage location; its name is the
authority of every URI on it (`mount://<name>/<path>[#t=<start>,<end>][&ch=<n>]`, "Data in full" above).

| Kind | What it is | Access |
| --- | --- | --- |
| `local` | A path on the host, bound into the control plane and every worker service at the same path | Read-only by default; writable when registered so |
| `nfs`, `smb` | A share the OS mounted at a path — the same driver as `local` | As `local` |
| `s3` | An S3-compatible bucket (MinIO included): endpoint, region, `bucket[/prefix]`, a secret holding `<accessKeyId>:<secretAccessKey>`; read with SigV4, path-style (ListObjectsV2, GetObject) | Read |
| `hf` | A Hugging Face Hub repository (`datasets/<org>/<name>` or `<org>/<model>`) pinned to a commit SHA, an optional token secret for gated repositories | Read |

- **Operations.** `mounts.list|get`; `mounts.new` (`POST /mounts`) registers one — an approval for everyone (preset
  rule `mount-registration`, registry scope, the admin decides); `mounts.scan` (202, a job in the control plane:
  files, bytes, top-level entries such as `<source>/<revision>`, content-store blobs found under `…/b3/<ab>/<hash>`,
  stopping at `storage.mount_scan_max_files`); `mounts.verify` (202, the health check: `mount_check@1` runs on a worker
  — reachable, free space, a write probe on a writable mount, a throughput sample of `storage.mount_check_sample_mb`;
  also every `storage.mount_check_hours`). Events on `mount.{id}`: `mount.created`, `mount.health`, `mount.scanned`.
- **Config is immutable** (revision stays 1): a new location is a new mount. A step job that names a mount whose last
  health check failed fails at once (`mount-unhealthy`); health is per mount, not per host.
- **What a mount may reach** (audit F1). A path mount's root is never one of Cadence's own directories (data, content
  store, backups, logs, secrets; `/var/lib/cadence`) or the system's (`/proc`, `/sys`, `/dev`, `/etc`, `/boot`,
  `/root`, `/run`), nor inside or around one. Readers follow no link out of the root: the control plane resolves links
  before opening (`mounts.InRoot`: scans, materialisation, the audio of a window) and the worker refuses a URI whose
  file resolves outside the root. An `s3` endpoint is `https://host[:port]`; plain http only to a loopback host, or
  anywhere with `CADENCE_MOUNTS_ALLOW_HTTP=1` (development).
- **Leases** carry the mounts a step reads (`lease.mounts: [{name, kind, root, readOnly, endpoint?, region?,
  revision?, credentialsEnv?}]`); a mount's credentials arrive in `CADENCE_MOUNT_<NAME>_CREDENTIALS` only for a step
  that reads the mount — its params or inputs' metadata name it, or a step that produced one of its inputs (back
  through their inputs) named it — and for the mount's own health check (06 "Worker protocol").
- **The stand's mounts.** `corpora` (`local`, read-only) holds the corpora the fetch scripts write once
  (`<source>/<revision>/…`, `scripts/corpora/`); compose binds `${CADENCE_CORPORA_DIR:-corpora}` (on the stand
  `/cadence/corpora`) to `/mnt/corpora` read-only in the control plane and every worker, so the mount is registered
  with root **`/mnt/corpora`**, the container path. `exports` (`local`, writable) binds `${CADENCE_EXPORTS_DIR:-exports}`
  to `/mnt/exports`: the target of exports (`storage.export_mount`) and the backup mirror's mount option
  (`backups.mirror_mount`).
- The control plane reads a mount only to scan it, to copy blobs back from it (materialisation) and to list what a
  step will read (below); workers read it for ingest and freeze.
- **Mount content in step hashes** (owner decision 2026-10-04, `mounts.Fingerprinter`, migration 0044). A step whose
  parameters name `mount://` URIs (`sdp_ingest`'s and `dataset_import`'s `path`) gets a fingerprint folded into its
  input hash when it becomes ready: SHA-256 over each URI's mount (name, kind, root, revision) and the sorted list of
  (relative path, size, stamp) of the files under it — the stamp is a path mount's modification time, an S3 object's
  ETag, a Hub file's LFS sha256 or blob id. Files the step's `exclude` globs (fnmatch) leave out are not listed; the
  `pattern` is not applied, since a step also reads sidecars beside the files it matches (a superset never reuses a
  step whose input changed). It is a listing, never the bytes, capped at `storage.mount_scan_max_files`; a listing
  that fails or exceeds the cap gives the step a stamp of its own, so it is not reused. Export steps (job kind
  `export`) write to their mount and are not fingerprinted. The fingerprint is kept on the step
  (`pipeline_steps.mount_fingerprint`); `fresh: true` still re-runs everything.

### The cache and materialisation (phase 4)

The cache **is** the content store (`internal/cache`): no second tier on the worker. A freeze writes its shards there
(the first copy, accounted to the freezing project), a mount copy of a blob is recorded in `blob_copies` (found by a
scan, written by an export or by the backup mirror), and eviction frees only what can be brought back.

1. **Index in place.** Ingest streams files from the mount and writes only the segment manifest; nothing is copied
   ("Data in full" above).
2. **Freeze** cuts the segments into the content store; `datasets.freeze` refuses a project past
   `storage.project_quota_gb` (200; `storage-quota-exceeded`), counting the cached bytes of the dataset versions it
   froze.
3. **Pinning.** A dataset version is pinned while a waiting or running step job or a running pipeline names it, while
   a model version that holds an alias was trained on it, or while a golden set is built on it.
4. **Eviction.** A scan records a file named like a blob (`…/b3/<ab>/<hash>`) as its copy only when its size is the
   blob's (`inventory.blobsMismatched` counts the others, and an earlier record of them goes). Before an eviction
   deletes a blob it reads a copy back from a mount and checks its hash; a copy that is gone or holds other bytes is
   dropped from `blob_copies`, and a version with a blob no copy verifies (an unreachable mount) stays cached (the job
   result says why). A version is evictable when nothing pins it and every shard blob has a mount copy or is listed by
   another live artifact; blobs on no mount (imported audio) are never evicted, and re-derivable shards without a mount
   copy are not evictable yet. Every `storage.cache_sweep_minutes` (15), above `storage.cache_high_water_pct` (85 %),
   the sweep evicts — over-quota projects first, then least recently used — down to `storage.cache_low_water_pct`
   (70 %), as the system actor and without an approval. `datasets.evict` (`POST /registry/datasets:evict`, body
   `{versionId}`) does it for one version (202; the dry run lists what blocks it). The evicted artifact keeps its row
   and manifest, so lineage resolves; events `artifact.evicted`, `artifact.restored` on `entity.artifact.{hash}`.
5. **Materialisation.** `datasets.materialize` (`POST /registry/datasets:materialize`, body `{versionId}`) copies
   evicted shards back from their mount copies, verifying each by hash, resumable by shard (202; the dry run says how
   much and from which mounts, and what is missing). A run on an evicted version is refused before it is queued (`artifact-missing`, naming `datasets.materialize`); its dry run warns `needs-materialize` with this plan.
6. **`storage.get`** answers the cache: use against the water marks, cached and evictable bytes, per-project quotas,
   and every cached dataset version with its state (`cached | evicted`), pins, mount copies and last use.

Windows: Storage (tool, Ops workspace: mounts, health, cache use, quotas, pinned and evictable versions); Dataset
version shows where each shard lives. Agent tools: `mounts.list|get|scan|verify`, `storage.get`,
`datasets.materialize`, `datasets.evict`; `mounts.new` waits for the admin. Help: `docs/help/panels/storage.md`.

NeMo's Lhotse dataloader already reads tarred data from AIStore-style object stores, and Lhotse recordings accept URL sources, so remote reads for evaluation need no custom loader; training stays on local disk for throughput.
