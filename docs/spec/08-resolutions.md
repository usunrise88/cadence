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
  Phase 1 adds `host`: the agent host's protocol (`hostSessions.claim|report|ask|decision`), authenticated by the
  agent-host credential only; like `auth` its operations are not commands.
  2026-09-30 adds `egress` (`egressHosts.list`, the allowlist the egress proxy polls with its own `cep_` credential; not
  a command) and `agentCredentials` (`agentCredentials.list|get|set|verify|archive`, `agentProviders.list`; the host's
  side is `hostCredentials.claim|report` under `host`). `agentCredentials` operations stay commands (actor,
  Idempotency-Key, If-Match, dryRun, audit) and use vocabulary verbs; they are exempt only so that they are never MCP
  tools: an agent must not see that its own model account can be changed, let alone try. Full scope (the admin) is
  required, and the presets forbid `agentCredentials.*` as well (defence in depth).
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
- 2026-09-30 adds `verify` (agent credentials; a mutation that records a result; no confirm): a live check through the
  agent is neither `scan` nor `sync`.

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
- 2026-09-30: the volume is filled either by the `agent-host login claude|opencode` CLI (the fallback) or by the agent
  host itself from Settings → Agents: the control plane seals a submitted value in the secret store's transit area
  under a task id, the host claims the task (`hostCredentials.claim`), writes the file 0600 in the agent's own format
  (`claude/oauth-token`; `opencode/auth.json` and, for a custom base URL, a provider block in `opencode/opencode.json`),
  acknowledges (`hostCredentials.report`), and the control plane deletes the transit copy. Postgres keeps metadata
  only (last four characters as a hint, set at/by, expected expiry, delivery and verification state, models); the
  value never enters Postgres, a log line, an event, an audit row, a response or an agent context.
- Each session runs as its own Unix user from a uid pool inside the agent-host container; its worktree is `0700` to
  that uid. Other sessions' worktrees, other projects and the host's own files are unreadable. No `./recipes` mount:
  the host clones from the control plane.

**R4 · Network sandbox** (B4)
The agent-host container sits on an internal compose network whose only egress is an allowlisting proxy: Anthropic
API, the configured opencode providers, Hugging Face, PyPI, NGC, and the control plane (MCP). This enforces the
Guardrails line for both drivers at one place. Claude Code's own sandbox is enabled on top through the preset.
2026-09-30: `CADENCE_EGRESS_ALLOW` is the static base list; the proxy adds the API hosts of the providers configured in
Settings → Agents (catalogue hosts, a custom base URL's host, `host:port` when the URL names a port, an IP literal only
when listed exactly), polled every 15 s from `egressHosts.list` with its own credential (`cep_`, written by the control
plane to `CADENCE_EGRESS_TOKEN_FILE`); the last good list is kept while the control plane is unreachable.

**R5 · Timeouts** (C3)
Three different clocks: *stuck turn* — no ACP update for 5 min during a turn → cancel the turn, pause, note;
*idle* — interactive session without a user message for 30 min → pause (cheap resume); *waiting approval* — never
pauses the session; the approval itself expires after 24 h → treated as deny, Telegram notified. Defaults live in
`defaults.yaml`.

**R6 · Agent authentication** (00 decision log) — confirmed by the owner, 2026-09-29
Claude Code sessions of every kind — interactive, read-only, playbook and scheduled — use the owner's Claude
subscription. The driver keeps an API-key mode per profile as a fallback; budgets count turns and tokens in both
modes, money only for an API key. opencode sessions use MiniMax through its Token Plan: the provider key lives with the
agent's own configuration in the `agent-credentials` volume (R3), never in Cadence's secrets or an agent context. The
free OpenCode Zen model used in spike A1 is for spikes and gated live tests only; it sends prompts to a third party.
2026-09-30: both are connected from Settings → Agents (instance-wide, admin only). Claude: the admin runs
`claude setup-token` once on any machine with a browser and pastes the token (valid about a year; the UI warns 30 days
before the expected expiry). opencode: a provider from a catalogue — MiniMax (default model `minimax/MiniMax-M3`),
Anthropic, OpenAI, OpenRouter, DeepSeek — or an OpenAI-compatible custom base URL (self-hosted vLLM; key optional);
several may be configured and the admin picks the default model of new projects (else `defaults.yaml`). Verify asks
the host for a tiny real request through the agent as a sandboxed session user behind the egress proxy: Claude
`claude -p` on haiku; opencode `opencode models <provider>` (the list is recorded) then `opencode run` on the provider's
cheap model.

**R7 · Permission presets and policy engine** (spec gap)
- Preset = `control-plane/templates/presets/<name>.yaml`, Cadence-level rules in three classes:
  `tools` (by verb class: read, draft, spend, gated, forbidden), `files` (worktree only), `shell` (allow/ask/deny
  patterns). `guardrails-default` encodes the Guardrails table.
