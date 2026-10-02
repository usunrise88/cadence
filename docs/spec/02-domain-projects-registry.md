# Cadence spec — Domain model, projects, registry, storage

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Domain model

Forty-seven entities across the five blocks, the project layer and the registry (three added by R15, R40–R41); everything a model was built from is reachable from the model by lineage links, and nothing referenced by a promoted model can be deleted.

| Entity | Block | What it is | Links to |
| --- | --- | --- | --- |
| Source | Data | A corpus with licence, languages, kind (public, production, synthetic) | Utterances |
| Utterance | Data | One audio segment in the content store, identified by the BLAKE3 hash of its audio file: duration, language, speaker, sample rate, channels | Source, Transcripts |
| Transcript | Data | Text for an utterance with origin (human, pseudo-label, model id) and confidence | Utterance |
| Recipe | Data, Training, Eval | A versioned file in the recipes repository: SDP config, mix, training YAML, eval-set definition | Commit SHA |
| Dataset version | Data | Immutable, fingerprinted selection with splits, statistics and lineage | Utterances, Recipe |
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
| Model version | Eval, Deploy (registry) | A checkpoint published by `models.register` once its eval passed the gate (registry kind `model`, collection `model/<name>`, R22): weights hash, family, base model, gate verdict with the gates SHA, lineage, model card; exports (ONNX, Triton repository) join in phase 5 | Checkpoint, Eval, Base model |
| Deployment | Deploy | A model version on a target at a stage: shadow, canary with traffic share, production | Model version |
| Promotion | Deploy | Signed record of who moved what to which stage, and why | Deployment, Approval |
| Production sample | Flywheel | An utterance captured from calls, PII-redacted, with the production hypothesis; published monthly into a registry Source | Deployment |
| Signal | Flywheel | Why a sample matters: model disagreement, low confidence, judge flag, operator correction | Production sample |
| Triage item | Flywheel | A sample in the review queue with candidate transcripts and consensus | Signal |
| Correction batch | Flywheel | Accepted triage items packaged as a source for the next dataset version | Triage items, Source |
| Agent session | Cross-cutting | One agent conversation: driver, model, worktree, status, spend, transcript | Commands it issued |
| Approval | Cross-cutting | A pending or decided request for a gated action, scoped to a project or to the registry | Command, actor |
| Project | Cross-cutting | Unit of work: locales, default base model, repository, adoptions and aliases, budgets, gates, agent profile, workspaces | Every project-scoped entity |
| Mount | Data | A storage location: kind, root, credentials reference, read-only flag, health | Utterance URIs, exports |
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
| Annotation batch | Eval, Flywheel (project) | Items to annotate or triage, guidelines version, annotators, double-annotation sample, agreement, adjudication state | Utterances, Credential (invitation), Golden set it freezes |
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
| Source, Utterance, Dataset version, Golden set, Normalizer, Base model, Model version, Eval record, Mount, Step kind, Runtime, Model family, Pipeline template, Playbook, Instruction template, Permission preset, Skill; artifacts in the content store | Mix, Run, Checkpoint, Eval and its cells, Gate (`gates.yaml`), Deployment, Promotion, Production sample, Signal, Triage item, Correction batch (until packaged), Agent profile, Agent session, Schedule, Approval, workspace layouts |

Rules:

- Collections and versions: a registry entry is a collection (`dataset/hebrew-calls`) of immutable versions named `YYYY-MM-DD.<sha>`; tags carry language, domain and licence. Versions never change; only aliases move.
- Aliases per project: `@train-current`, `@baseline`, `@production` point a project at a version; a pipeline references an alias, a run records the resolved version.
- Adoption: a project adopts a dataset version, golden set or base model by reference. Adoption checks licence and locale; the Mix editor lists only adopted or adoptable datasets.
- Lockfile: Cadence writes `data.lock` in the project repository listing every registry version the project depends on, so a checkout of the repository says exactly which data a run used.
- Publishing: registering a model version publishes it to the registry with its gating eval report; another project can adopt it as base model. Packaging a correction batch creates a registry Source with licence `internal`.
- Lineage both ways: a model version links to dataset versions, mix SHA, recipe SHA and base model; a dataset version links to its sources and pipeline run; every registry entry answers "used by".
- Storage: registry assets live in the content store and on mounts; a project repository holds recipes and the lockfile, never data.
- Deletion: a registry version referenced by anything cannot be deleted; otherwise admin-only soft delete.
- Events: work events carry `projectId`; registry events carry none, and a client shows them by reference ("used by this project").
- Adoption re-runs the leakage check: adopting a golden set is blocked (`golden-set-leakage`) if any dataset version the project has trained on overlaps it by fingerprint, until those datasets are re-frozen without the overlap.
- Production samples, once PII-redacted, are published as a registry Source per deployment target and month; any project adopts them like any other source, and retention applies to the source.
- Eval results are cached as registry Eval records keyed by model key, golden set version, normalizer version, decoding hash and scorer version ("Evaluation entities" below); two projects evaluating the same model on the same set never compute it twice.
- Templates and skills: bootstrap copies them into the project; projects.sync later diffs the project against Cadence's current templates and skills and offers the update as a draft commit.
- Runtimes, step kinds and model families are published by workers at start (06 "Worker protocol") and stored as registry versions named by their published JSON; no person registers them, and a runtime beyond the bundled ones needs `runtimes.new` with approval (deferred with the packs beyond NeMo).
- Step kinds: a version can be deprecated but not removed while any pipeline template or project pipeline pins it; deprecation shows as a warning on the Pipeline run and in the Recipe document.

Windows: Library gains a this-project / all filter and an Adopt action; registry documents (Source, Dataset version, Golden set, Model) show "Used by projects"; a Lineage tool draws the graph around the selection. API (phase 1, R1 names): versions per kind at `/registry/base-models`, `/registry/datasets`, `/registry/templates` (`<kind>.list|get`, each version with "used by"), collections at `/registry/collections` (`collections.list|get`), `registry.search`, `POST /projects/{p}:adopt`, `GET /projects/{p}/adoptions`, `GET|PUT /projects/{p}/aliases/{name}`. Agent tools: `registry.search`, `projects.adopt`, `aliases.set` and the list/get reads.

### Data entities as built (phase 2, R18)

The minimal data entities imports need; the full ingest path (mounts, pseudo-labels, triage) arrives in phase 4. All are registry data (no `projectId`; events on `entity.source.{id}`), in `internal/data`, migration `0014_data.sql`.

| Entity | As built |
| --- | --- |
| Source (`src_…`) | Unique name, licence (required), kind `public \| production \| synthetic`, languages, url, `trainingCleared` with who cleared it and when, `archived`, `rev`. Created by the first import that names it, **eval-only** (`trainingCleared: false`) until a person clears it. `sources.list\|get\|edit\|archive` at `/registry/sources`; `sources.get` adds utterance count, hours and the dataset versions built from it |
| Utterance (`utt_…`) | Identity = content hash, the BLAKE3 (`b3:…`) of its audio file in the content store; duration, language, speaker, sample rate, channels, bytes; owned by the first source that imported it. `utterances.list\|get` at `/registry/utterances` (filters source, dataset version and split, language; oldest first, `after` cursor; `get` by id or hash with fingerprints and memberships) |
| Transcript (`trn_…`) | Text with origin `human \| pseudo-label \| model:<id>` and optional confidence; one row per (utterance, origin, text) |
| Dataset membership | Which utterances a dataset version holds, the split (`train \| validation \| test`) and the transcript it uses; written once when the version is registered |
| Utterance fingerprint | Kind → value per utterance for leakage checks: `audio-b3` from every import; steps may add others (an acoustic fingerprint in phase 4) |

Rules:

- Clearing a source for training is a person's decision: an agent's `sources.edit` is gated (preset rule `registry-changes`, approval); `sources.archive` is the admin's (agents: `no-deletes`). An archived source takes no new imports and cannot be edited; its utterances and versions stay.
- A dataset version is **eval-only** when it was registered `evalOnly` (golden and replay test sets) or any of its sources is not cleared *at the time of asking*. Mixes (`mixes.new|edit|preview`, draft accept) and `runs.new` refuse eval-only versions with `eval-only-dataset` (422); clearing a source makes its versions trainable without a re-import. Collections of versions eval-only at registration carry the tag `eval-only`.
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

**Leakage and training exclusion.** A golden set's utterances never reach training, checked by utterance fingerprint
(`utterance_fingerprints`: `audio-b3` today, an acoustic fingerprint in phase 4):

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
| `scorer` | The scorer `kind@version` (`wer_score@1`) |

