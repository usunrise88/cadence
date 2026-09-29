# Cadence spec — Resolutions of open decisions and spec gaps (2026-09-29)

_Part of the Cadence specification v0.2. Source of truth: the "Resolutions 2026-09-29" tab of the Claude Doc "Cadence — spec v0.2"; this file mirrors it with repo-specific pointers. Decision-log rows for each group are in `00-overview.md`._

Resolves every **decide** and **spec** line in `ROADMAP.md` (sources: `docs/review/2026-09-29-spec-kickoff-review.md`
and the roadmap's dependency audit). Ported into the Doc and its decision log on 2026-09-29. Items marked **confirm** depend on facts outside engineering (legal, vendor terms, Эра) and need
the owner's answer; the proposal is the default to build against meanwhile.

Guiding choices, applied throughout:
- Server-side enforcement is the only enforcement. Agent-side permission files reduce prompts; token scope and the
  policy engine decide.
- Measure, don't guess: every estimate, threshold and default names the measurement that replaces it.
- Comparability beats convenience in evaluation: whatever scores a model must be shared, versioned and immutable.

---

## Contract and naming (phase 0)

**R1 · Naming rules** (review A1–A4, A6)
- Operation id = tool name = command id = `<entity>.<verb>`. The entity is declared once per resource in
  `x-cadence.entity`; the generator derives the three spellings: tool/opId entity = lowerCamel plural collection
  (`goldenSets`), path segment = kebab plural (`/golden-sets`), EntityKind and topic = snake singular (`golden_set`).
- HTTP shape ⇔ verb, enforced by the generator: `GET` item = `get`; `GET` collection = `list`; `GET` with `q` =
  `search`; `POST` collection = `new`; `PATCH` item = `edit`; `PUT` pointer (alias, workspace) = `set`; every other
  verb = `POST /{collection}/{id}:{verb}`, or `POST /{collection}:{verb}` for collection-level (`preview`, `calibrate`).
  No `DELETE`: removal is `archive` (soft).
- Paths: collections of work under `/projects/{p}/…`; items addressed by a globally unique id (`/runs/{id}`) with
  authorisation checked against the item's project; registry under `/registry/{kind}/…` (model versions included:
  `/registry/models/{id}:export`).
- Exempt from the vocabulary and from MCP: operations tagged `auth` (`login`, `logout`, invitations) and `me`
  (workspaces, saved views); they keep `<entity>.<verb>` ids but the generator doesn't publish them as tools.
- Renames that make the spec pass its own rule:

| Spec says | Becomes | Why |
| --- | --- | --- |
| `runs.create`, `evals.create`, `gates.update` | `runs.new`, `evals.new`, `gates.edit` | vocabulary form |
| `search.query` | `projects.search` | the agent token is project-bound; `scope:all` qualifier reaches the registry |
| `samples.query` | `samples.search` | read verb exists |
| `triage.next` | `triage.list` (ordered by priority, `limit`) | no new verb for "first of a list" |
| `boost.evaluate`, `augment.evaluate` | `evals.new` with `decoding` and `augmentation` axes | they *are* eval runs (see R17) |
| `agent-sessions:merge` / discard | `agentSessions.accept` / `agentSessions.revert` | same verbs as drafts; glossary already says "accepted or discarded as a whole" |
| `:bootstrap` | part of `projects.new` (returns `202 {jobId}`) | bootstrap is not a separate user action |
| `sendAgentMessage` | `agentMessages.new` | messages are a sub-collection |
| `PATCH /projects/{p}/baseline`, `baselines.set` | `aliases.set` on `baseline` (gated, R8) | one mechanism for pointers |
| `POST /projects/{p}:syncTemplates` | `projects.sync` | matches tool |
| `PATCH /triage/{id}` | `triage.accept|correct|reject` actions | one verb per operation |

- One verb is added to the vocabulary: `revoke` (credentials; irreversible; inline confirm). Security semantics are
  distinct enough from `archive` to deserve it.

