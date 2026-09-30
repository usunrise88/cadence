# Cadence spec — Domain model, projects, registry, storage

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Domain model

Forty-six entities across the five blocks, the project layer and the registry (two added by R40–R41); everything a model was built from is reachable from the model by lineage links, and nothing referenced by a promoted model can be deleted.

| Entity | Block | What it is | Links to |
| --- | --- | --- | --- |
| Source | Data | A corpus with licence, languages, kind (public, production, synthetic) | Utterances |
| Utterance | Data | One audio segment in the content store: duration, language, speaker, sample rate | Source, Transcripts |
| Transcript | Data | Text for an utterance with origin (human, pseudo-label, model id) and confidence | Utterance |
| Recipe | Data, Training, Eval | A versioned file in the recipes repository: SDP config, mix, training YAML, eval-set definition | Commit SHA |
| Dataset version | Data | Immutable, fingerprinted selection with splits, statistics and lineage | Utterances, Recipe |
| Base model | Training | Upstream checkpoint: Hugging Face repo, revision, licence | — |
| Mix | Training | Groups, weights, temperature and replay share over dataset versions | Dataset versions, Recipe |
| Run | Training | One optimisation stage: init checkpoint, mix, recipe, card, step budget, seed, container image digest, status | Base model or Checkpoint, Mix, Recipe |
| Job | All | A unit of work on a card: command, workdir, log, resources, state | Run, Eval run, Export |
| Checkpoint | Training | Weights at a step with validation WER | Run |
| Golden set | Eval | Frozen held-out test set per language and domain, never trainable | Dataset version, Normalizer |
| Normalizer | Eval | Versioned text normalisation used for scoring one language | — |
| Eval run | Eval | Checkpoint × golden sets × latency settings | Checkpoint, Golden sets |
| Eval result | Eval | Per-cell WER, CER, substitutions, deletions, insertions, plus per-utterance rows | Eval run |
| Gate | Eval | Thresholds and allowed regressions per language, including replay languages | Golden sets |
| Model version | Deploy | A registered checkpoint with its artifacts (`.nemo`, ONNX, GGUF, Triton repository) and gate verdict | Checkpoint, Eval run |
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
| Compute | Cross-cutting (registry) | A host and its cards: memory cap per card, allowed job kinds (training, eval, shadow, export), health; the queue schedules against it | Jobs, Mounts reachable from the host |
| Secret | Cross-cutting (registry) | A named credential (Hugging Face, NGC, GitHub, S3, judge API): name, kind, where it lives; the value never enters the database or an agent context | Mounts, project repositories, jobs |
| Eval record | Eval (registry) | Cached result for one model version × golden set version × normalizer version × latency setting; project eval runs reuse it and compute only missing cells; the unit an Eval run is assembled from | Model version, Golden set, Normalizer |
| Saved search | Cross-cutting | A named query in the qualifier language, per user per project; appears as a Library view and in the palette | — |
| Help article | Cross-cutting (registry, bundled) | Versioned markdown shipped with the app; addressed by slug from panel manifests, step schemas, error types and skills | Panels, Step kinds, error types |
| Step kind | All (registry) | A versioned worker plugin: parameter schema with x-cadence defaults, artifact types consumed and produced, resource needs, help slug | Pipelines, Pipeline runs, Help article |
| Playbook | All (registry) | A pipeline chain with defaults filled in, a prefilled agent prompt and a cost estimate; a button on the Project home and an MCP tool | Pipeline templates, Agent sessions it starts |
| Template | Cross-cutting (registry) | Bundled, versioned configuration the bootstrap copies into a project: pipeline templates, instruction templates, permission presets, skills; projects.sync diffs a project against the current versions | Projects |
| Credential | Cross-cutting | A session, invitation, agent token or API key: kind, scope, hash, expiry, last use | Project or registry scope; Agent session; Annotation batch |
| Language pack | Data, Eval, Deploy (project) | A locale's directory in the project repository: normalizer, inverse normalisation, transliteration, LID config, boost lists, golden-set recipe | Commit SHA; Evals and Runs pin it |
| Annotation batch | Eval, Flywheel (project) | Items to annotate or triage, guidelines version, annotators, double-annotation sample, agreement, adjudication state | Utterances, Credential (invitation), Golden set it freezes |
| Experiment | Training (project) | A question, a fixed mix and base, the runs that answer it, an optional sweep definition with a GPU-hour cap, the best run | Runs, Mix, Base model |
| Augmentation profile | Training, Eval (project) | A versioned file of transforms with probabilities and ranges; telephony by default | Commit SHA; Runs, Eval runs; Noise bank |
| Noise bank | Data (registry) | Non-speech segments mined from own recordings plus licensed public noise sets, versioned like a dataset | Sources, Augmentation profiles |
| Notification rule | Cross-cutting (registry) | Event class → channels and timing; quiet hours | — |
| Runtime | All (registry) | A worker container image pinned by digest, with the framework versions and the worker plugin it carries; each step kind names its runtime (R40) | Step kinds, Model families, Compute |
| Model family | Training, Eval, Deploy (registry) | Versioned descriptor of a framework and architecture: formats, capabilities, latency profiles, the step kinds for each role, defaults (R41) | Runtime; Base models, Checkpoints, Model versions |

## Projects

A project is the unit of work — "Hebrew telephony", "Western Balkans regional" — with its own recipes branch, worktrees, budgets, gates and workspaces; everything else is scoped to it, and sources can be shared across projects read-only.

| Aspect | Rule |
| --- | --- |
| Scope | Work entities carry a `projectId`; registry assets do not (see Registry). Work lives under `/projects/{p}/…`, assets under `/registry``/…`; MCP tokens and agent sessions are bound to one project but may read the whole registry |
| Recipes | One repository per project — a GitHub repository chosen in the wizard or the internal bare repository — with `main` as the project branch; each agent session gets a worktree; a person's edits from the UI commit to `main` directly |
| Base model | A project has a default base model and may adopt several; a run starts from the default or any adopted base model, and the choice is recorded in lineage |
| Golden sets and gates | Golden sets live in the registry; a project adopts the ones its gates use; thresholds and replay languages are per project |
| Budgets | GPU-hours per day, agent spend per day and queue priority are set per project; the queue interleaves projects by priority, one training slot per card still holds |
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
| Source, Utterance, Dataset version, Golden set, Normalizer, Base model, Model version, Mount, Step kind, Runtime, Model family, Pipeline template, Instruction template, Permission preset, Skill | Mix, Run, Checkpoint, Eval run and results, Gate, Deployment, Promotion, Production sample, Signal, Triage item, Correction batch (until packaged), Agent profile, Agent session, Schedule, Approval, workspace layouts |

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
- Adoption re-runs the leakage check: adopting a golden set is blocked if any dataset version the project has trained on overlaps it, until those datasets are re-frozen without the overlap.
- Production samples, once PII-redacted, are published as a registry Source per deployment target and month; any project adopts them like any other source, and retention applies to the source.
- Eval results are cached as registry Eval records keyed by model version, golden set version, normalizer version and latency; two projects evaluating the same model on the same set never compute it twice.
- Templates and skills: bootstrap copies them into the project; projects.sync later diffs the project against Cadence's current templates and skills and offers the update as a draft commit.
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

## Storage and mounts

Audio lives where it already is — local disk, a network share or object storage — and Cadence indexes it by content hash; only what a job needs is materialised onto the fast local cache, and only for as long as it is pinned.

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