- The `scores` output hook writes the record in the transaction that marks the scoring step done (idempotent per
  artifact hash), and the eval links its cell to it. A cell whose key already has a record is never computed again,
  in any project.
- An eval's pipeline is generated per eval with only the missing cells (03 "The eval pipeline").
- As built (stream E, migration 0024): tables `eval_records` (global, `erc_`), `evals` (`evl_`) and `eval_cells`
  (`evc_`); the decoding hash is the sha256 of `{transcribe: kind@version, profile, <the kind's locale parameter>:
  locale, boost?: {list: <boost_list hash>, weight}, augment?: {kind, profile: <hash>, seed}}` (no `beam` yet: no
  transcribe kind takes one). A record whose `scores` artifact was evicted stays a record; the cell's delta then
  carries an `error` instead of numbers until the cell is recomputed (records are not protected from eviction yet,
  07 "Open questions"). Metrics beside WER (entity accuracy, latency to final) are kept in `eval_metrics` (`erm_`,
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

## Storage and mounts

Audio lives where it already is — local disk, a network share or object storage — and Cadence indexes it by content hash; only what a job needs is materialised onto the fast local cache, and only for as long as it is pinned.

### Content store (phase 2)

- Every step input and output is an artifact in one content-addressed store: `b3:<64 hex>` (BLAKE3-256), a file at
  `cas/b3/<ab>/<hash>`, a directory as a manifest blob `{files: [{path, hash, size}]}` whose hash is the artifact's
  (06 "Artifacts, metrics and logs").
- Before mounts exist (phases 2–3) the store on the staging host's NVMe (`CADENCE_CAS_DIR`) is the only tier, shared by
  volume with the worker; imports, shards, checkpoints and reports all land there. From phase 4 a mount is a further
  tier behind the same hash, and an utterance's URI names where its bytes also live.
- The `artifacts` table is the index (hash, type, size, metadata, producing step); lineage and "used by" follow the
  hashes. Metrics are rows in Postgres (`metric_points`) and job logs NDJSON files under `$CADENCE_DATA_DIR/job-logs/`
  kept 14 days — neither lives in the store. v1 deletes no blob; backups mirror the store once per blob.
- Compose puts the store on the `artifacts` volume (`/var/lib/cadence/cas`), mounted by the control plane and every
  worker service; per-lease scratch (`/var/lib/cadence/scratch`) sits on the same file system so inputs are hard
  links, not copies.

### Mounts

| Mount kind | Examples | Access |
| --- | --- | --- |
| Local path | NVMe on the control-plane or worker host | Read-write; the default cache lives here |
| Network share | NFS, SMB mounted by the OS at a path | Read-only by default; writable for exports if configured |
| Object storage | S3-compatible, including self-hosted MinIO | Read through a URI driver; writable for exports and archives |
| Hub | Hugging Face datasets and models | Read-only, pulled by revision |

- A mount is an entity: kind, root, credentials reference, read-only flag, default licence hint, and a health check the worker runs (reachable, free space, throughput sample).
- A utterance stores a URI (`mount://calls-nas/2026/09/…`) plus its content hash; the hash is the identity, so the same file on two mounts is one blob.
- Mounts must be reachable from the worker host; the control plane only needs them for indexing and previews.

### Materialisation

1. Ingest indexes in place: hashes, durations and metadata are computed by streaming the file, nothing is copied.
2. Freezing a dataset version writes Shar shards to the local cache — registry-owned, accounted to the freezing project's quota — or to a writable object-storage mount when the cache is the constraint.
3. A training job requires its shards to be `materialised` on the worker's local disk; `dryRun` reports how much must be copied and from where. Eval and preview jobs may read remotely.
4. Pinning: a dataset version referenced by a running job or a promoted model is pinned; unpinned shards are evicted least-recently-used when the cache passes its high-water mark.
5. Re-materialisation is a job (`datasets.materialize`) with progress events, resumable by shard.

Windows: Storage (tool: mounts, health, cache use, pinned versions); Dataset version shows where each shard lives. Agent tools: `mounts.list`, `mounts.scan`, `datasets.materialize`, `datasets.evict`. Gates: adding a mount or storing credentials needs a person; a job never starts on an unhealthy mount.

NeMo's Lhotse dataloader already reads tarred data from AIStore-style object stores, and Lhotse recordings accept URL sources, so remote reads for evaluation need no custom loader; training stays on local disk for throughput.