---

## Agent host and security (phase 1)

**R2 · Session token never touches disk in the worktree** (B1)
Both drivers receive the Cadence MCP server through ACP `session/new` → `mcpServers` (HTTP transport with an
`Authorization` header). Rendered config files (`.claude/settings.json`, `opencode.json`) carry permissions only, no
MCP section. Session tokens get a fixed prefix (`cst_`); before each per-turn commit the host scans the staged diff
for credential prefixes and refuses the commit with an event. Verify the ACP header support in A1; fallback for
opencode is `{env:CADENCE_MCP_TOKEN}` substitution.

**R3 · Credential isolation** (B2, B5)
- The spec already allows "agent model credentials stay with each agent's own configuration"; what must never be
  reachable is Cadence's secrets and other projects' work.
- Don't mount the user's `~/.claude`. A dedicated `agent-credentials` volume holds only the Claude login (and
  opencode provider config); the host copies it into a per-session `CLAUDE_CONFIG_DIR`.
- Each session runs as its own Unix user from a uid pool inside the agent-host container; its worktree is `0700` to
  that uid. Other sessions' worktrees, other projects and the host's own files are unreadable. No `./recipes` mount:
  the host clones from the control plane.

**R4 · Network sandbox** (B4)
The agent-host container sits on an internal compose network whose only egress is an allowlisting proxy: Anthropic
API, the configured opencode providers, Hugging Face, PyPI, NGC, and the control plane (MCP). This enforces the
Guardrails line for both drivers at one place. Claude Code's own sandbox is enabled on top through the preset.

**R5 · Timeouts** (C3)
Three different clocks: *stuck turn* — no ACP update for 5 min during a turn → cancel the turn, pause, note;
*idle* — interactive session without a user message for 30 min → pause (cheap resume); *waiting approval* — never
pauses the session; the approval itself expires after 24 h → treated as deny, Telegram notified. Defaults live in
`defaults.yaml`.

**R6 · Claude authentication** (00 decision log) — **confirm**
Driver supports two auth modes per profile: subscription login and API key. Default: subscription for interactive
and read-only sessions; API key for playbook and scheduled sessions (no person present) until the subscription terms
are confirmed to cover them. Budgets count turns/tokens in both modes; money only for API key.

**R7 · Permission presets and policy engine** (spec gap)
- Preset = `control-plane/templates/presets/<name>.yaml`, Cadence-level rules in three classes:
  `tools` (by verb class: read, draft, spend, gated, forbidden), `files` (worktree only), `shell` (allow/ask/deny
  patterns). `guardrails-default` encodes the Guardrails table.
- Renderer maps a preset to Claude `permissions.allow/deny/ask` (+ sandbox settings) and opencode `permission`.
- Policy engine runs server-side on every command: input = actor, verb class, entity, scope, estimate vs remaining
  budget; output = `allow` | `approval` | `deny`. Rules are data (same YAML), evaluated in order, first match wins;
  default `deny`. Agent-side files never widen what the server allows.

**R8 · Reserved aliases** (B3)
`baseline` and `production` are reserved. `aliases.set baseline` is a gated command (returns `202 {approvalId}`);
`production` is read-only through `aliases.set` (`409` with a help type pointing to `deployments.promote`) and moves
only with a Promotion. Other aliases stay free.

**R9 · Secret storage** (spec gap)
Values live in an encrypted file store on the control-plane volume (`secretbox`, key from a compose secret, files
`0600`), never in Postgres; the database holds name, kind, scope, last use. `POST /secrets` writes there; bootstrap
and the worker lease (R14) read there; values never return through any endpoint. Replaces the "compose env file"
proposal, which the UI cannot write. `.env` keeps only bootstrap values (Postgres password, master key path).