- Renderer maps a preset to Claude `permissions.allow/deny/ask` (+ sandbox settings) and opencode `permission`.
- Policy engine runs server-side on every command: input = actor, verb class, entity, scope, estimate vs remaining
  budget; output = `allow` | `approval` | `deny`. Rules are data (same YAML), evaluated in order, first match wins;
  default `deny`. Agent-side files never widen what the server allows.
- Built in phase 1 (stream C): presets `guardrails-default` and `read-only` in `control-plane/templates/presets/`, the engine and the renderer in `control-plane/internal/policy`, the guide `docs/help/guides/approvals.md`. Rules marked `everyone` apply to people too (R8 `baseline`); agent-side files allow every Cadence tool the server may run (spend and gated ones answer with an approval id) and deny the rest.

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
  As built (2026-10-01): plus a fixed per-lease overhead (model load, final validation, saves) — the calibration's
  measured `leaseOverheadSeconds`, else `estimates.lease_overhead_seconds` (120 s, gate rehearsal); ± on the steps.
  Seconds/step is *measured* by `runs.calibrate` (OOMptimizer plus 50 timed steps) and cached; the cache is shared
  by all projects, so only the family's calibrate step of `runs.calibrate` or of a run from the base model, on the
  key's base model, precision and card, replaces an entry (control-plane audit, 2026-10-01); other calibration
  outputs are recorded unshared (`calibration_observations`). Before any
  calibration a table in `defaults.yaml` answers, seeded from A3.
- Eval: audio hours × RTF for (model size, card, latency setting), from the benchmark cache, seeded from A3.
- Data: bytes to materialise from the shard index.
- Every estimate carries `basis: measured | table` and ±; approvals show it. Phase 1 ships the table path only.
- Budgets (as built, 2026-10-01, after the phase-2 audit): the remaining budget subtracts committed work (the
  remaining estimates of GPU steps of running pipeline runs), every spending command names an estimate
  (`pipelineRuns.retry` and `jobs.resume` included, in the `gpu-spend` rule), and an unknown GPU cost asks for
  approval (fail closed) instead of passing ungated.

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

_Folded into the spec files on 2026-10-02 with the phase-3 plan (`docs/review/2026-10-02-phase-3-plan.md`): 02 "Evaluation entities", 03 "The eval pipeline" and "Scorers and metrics", 04 "Block 3", 06 "Media" (R25, R47–R50), 10 "Audio view and charts" (R51–R53), 11 "Panel catalogue". Where the plan is more precise, the spec files follow the plan._

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

_Folded into the spec files on 2026-10-04 with the phase-4 plan (`docs/review/2026-10-03-phase-4-plan.md`): 02 "Storage and mounts", the dataset entities and the registry (auxiliary kind, adoption checks, archive, `data.lock`, deprecation), 03 (ingest and pseudo-label pipelines, step kinds, artifact formats, interoperability), 04 "Block 1" and "Annotation workflow", 05 (presets), 06 "Media" (tracks, reviewers), 10 "Glossary", 11 "Panel catalogue"; departures in `ROADMAP.md` "Phase 4 notes"._

**R26 · LID and pseudo-label models** (C10) — **confirm licences**
All are registry base-model entries of kind `auxiliary`, pinned by revision, licence checked at adoption.
- LID: a VoxLingua107-trained classifier (covers Hebrew), confirmed by the base model's own output language.
- Pseudo-label ensemble for he-IL: Nemotron 3.5 at the pinned revision, a strong offline Hebrew model (ivrit.ai
  Whisper fine-tune), and a massively multilingual model (the "omnilingual-3b" of the starter pipeline).
  Agreement filter: pairwise WER ≤ 0.15 after the scoring normalizer; disagreements go to triage, not to training.