**R10 · Internal git repository** (spec gap)
Bare repositories at `/var/lib/cadence/repos/<slug>.git`, served by the control plane over smart HTTP behind API-key
auth so a person can clone and push. The server keeps a working clone per project for UI commits to `main` and
session worktrees. Pushes to `main` from outside are accepted and appear as `recipe.{path}` events. GitHub projects:
same working clone, remote = GitHub, token from R9, push after each commit to `main`.

---

## Defaults, estimates, mixes (phases 1–2)

**R11 · `defaults.yaml`** (C1)
One file at `defaults/defaults.yaml`, embedded in the Go binary and packaged with the worker. Step schemas point to
keys (`x-cadence.defaultRef: training.peak_lr`) instead of repeating values; CI checks every `defaultRef` resolves
and every key is used. Pipeline templates omit parameters equal to their default, so a changed default reaches every
pipeline and a written value is a visible departure.

**R12 · Estimate model** (spec gap)
- Training: GPU-hours = steps × seconds/step for (base model, card, memory cap, precision, bucket config).
  Seconds/step is *measured* by `runs.calibrate` (OOMptimizer plus 50 timed steps) and cached; before any
  calibration a table in `defaults.yaml` answers, seeded from A3.
- Eval: audio hours × RTF for (model size, card, latency setting), from the benchmark cache, seeded from A3.
- Data: bytes to materialise from the shard index.
- Every estimate carries `basis: measured | table` and ±; approvals show it. Phase 1 ships the table path only.

**R13 · Mix** (C5, `mixes.edit`)
A mix is project work with revisions (`mixes.new`, `mixes.edit`, `mixes.preview`); "save as version" becomes "save".
A run records the mix revision plus a content hash of its resolved `input_cfg`, which is what reproducibility needs.
Mixes are draftable (agent edits land as drafts). No registry version for mixes.

---

## Worker, artifacts, playbooks (phase 2)

**R14 · Worker protocol** (spec gap)
Pull model, as CI runners do it. The worker long-polls `POST /worker/leases` with its capabilities (host, cards,
free memory, step kinds); the River job in the control plane is the orchestrator and marks the job leased. The lease
returns the step spec, input artifact URIs and secret env for the subprocess. The worker heartbeats every 10 s with
progress (reaped after 3 misses), streams logs (NDJSON chunks) and metric batches, and completes with an output
artifact manifest. New credential kind `worker` (`cwk_`), scoped to worker endpoints, one per host.

**R15 · Artifact store and telemetry storage** (spec gaps)
- Content-addressed store `cas/<blake3>` on the staging host's NVMe, shared by volume with the worker in v1 and
  reachable over HTTP by hash for remote workers later; an `artifacts` table records hash, type, size, producing step.
  Mounts (phase 4) become additional tiers behind the same hash.
- Metrics: a Postgres table (run, step, name, value, wall time); thousands of points per run need no TSDB.
- Logs: NDJSON files per job in the store, 14-day retention; tail through SSE; field search is job-scoped in v1,
  with `warn`+ lines indexed for global search.

**R16 · Playbooks and the phase-2 gate** (spec gap)
- Format: `templates/playbooks/<name>.yaml` — inputs (with `defaultRef`s), a chain of pipeline runs or commands,
  stop conditions (failed gate, budget), a prompt template, estimate = sum of step estimates.
- Fifth v1 playbook, **"Fine-tune from a dataset version"**: mix with replay → calibrate → train → register
  checkpoints → (from phase 3) eval matrix → gate. It is the phase-2 gate and the core that "Adapt a new language"
  later prefixes with ingest and freeze.

**R17 · Replay data** (spec gap) — ML rationale: replay exists to stop catastrophic forgetting in the other 39
locales, so it needs breadth, not volume.
- Replay corpus: a capped, licence-cleared sample per locale from public training splits (Common Voice, FLEURS,
  Granary where the locale is covered), ≈ 5 h per locale, frozen once as `dataset/replay-base` and adopted by every
  project. Licence of each source is checked at adoption (commercial use).
- Replay golden sets: FLEURS test per locale, ≤ 300 utterances each, evaluated at the primary latency only. The gate
  measures the *delta vs the base model*, so possible exposure of FLEURS in NVIDIA's training doesn't bias it.
- Cost: 39 × 300 utterances at primary latency is a small fraction of one eval run; the bootstrap CI keeps small
  sets honest.

**R18 · Minimal data entities for imports**
Imports register a Source with the licence the format carries, Utterances with content hashes and Transcripts with
origin; this is what fingerprint exclusion, per-utterance rows and Diff need from phase 2 on.

**R19 · Compute availability windows** (spec gap)
Compute gets per-kind availability windows. The queue starts a job only if its estimate fits before the window
closes; training checkpoints every 20 minutes and is paused at a window close, resuming from the last checkpoint.
This also makes long runs preemption-safe, which the shared staging card needs anyway.

---

## Evaluation (phase 3)

**R20 · Latency set** (C2)
Primary cell = the latency Эра runs in production, a project setting defaulting to `[56,1]` (160 ms). Matrix =
`[56,0]`, `[56,1]`, `[56,13]`; the "offline" column in 11 is `[56,13]`, labelled "high-latency reference". The gate
reads the primary cell only; other cells are reported. A3 evaluates at the primary cell, not `[56,0]`.

**R21 · Normalizer identity** (spec gap) — ML rationale: scoring and text style are different jobs.
- *Scoring normalizer* (what WER is computed after): a registry asset, versioned, shared, pinned by golden sets and
  Eval records. Without that, WER is not comparable across projects or over time.
- *Training text style* (punctuation, casing, numbers in targets): project language pack, file SHA.
- A pack's `normalizer.yaml` references a scoring normalizer version; project changes to scoring rules are frozen
  into a new registry version, which forces a new baseline (existing spec rule).

**R22 · Eval records and model registration**
Eval records are keyed by weights content hash × golden set version × scoring normalizer version × decoding config
hash (latency, boost list, beam). Checkpoints are evaluated and cached before registration; `models.register` moves
into phase 3 (register = publish a checkpoint whose gate passed, with its eval report); export stays in phase 5.
Experiments' "register best" then works in phase 3.

**R23 · Baseline before production**
The base model at its pinned revision is registered as a model version of kind `base`; the `baseline` alias points
at it until the first promotion. Its eval cells are computed once and cached for every project.

**R24 · Boosting in evaluation**
Decoding config is an eval axis: `evals.new` takes `decoding: [{boost: none}, {boost: <list SHA>, weight}]`. Static
RNNT context biasing is implemented in the eval decoder in phase 3; the Language pack "test a phrase" box is a
one-utterance eval. The gate checks entity recall on the boosted terms *and* general WER, because over-boosting
shows up as insertions of listed terms.

**R25 · Audio serving** (spec gap)
`GET /utterances/{id}/audio?channel=&start=&end=` returns a 16 kHz segment with range support. Reviewers get short-
lived signed URLs streamed through Media Source Extensions with no download control. "Play, no download" is a
deterrent, not a guarantee: anyone who can hear audio can record it; the audit log records who played what.

---

## Data (phase 4)

**R26 · LID and pseudo-label models** (C10) — **confirm licences**
All are registry base-model entries of kind `auxiliary`, pinned by revision, licence checked at adoption.
- LID: a VoxLingua107-trained classifier (covers Hebrew), confirmed by the base model's own output language.
- Pseudo-label ensemble for he-IL: Nemotron 3.5 at the pinned revision, a strong offline Hebrew model (ivrit.ai
  Whisper fine-tune), and a massively multilingual model (the "omnilingual-3b" of the starter pipeline).
  Agreement filter: pairwise WER ≤ 0.15 after the scoring normalizer; disagreements go to triage, not to training.
- The one hard rule: no model whose licence forbids commercial use of its outputs.

**R27 · Annotation guidelines**
Markdown in the project repository (`annotation/guidelines/<name>.md`), versioned by commit SHA; a batch pins the
SHA; the golden-set card cites it. Help articles stay product documentation.