- The one hard rule: no model whose licence forbids commercial use of its outputs.
- *As built (2026-10-04, owner's licence table of 2026-10-03):* a registry kind `auxiliary` of its own, not a base
  model; seeds `whisper-large-v3`, `whisper-he-ivrit`, `oasis` (the owner's gRPC ensemble, called, never started),
  `lid-voxlingua107` and `omniasr-ctc-1b`; MMS and `mms-lid` are excluded (CC-BY-NC). LID falls back to Whisper's
  language token because speechbrain conflicts with the NeMo runtime's torch (00 decision log). The members are
  `nemotron_transcribe@3`, `whisper_transcribe@1` and `oasis_transcribe@1`, combined by `pseudolabel_ensemble@1`
  (03; 02 "Auxiliary models"). Since the owner's decision of 2026-10-04 the template votes Whisper and OASIS only
  (OASIS required) through `pseudolabel_ensemble@2` (00 decision log).

**R27 · Annotation guidelines**
Markdown in the project repository (`annotation/guidelines/<name>.md`), versioned by commit SHA; a batch pins the
SHA; the golden-set card cites it. Help articles stay product documentation.
*As built (2026-10-04):* the batch pins the repository's HEAD commit at `batches.new`; reviewers see the path and
commit, and from the phase-4 tail its text at that commit (`guidelines.get`; 04 "Annotation workflow").

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
  Built in phase 1: `make gen` writes the command table from the contract (exempt `auth`/`me` and planned operations
  left out); flags come from parameters (`p` → `--project`, `If-Match` → `--if-match`), `--body` takes JSON,
  `@file` or `@-`, the Idempotency-Key is generated; `CADENCE_URL`/`CADENCE_TOKEN` or `--url`/`--token`; JSON on
  stdout, the problem on stderr with exit 1, usage errors exit 2 (help article `guides.cli`).
- **R35 · Agent budget in `project.yaml`** (C4): `agent_budget: {turns_per_day: 200, tokens_per_turn: …}`; money
  appears only with API-key auth.
- **R36 · Versions** (C6): a pipeline version is its commit SHA; `YYYY-MM-DD.<sha>` is for registry assets only.
- **R37 · Shortcuts** (C9): Switch project leaves Ctrl/Cmd+Shift+P (Firefox private window) for the palette with an
  `@` prefix plus Ctrl/Cmd+Alt+P; S2 verifies interceptability of every default key.
- **R38 · `CadenceEvent.projectId`** (B8) optional; **R39 · compose exposure** (B7) as in the roadmap.

---

## Second pass of 2026-09-29: extensibility, trying models by hand, audio views

The owner asked three questions: how hard it would be to train other models (k2/icefall from scratch, newer
stacks), whether Cadence can test ASR by upload, microphone and live streaming, and whether spectrograms and charts
are designed in. The spec answered no to the second and third and assumed NeMo in about ten places. R40–R54 answer
them, built on research of 2026-09-29 (sources in `07-audit-risks-sources.md`). The owner's decisions the same day:
the framework seams now and everything beyond them deferred (R44–R46); manual tests store nothing (R47). ROADMAP
places the rest; spikes A5 and S5 test it, F1 waits with the deferred packs. Ported into the Doc (Resolutions tab and
decision log) on 2026-09-29; the section-level edits of this pass (entity, panel and glossary rows, theming,
defaults, the consistency matrix) are in these files only until the Doc's tabs are re-synced.

### Extensibility: training frameworks and model families (phase 2 seams; everything beyond them deferred)

Where the spec assumes NeMo today: one worker image; latency spelled as `att_context_size`; OOMptimizer as the
calibration; `init_from_nemo_model`, Noam and `target_lang` in the run recipe; NeMo cache-aware inference as the only
evaluator; `.nemo` → ONNX → Triton as the only artifacts; RNNT context biasing as the only boosting; a Lightning
logger for metrics; runs that start only from a base model or a checkpoint; one card per job; the NeMo fine-tune skill.

Why now: the seams cost days while step kinds are being born (phase 2) and weeks later, when checkpoints, eval
records and panels already assume NeMo. The shape follows platforms that already solved it: Kubeflow Trainer's
training runtimes (an admin-defined image and policy that jobs reference), W&B Launch (queues and agents that pull a
per-job image), MLflow's model flavours (framework-specific payload, one common interface).

**R40 · Runtimes**
- A runtime is a registry entry: a container image pinned by digest, its environment lock (CUDA, PyTorch, NeMo or
  k2/icefall, Lhotse) and the worker plugin it carries.
- Each step-kind version names its runtime. A worker process lives in one runtime and advertises `runtime@version`,
  GPU class and count; it leases only that runtime's step kinds (R14 capabilities).
- Card slots belong to the control plane per host and card, not per worker, so two runtimes share a card without
  double-booking.
- v1 ships one runtime, NeMo Speech 26.07. Compose gets one worker service per runtime.
- A framework gets its own image even when its wheels would fit another's (k2 has a wheel for NeMo Speech 3.0's
  PyTorch), so the two stacks upgrade independently.
- Adding a runtime is an admin command with approval (`runtimes.new`), because the image runs arbitrary code on the
  GPU host.

**R41 · Model families**
A model family is a versioned descriptor that a runtime publishes at start, beside its step kinds. It holds:
- framework and architecture;
- artifact formats and what loading a checkpoint needs (config, train arguments, tokenizer);
- input (sample rate, channels) and features;
- tokenizer kind;
- capabilities: streaming, word timestamps, confidence, boosting method, language prompting, and train modes
  `finetune | adapter | scratch`;
- latency profiles (R43);
- the step kinds that fill each role: calibrate, train, average, transcribe, export, parity reference;
- a `defaults.yaml` section;
- help and skill slugs.

Base models, checkpoints and model versions carry a family reference. The UI and MCP render family options from the
descriptor's schemas. No control-plane or web code branches on a family name; R45 runs the flows against a second
family to prove it. Nemotron 3.5 streaming (cache-aware FastConformer RNNT, NeMo) is the first family.

**R42 · Neutral artifacts at the seams**
Framework code lives only in the role step kinds of R41. Everything downstream reads neutral, self-describing
artifact types:
- `shar` in: audio and text only. Frozen versions never store features, because 80 or 128 mel bins, the frame rate
  and normalisation belong to a family; a pack computes features on the fly or materialises them itself.
- `checkpoint`: the family's payload, the configuration needed to load it (for icefall, every train argument), the
  tokenizer reference, and neutral metadata (family, step, validation metrics, weights hash). Optimiser and sampler
  state is a separate `training-state` artifact, used only to resume.
- `hypotheses`: JSON lines per utterance with text; words with start, end and confidence; the decoding config and
  its hash; family and weights hash. Streaming decodes add partial events (audio offset, emit time, text).
- `analysis`: float16 arrays with frame rate and axis labels — the model's input features and per-frame emissions.
- `eval report` and `deployable` (format, files, serving metadata).

Scorers, gates, Diff, Audio, Shadow, triage and the Transcription panel read only these, so a new family changes
none of them.

**R43 · Latency profiles**
- A family declares named latency profiles. Each has its algorithmic latency, chunk and left context in
  milliseconds, plus the family parameters that realise it.
- Nemotron 3.5 has `80ms`, `160ms`, `320ms`, `560ms` and `1120ms`: `att_context_size` `[56,r]` gives 80 × (r + 1) ms,
  with a left context of 56 frames (4.48 s).
- A family without streaming has one profile, `offline`.
- The eval matrix axis, the primary cell (R20) and the decoding config of Eval records (R22) name profiles. The UI
  shows "160 ms · [56,1]". Families line up by milliseconds, not by parameter spelling.
- Evaluation always runs the real streaming decoder, never an offline decode relabelled.

**R44 · Starting points and multi-card runs: the seams now, the features later**
- Owner, 2026-09-29: prepare for other ways of training, build none of them yet.
- Seams in phase 2:
  - `runs.new` carries `init: base | checkpoint`, an enum that can grow;
  - step resources carry `gpus` (1 in v1);
  - a checkpoint names the tokenizer it was trained with (the base model's).
- Deferred, without a phase:
  - training from scratch (`init: scratch`), with a `tokenizer` artifact and a Tokenizer registry kind trained by
    `tokenizers.new`;
  - gang leases of several cards;
  - adapter (LoRA) runs;
  - multi-node training.
- When they come back, `scratch` in the Nemotron family is the cheapest first step: NeMo ships streaming
  FastConformer configs for it. A second framework is justified only by what it adds, for example compact Zipformer
  models for CPU through sherpa-onnx.

**R45 · Framework packs and the conformance suite**
- A framework pack is the unit of extension. It contains:
  - a runtime image and its environment lock;
  - the worker plugin (entry points `cadence.steps`, `cadence.families`);
  - exporters and the transcribe (eval decoder) step;
  - pipeline templates and playbooks;
  - a `defaults.yaml` section;
  - help articles and an agent skill.
- At start the worker publishes the whole pack. The control plane stores it as registry versions keyed by the runtime
  digest, just as it stores the step registry today (R14).
- Every pack passes one conformance suite on fixtures: calibrate → train a few steps → average → transcribe (file and
  streaming) → export → parity → score. The suite also checks schemas: `x-cadence` complete, help present, profiles
  declared. The NeMo pack's run grows with the phases; export and parity join it in phase 5.
- CI runs the suite for two packs:
  - the NeMo pack, nightly on the staging GPU;
  - a CPU `toy` pack (a tiny CTC model trained in seconds) on every pull request. The toy pack exists only to keep
    the seams honest.
- NeMo is the only real pack (owner, 2026-09-29). Packs beyond it are deferred without a phase, and spike F1 with
  them: sherpa-onnx, Hugging Face transformers, k2/icefall.
- When one comes back:
  - sherpa-onnx goes first, because it already runs Whisper, Omnilingual CTC and the exported Nemotron 3.5;
  - icefall changes little now (17 commits in the last year), does not read Shar, and needs k2 matched to PyTorch
    exactly, so it would be a pack, never a dependency of the NeMo runtime.
- R26's pseudo-label members that are not NeMo models are decided when phase 4 starts: they run in the NeMo runtime
  if its libraries serve them, or the ensemble starts with NeMo models only.
  *Decided (owner, 2026-10-03; built 2026-10-04):* Whisper loads per job in the NeMo runtime; OASIS is a running
  service called from a CPU pack `services`; the omniASR aligner needs fairseq2 on torch 2.8, so it runs in a second
  one-step runtime `omni` (00 decision log; 03 "Runtimes, model families and latency profiles").

**R46 · Deployment targets and external baselines**
- A deployment target declares the families and formats it serves:
  - Эра's Triton target serves the Nemotron family: cache-aware ONNX Runtime models with sequence batching and
    implicit state. k2-fsa's Triton recipe for streaming Zipformer uses the same pattern, so a Zipformer target
    would be a builder, not a new server.
  - sherpa-onnx bundles serve CPU and edge.
  - vLLM would serve LLM-based ASR.
- A model version of another family can be registered, evaluated, used as the offline oracle (Block 5 signals) or as
  a pseudo-labeller. It cannot be promoted to a target that does not serve it.
- A family meant for Эра brings a Triton repository builder and its parity check in its pack.
- Deferred with the packs: inference servers that speak the OpenAI audio API (`/v1/audio/transcriptions`,
  `/v1/realtime?intent=transcription`), such as NVIDIA Speech NIM and vLLM, as eval baselines through one adapter.

### Trying models by hand: transcriptions and live audio (phase 3; Triton target in phase 5)

**R47 · Transcriptions are manual tests; nothing is stored**
- A transcription is a manual test (owner, 2026-09-29). A person runs models on a file, the microphone or an
  utterance span and watches the words appear.
- Nothing outlives the session: no audio, no text, no metrics.
  - The interactive job's record keeps only who, when, which targets and the GPU time, because the queue and the
    daily allowance need them.
  - The page can copy the text.
  - Evaluations are where results are kept and compared.
- `transcriptions.new` is the only operation: there is nothing to get or list. It and its socket carry the `media`
  tag, so they are not MCP tools.
  - This is a human tool: someone has to speak or listen.
  - Agents test models through evals and the benchmark, which run the same decoder and are stored.
  - It is the second UI-only surface after workspace layouts (principle 1).
- Inputs:
  - a file chosen in the browser;
  - the microphone;
  - an utterance span already in Cadence (`utt:123#t=1.2,3.4`).
  One streaming decoder serves all three. A file can play at real-time pace or as fast as the card allows; the
  microphone and paced files show latency.
- A file's bytes go to the worker's temporary directory for the session only, capped at 15 minutes of audio. There
  they are decoded with ffmpeg and the training resampler. They are deleted when the socket closes, and a sweep
  removes what a crashed session left within an hour.
- Targets: one to three of checkpoint, model version, base model and, from phase 5, the staging Triton deployment.
  - Each target has its own latency profile (R43), boost list or none, and language.
  - Streaming families decode as streaming at the profile, so the page shows what production would have written.
  - To see the gap to the high-latency reference, add the same checkpoint at `1120ms` as a second target.
  - A blind option hides which target wrote which lane until the person picks the better one (the ASR-arena
    pattern). The pick is not recorded.
- A typed reference gives WER and a diff on the page. The Language pack's "test a phrase" (R24) is a transcription
  with two targets, boost on and off.
- `analysis: [features, emissions]` asks the transcribe step for the model's inputs and per-frame outputs, shown in
  the audio view. They live as long as the session.

**R48 · The live channel**
- `transcriptions.new` takes targets, profiles, boost lists and language. It returns a session with a `streamUrl` and
  a single-use ticket valid 60 s, and the server also checks `Origin`.
- The browser then opens a WebSocket to `/api/transcriptions/{id}/stream`. This is the only connection besides the
  event stream, because audio flows up and SSE is one-way.
- No WebRTC: its Opus encoding and echo processing would change the audio under test.
- The protocol copies the shape the streaming vendors converged on (Deepgram, AssemblyAI, Speechmatics, Soniox,
  NVIDIA NIM). Configuration comes first, then binary audio. Partials replace each other; finals never change.
  `finalize` flushes without closing, and `end` flushes, summarises and closes.
  - Up, client to server:
    - `start` as JSON: the input (microphone with its capture rate and `getSettings()`, a file, or an utterance
      span), telephony simulation, and the pace for files;
    - then binary frames: 16-bit little-endian mono PCM at the capture rate, 80 ms each, or a file's bytes closed by
      `fileEnd`;
    - `finalize`, `keepalive` and `end` as JSON.
  - Down, server to client:
    - `started`: the effective configuration and model load time;
    - `partial`: target, segment, sequence, text, audio end;
    - `final`: words with audio-time start, end and confidence, and the endpoint reason;
    - `stats`: real-time factor, queue, relay time;
    - `error`: problem+json;
    - `summary`, then close code 1000.
  - Every result states the audio offset it covers, so latency is measured on audio time. The client adds its own
    wall-clock stamps.
- Message schemas are components in `api/openapi.yaml` (`LiveClientMessage`, `LiveServerMessage`). The TypeScript
  types are generated like the rest.
- Media endpoints (`…/stream`, `…/audio`, `…/peaks`, `transcriptions.new`) carry the tag `media`. They are exempt from
  the verb rule and from MCP, like `auth` and `me`.
- The control plane relays frames to a `live` job and enforces backpressure, caps and timeouts.
  - The worker dials out for the job (`/worker/live/{jobId}`), so the pull model of R14 holds.
  - In the NeMo runtime the job runs NeMo's streaming pipeline API (`nemo.collections.asr.inference`, the
    cache-aware RNNT pipeline):
    - one socket per stream id;
    - streams batched continuously;
    - end-of-utterance detection;
    - boosting and language per stream.
  - A hard finalize pads the right context with silence. Up to three targets receive the same audio.
- Limits in v1: one session per user, 15 minutes each, closed after 5 minutes idle.
- Nothing is written. The worker keeps audio and results in memory or its temporary directory for the session only.
  The latency and stability figures on the page are computed from the session's own events and go with it.

**R49 · Interactive compute**
- Job kind `interactive` (transcription sessions):
  - memory reservation from the family (Nemotron 0.6B: 3 GB, measured in A5; *A5 measured 6 000 MB plus 2 600 MB per
    further distinct checkpoint — 06 "Media" as corrected 2026-10-02*);
  - highest queue priority;
  - may run beside training under the card's cap, never beside a benchmark (R30);
  - counted in a small daily GPU-hour allowance per project (default 1).
- When no card has room, the session waits in the queue, the page shows its place, and live mode is disabled with the
  reason.
- From phase 5 a Triton target needs no worker job.
- sherpa-onnx can run exported Nemotron 3.5 on CPU, one export per chunk size. Its exporter needs a small
  `restore_from` change for a fine-tuned `.nemo`. It is a CPU fallback for when packs return.

**R50 · Capture in the browser**
- The microphone is captured with an AudioWorklet at the device rate, and the worker resamples with the same
  resampler as the training data. MediaRecorder's lossy formats are not used.
- `getUserMedia` runs with echo cancellation, noise suppression and automatic gain off by default ("raw microphone"),
  as Google and Deepgram advise for recognition. A toggle turns them on, to hear what a call stack does to the audio.
- Take channel 0 only: Safari returns a stereo track with audio on the left when echo cancellation is off.
- Telephony simulation: down to 8 kHz, through the codec of the project's augmentation profile (G.711 by default),
  then back up to 16 kHz with the training resampler. This is the path NeMo recommends for telephone audio. The
  profile's SHA and seed are shown with the result. A wideband laptop microphone says little about 8 kHz calls.
- The page has a device picker and an input level meter with a clipping mark. It needs a secure context: HTTPS
  through Caddy, or localhost.
- Display:
  - Hebrew renders right to left, with bidi isolation around digits and Latin text.
  - Grey partials update in place; finals are solid, with endpoint marks.
  - Confidence shades words, and timestamps show on hover.
  - Live p50/p95 time to final and the real-time factor sit under the lanes.

### Audio views and charts (phases 2–5)

**R51 · One audio view, many tracks**
- `AudioView` is a shell primitive (`@/shell/audio`), allowed in panels the same way the entity primitives are. The
  panels that show audio compose it: Audio, Diff, Triage (Annotate), Transcription, Language pack ("test a phrase"),
  Recipe (augmentation preview) and Shadow. No panel draws audio itself, and lint enforces this as it does for
  `EntityHeader`.
- The view owns one time axis: visible range, zoom, playhead and loop span. Tracks render against it, stacked like
  Sonic Visualiser layers or Praat tiers. The axis runs left to right in every locale.

  | Track | Shows | Data | Phase |
  | --- | --- | --- | --- |
  | Overview, waveform | Min/max peaks per channel (caller and bot lanes for calls), clipping marks | Session audio and short utterances from their PCM; long audio from a `peaks` artifact (≈ 450 KB per hour; *S5: 720 KB per channel-hour, 10 ms int8 min/max*) computed at ingest (phase 4; *as built: stored when a dataset version is registered, multi-level `cadence.peaks/2`, first view as fallback, 06 "Media"*) | 3 |
  | Spectrogram | The acoustic view (R52) | Browser FFT for session audio and short spans; server tiles for long audio | 3 |
  | Model input | The family's features as the model saw them, from its own preprocessor; SpecAugment masks in training previews | `analysis` artifact | 3 |
  | Emissions | CTC posteriors or RNNT per-frame emissions (top tokens and blank) | `analysis` artifact | 3 |
  | Hypothesis words | One lane per target; word confidence shades each word (NeMo's entropy-based confidence); S, D and I against the reference by glyph as well as colour | `hypotheses` | 3 |
  | Streaming timeline | Each word from its first partial to its final, against audio time; revisions highlighted | `hypotheses` partial events | 3 |
  | Reference words | The reference transcript at aligned times; unaligned references show as text | Alignment step (NeMo Forced Aligner with a CTC model for the locale; *as built: `align_reference@1`, omniASR CTC emissions + torchaudio's aligner, NFA cannot read omniASR*) | 4 (*drawn since the phase 4 tail: `words.get?goldenSet=`*) |
  | Energy, VAD | Level in dBFS, speech regions, endpoints, estimated bandwidth | Worker step; the worklet when live (*as built: `tracks.get`, computed on request; bandwidth too*) | 4 (live: 3) |
  | Redactions, boosted terms | PII spans replaced by tone; hits of boost-list terms | `pii_redact`; decode | 5; 3 |

- Playback goes through an HTMLMediaElement (Media Source Extensions for signed segments, R25).
- The spectrogram, model-input, emissions and word tracks are Cadence code: no maintained open-source WebGL
  spectrogram library exists.
- wavesurfer.js 8 (BSD-3) may provide the waveform, regions, timeline and minimap tracks, but only if S5 shows that it
  follows the external time axis and renders in popouts. Otherwise those tracks are Cadence code too; they are simple
  over precomputed peaks. *S5 (2026-10-02) decided: Cadence code — wavesurfer's region drag is dead in popouts,
  following an external axis drops half the frames while zooming, and it is 3× the size of the whole view.*
- Excluded for their licences: peaks.js and waveform-data (LGPL-3.0), audiowaveform (GPL-3.0; peaks come from our own
  step), audioMotion-analyzer (AGPL-3.0).
- Spans are selections: `utt:123#t=1.20,2.35`, in the W3C Media Fragments temporal syntax. They work in chat
  references and deep links; the Inspector shows a span's statistics, and Ask agent attaches it.
- Words are DOM, not canvas. Each word is its own bidi-isolated run: Hebrew runs right to left inside its box, with
  digits and Latin text isolated. The flowing transcript beside the view follows the locale's direction, and hovering
  a word highlights it in both places.
- Keys are `view.audio.*` commands, active only while a view has focus: Space plays and pauses, ←/→ seek, +/− zoom,
  `[` and `]` set loop in and out, `,` and `.` step between words. The window-move arrows of a floating panel apply
  only while its frame has focus.
- Exports: Praat TextGrid, NIST CTM and WebVTT. TextGrid and CTM also import as a reference track.

**R52 · Spectrograms: two modes, defaults, computation**
- Acoustic mode, for people: a dB STFT on a mel or Hz axis.
  - Defaults, in `defaults.yaml` `views.audio`:
    - 25 ms Hann window with a 10 ms hop (the model's frame grid), FFT 512;
    - mel axis 0–8 kHz;
    - range 80 dB below the peak, gain 0;
    - colormap magma.
  - Audio whose estimated bandwidth shows 8 kHz origin stops at 4 kHz, with a Nyquist line.
  - Presets:
    - "Praat broadband": Praat's editor defaults — 5 ms Gaussian, 0–5 kHz, 70 dB, +6 dB/octave pre-emphasis, grey;
    - "Narrowband": 30 ms, harmonics visible;
    - "Model frames": the default.
  - A reassigned spectrogram is an expert option computed on the server, never the default.
- Model-input mode, what the model saw:
  - Computed by the checkpoint's own preprocessor in the transcribe step, never re-implemented in the browser.
  - Colormap:
    - sequential for features without normalisation (NeMo's cache-aware streaming configs use `normalize: NA`);
    - diverging over ±3σ for per-feature normalised ones.
  - For 8 kHz audio upsampled to 16 kHz, about 18 of 80 mel filters sit above 4 kHz and hold dither only. The view
    dims them: the telephony mismatch in one picture.
- Colormaps:
  - Available: magma (the default), viridis, cividis, inferno, Roseus (the default of Audacity and wavesurfer), and
    grey and inverse grey (the phonetics convention).
  - Turbo only on request, labelled "not perceptually uniform".
  - No jet or rainbow (Borland & Taylor 2007; Crameri, Shephard & Heron 2020): they invent edges and fail colour-blind
    readers.
  - The colormap ignores the light/dark theme; axes, grid and labels follow it at 3:1 or better.
- Where it is computed:
  - Browser, for session audio and spans under 10 minutes:
    - the served 16 kHz PCM (R25) goes through an FFT in a Web Worker (WASM; *S5: a JavaScript FFT, `fourier-transform`
      (MIT), matches PFFFT-WASM at 45 ms per minute of audio and no maintained WASM FFT exists*);
    - uint8 dB values go to WebGL2 once, as R8 textures with a 256×1 colour lookup texture;
    - gain, range and colormap are shader parameters, so they change instantly;
    - Canvas 2D is the fallback.
  - Server, for long audio:
    - a uint8 dB tile pyramid: a 10 ms base level, with coarser levels max-pooled over time;
    - computed by a worker step on demand and cached in the artifact store by content hash and settings (*as built
      in the phase 4 tail: by the control-plane job `media.spectrogram` on the first view, the same STFT as
      `spectrogram_tiles@1`; 00 decision log 2026-10-04, 06 "Media"*);
    - one hour at 10 ms × 257 bins is ≈ 93 MB, too much for a tab to compute or hold.
  - Live microphone:
    - AudioWorklet frames are posted to a worker as transferable buffers and drawn by the same FFT and renderer as a
      waterfall;
    - nothing round-trips to the server for visuals;
    - no AnalyserNode: its window is fixed and polling it drops frames.
- WebGL2 is the baseline, not WebGPU, whose Linux support was still rolling out at the last check.
- Chrome allows 16 active WebGL contexts per page. Views therefore share one renderer per window and survive context
  loss, and hidden panels release their textures.

**R53 · Charts**
- Two libraries, each for its job:
  - uPlot (MIT, 22 KB) for time series and live data: Metrics, latency traces, GPU telemetry. It gives synced cursors,
    EMA smoothing over a faint raw line, and min/max envelopes for dense series.
  - Apache ECharts 6 (Apache-2.0, tree-shaken) for analytics: histograms, bars, forest plots with confidence
    intervals, heatmaps, scatter and Pareto fronts. Its ARIA descriptions and decal patterns help screen-reader and
    colour-blind readers, and `setTheme` switches light and dark without re-creating a chart.
  - Not Recharts (the shadcn chart): SVG slows down past tens of thousands of points.
  - Not Plotly: 1.5 MB.
  - Dense heatmaps (spectrogram, emissions) belong to the audio view's renderer.
- Panels import charts only through `@/shell/charts` (added to the lint allowlist beside `@/shell/audio`); no panel
  imports uPlot, ECharts or WebGL directly.
- Chart data is contract data. Histograms, buckets, confidence intervals and aggregates arrive binned from the API:
  the same numbers an agent gets from the same `get` operation. The browser only zooms, smooths and switches scales.

  | Panel | Charts |
  | --- | --- |
  | Metrics | Loss, validation WER, LR, gradient norm, throughput (audio seconds per second), GPU memory; x by step, epoch, wall time or GPU-hours; checkpoint marks; pinned runs overlaid |
  | Run | Sample predictions of a fixed validation subset at each validation step: the text as it evolves |
  | Dataset version | Hours by language, source and speaker; duration histogram with the filter bounds and percentiles (as Lhotse's `describe`); characters per second with outliers; level, SNR and estimated bandwidth; sample rates and codecs; transcript length against duration; two versions overlaid (*as built, phase 4: duration and characters-per-second histograms with the filter bounds, level, hours by language, split, origin and role, source sample rates — from `datasets.get` stats; speaker, SNR, bandwidth and the overlay are not drawn yet*) |
  | Eval report | Matrix heatmap; forest plot of WER deltas with 95 % intervals; S/D/I stacked bars; WER by duration, SNR, bandwidth and speaker; per-utterance WER ECDF; top confusion pairs; entity accuracy; CDFs of latency to final and emission delay per profile; WER against latency across profiles and models; robustness matrix; an utterance table (as in NeMo's Speech Data Explorer) whose rows open in Diff and Audio |
  | Experiment | Parameter against metric scatter; parallel coordinates for sweeps |
  | Model, Queue & GPU | Benchmark p50/p95 against concurrent streams, real-time factor; GPU memory and utilisation |
  | Shadow, Triage | Divergence over time; signals per day; triage throughput |
  | Transcription | Live latency and real-time factor sparkline; the streaming timeline (R51) |

- Tokens:
  - categorical: eight Radix hues at step 9 (dark: 10), excluding the four status hues and the accent;
  - sequential: magma or viridis for heatmaps;
  - diverging: blue–slate–orange for deltas, never red–green;
  - the contrast script checks chart colours at 3:1 against their background (WCAG 1.4.11), and a
    colour-vision-deficiency simulation keeps neighbouring series apart;
  - the Theming table lists these as allowed pairings.
- Accessibility:
  - every chart has a table view with CSV copy, a keyboard cursor and a text summary;
  - colour is never the only channel (WCAG 1.4.1);
  - live charts redraw at most 4 times per second (the existing rule);
  - sonification (Chart2Music, MIT) is a later option.

**R54 · Streaming metrics and significance by published definitions**
- Latency to final: the time from utterance end to the final that covers it, with audio fed at real-time pace,
  reported at p50 and p95. Pipecat's "time to final segment" is the same measure. Utterance end comes from
  per-channel VAD or the aligned reference.
- Emission delay: the time from a word's aligned end to its first appearance in a partial, reported as percentiles
  PR50 and PR90 (Yu et al., FastEmit, ICASSP 2021). *As built (2026-10-04): `latency_score@3` against the golden
  set's reference alignment; `n/a` with a reason when the set is unaligned (03 "Scorers and metrics").*
- Partial stability: the unstable partial word ratio (Shangguan et al., Interspeech 2020), with edits per second
  beside it.
- All three come from the partial events of the `hypotheses` artifact (R42). Live tests, paced replays and eval runs
  therefore compute them the same way.
- Confidence intervals resample whole calls, or speakers where there are no calls: a blockwise bootstrap (Liu & Peng,
  arXiv:1912.09508), because utterances from one call are correlated. Bisani & Ney's 1 000-sample bootstrap stays the
  method; only the resampling unit changes.

## Needs the owner's answer

| # | Question | Default built meanwhile |
| --- | --- | --- |
| R28 | Legal basis and term for keeping curated call audio | 90 days captured, 24 months curated |
| R26 | Licences of the auxiliary models | *Answered 2026-10-03: Claude checks before adoption, the owner's table in the phase-4 plan; nothing whose outputs are not for commercial use* |
| R31 | Peak concurrent streams per card at Эра | 32 |
| R32 | Эра's side of the samples and boost contracts | Contract drafted in OpenAPI |