---

## Deploy and flywheel (phase 5)

**R28 · Retention vs immutability** (B6) — **confirm** (legal)
Two retention classes. *Captured samples* (unreviewed flywheel audio): 90 days, then the blob is deleted.
*Curated items* (annotated golden-set members, accepted corrections): redacted audio kept under a longer term
(proposal 24 months) because a telephone golden set that expires every 90 days makes scores incomparable.
Deletion of a blob leaves a tombstone: versions that referenced it stay immutable as manifests with lineage and
their Eval records, are marked incomplete, can't be materialised for new training, and are re-frozen without the
member when needed.

**R29 · PII redaction** (spec gap)
A `pii_redact` step: regex/checksum detectors for numbers (phone, national ID, card, amounts) plus a Hebrew NER model
for names and addresses; spans are mapped to audio through the ASR word timestamps and replaced by tone ± 150 ms.
Measured, not assumed: recall on an annotated PII set is a gate for the step (target ≥ 95 % on numbers). The judge
gets text with typed placeholders only.

**R30 · Staging serving and benchmarks** (spec gap)
Triton runs as a compose profile on the staging card under its own memory cap; shadow replay and benchmarks are
queue jobs of kinds `shadow` and `benchmark`. Benchmarks require the card exclusively (latency numbers taken beside
a training job are meaningless); shadow replay is throughput work and can share.

**R31 · Parity and latency thresholds** (spec gap)
Parity: ONNX vs NeMo WER difference ≤ 0.1 absolute on the fixed 200-utterance sample (A3 acceptance) *and* ≥ 99.5 %
identical token sequences. Latency: p95 time-to-final at the primary chunk size ≤ chunk + 100 ms at the target
concurrent streams. **confirm**: target concurrency comes from Эра's peak; placeholder 32.

**R32 · Эра interfaces** (spec gap) — **confirm** with Эра
Defined in `api/openapi.yaml`: `samples.new` (call id, channel, span, audio or reference, production hypothesis,
confidence, model version, redacted dialogue context, boost list used), `samples.edit` for operator corrections, and
in the inference contract a `boost` field (phrases with weights, cap 100 per call).

**R33 · Signed promotions** (spec gap)
A Promotion record holds the model version, artifact hashes, approver and time, hash-chained to the previous record
and signed with the instance's Ed25519 key. The generated delivery script prints the record hash at the end; the
person pastes it into Cadence to confirm, which proves the script that ran is the one approved.

---

## Smaller items

- **R34 · CLI**: `cadence <entity> <verb>` generated from the contract, same Go binary as the server
  (`cadence serve`), hand-written `admin` and `smoke` subcommands; generated commands in phase 1, `smoke` in phase 4.
- **R35 · Agent budget in `project.yaml`** (C4): `agent_budget: {turns_per_day: 200, tokens_per_turn: …}`; money
  appears only with API-key auth.
- **R36 · Versions** (C6): a pipeline version is its commit SHA; `YYYY-MM-DD.<sha>` is for registry assets only.
- **R37 · Shortcuts** (C9): Switch project leaves Ctrl/Cmd+Shift+P (Firefox private window) for the palette with an
  `@` prefix plus Ctrl/Cmd+Alt+P; S2 verifies interceptability of every default key.
- **R38 · `CadenceEvent.projectId`** (B8) optional; **R39 · compose exposure** (B7) as in the roadmap.

## Needs the owner's answer

| # | Question | Default built meanwhile |
| --- | --- | --- |
| R6 | Do the Claude subscription terms cover ACP-driven and scheduled headless sessions? | API key for playbook/scheduled sessions |
| R28 | Legal basis and term for keeping curated call audio | 90 days captured, 24 months curated |
| R26 | Licences of the auxiliary models | Adopt only what passes the check |
| R31 | Peak concurrent streams per card at Эра | 32 |
| R32 | Эра's side of the samples and boost contracts | Contract drafted in OpenAPI |
