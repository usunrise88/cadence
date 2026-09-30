# Cadence spec — Real-time model, authentication, operations, notifications, testing

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Real-time model

Every state change, from any actor, is one event on one stream; panels patch themselves from events instead of polling, and every change says who made it and which agent tool call caused it.

```ts
type CadenceEvent = {
  seq: number;                 // global, monotonic; the SSE id for resume
  topic: string;               // "run.123.metrics", "entity.mix.7", "agent.session.9"
  type: string;                // "mix.revised", "run.status_changed", "recipe.file_changed"
  entity?: { kind: string; id: string; rev: number };
  actor: { kind: "user" | "agent" | "automation"; id: string; sessionId?: string };
  causedBy?: { commandId: string; toolCallId?: string; approvalId?: string; draftId?: string };
  payload: unknown;
  at: string;
};
```

- Outbox: the event row is written in the same transaction as the change; a dispatcher publishes in `seq` order. Reconnecting clients resume from `Last-Event-ID`.
- Cache patching: the UI applies events to the TanStack Query cache; a panel re-fetches only when an event says its data is too big to carry.
- Attribution: a change made by an agent shows a small badge ("opencode · session 9"); clicking it scrolls the Chat panel to the tool call that caused it.
- Drafts: agent edits to mixes, recipes and gates land as draft revisions; the panel shows the diff as it grows and offers Accept or Revert. A per-entity policy can auto-accept.
  Phase 1 (mixes): a draft (`drf_…`) belongs to one entity and one author (actor + agent session), is based on one
  entity revision and has its own `rev`; the author's later edits update it. Events on `entity.{kind}.{id}`:
  `draft.created|updated|accepted|reverted` (payload `{draft}` with its `changes` against the base) and
  `presence.changed`. `drafts.accept` (If-Match: the draft's `rev`) applies the draft as the entity's next revision
  attributed to the person, `causedBy.draftId` naming the draft; a draft whose base is no longer the entity's
  revision answers `412 draft-stale` with the current revision. `drafts.revert` discards it. Agents never accept
  drafts. The policy per kind (`draft` or `direct`, i.e. auto-accept) comes from the project's agent profile
  (`draftPolicy`, Agent settings); a project without a profile falls back to `defaults.yaml` `drafts.*`.
- Concurrency: every entity has a revision; writes carry `If-Match`. A losing writer, person or agent, gets the current revision and a conflict error it must resolve, never a silent overwrite.
- Presence: while an agent is mid-edit on an entity, its panel shows "agent editing" and disables conflicting controls instead of racing.
  An agent is mid-edit while it has an open draft on the entity, or for `drafts.presence_seconds` after a direct
  edit; the entity carries `presence` (actor, tool call, draft) and `presence.changed` sends the whole list.
- Agent transcripts are events too (`agent.session.{id}`), so a chat is durable and can be opened from any tab or after a restart.
- Audio is not an event. The manual transcription test (R48) is the one media channel: a WebSocket per session carries audio up and words down, relayed by the control plane to a worker job; the job reports its state on `job.{id}` like any job, and nothing of the session is stored.

Topic scheme, canonical for both tabs:

| Topic | Carries |
| --- | --- |
| `entity.{kind}.{id}` | Revision changes of one entity, including drafts |
| `job.{id}`, `job.{id}.log` | Job state; log lines |
| `pipeline_run.{id}` | Step status changes |
| `run.{id}.status`, `run.{id}.metrics` | Training run state; metric points |
| `eval.{id}.progress` | Cells completed, utterances scored |
| `deploy.{id}`, `shadow.{deployment}` | Deployment stage changes; divergence samples |
| `queue`, `gpu`, `mount.{id}` | Queue order; card memory and compute; mount health |
| `triage.new`, `approvals` | New triage items; approval requests and decisions |
| `agent.session.{id}`, `agent.sessions` | One transcript; the session list |
| `recipe.{path}` | File changes in the project repository: commits (`recipe.changed`) and, from the agent host's worktree watcher, a running turn's uncommitted edits (`recipe.working`) |
| `compute.{id}` | Host and card health, slot occupancy |
| `entity.agent_credential.{id}` | An agent credential set, verified, written or removed by the agent host (metadata without the value or its hint; the Agents settings refetch) |

A subscription can use a wildcard on the last segment (`run.123.*`). Work events carry `projectId`; registry events carry none, so a client filters work by project and shows registry changes by reference.

## Authentication and access

One admin account, invitation links for reviewers, and opaque scoped tokens for agents and automation — all issued and revoked in one place, all hashed at rest, nothing external.

| Who | How they get in | Lifetime and scope |
| --- | --- | --- |
| Admin | Username and password (Argon2id) plus optional TOTP; a session cookie that is HttpOnly, Secure, SameSite=Lax | 30-day sliding session; everything |
| Reviewer | A signed invitation link sent with the batch; no password | Expires when the batch closes, 14 days at most; one batch's triage or annotation items, play audio, no download |
| Agent session | An opaque token minted when the session starts, passed to the agent as the MCP credential | Dies with the session; one project; the verbs of the project's permission preset; read access to the registry |
| Automation and CLI | Personal API keys (`cdk_` prefix) created in Settings, used as Bearer | Until revoked; scoped to one project or to registry read; opt-in: may run agent sessions in its project (2026-09-30); last-used time shown |
| Worker | A worker token (`cwk_`), one per compute host: the control plane keeps it in `CADENCE_WORKER_TOKEN_FILE` (a volume the worker mounts; host `CADENCE_WORKER_HOST`, default `staging`) and re-issues it when it no longer resolves; `cadence admin worker-token --host <name>` prints one for a worker elsewhere (phase 2) | Until re-issued (the host's older tokens are revoked); the worker protocol (`/worker-*`) for its own host only, and every other operation refuses it; secret values reach the step only through its lease |

- Mutations require the session cookie plus a custom header, or a Bearer token; SameSite plus the header is the CSRF defence. The cookie is `cadence_session` (an opaque `cws_` token); the header is `Cadence-Client: web`; tokens are `cdk_` (API key), `cst_` (agent session), `cwk_` (worker), `cah_` (the agent host: its protocol only) and `cep_` (the egress proxy: its allowlist only, 2026-09-30), stored as SHA-256. Unauthenticated requests answer `401 unauthenticated`, out-of-scope ones `403 forbidden`.
- All credentials live in one table: kind, scope, hash, expiry, last use; revocation is one click and is itself an audited command.
- TLS terminates at a reverse proxy in the compose file (Caddy with an internal certificate); the control plane listens on localhost only.
- Login attempts are rate-limited; a lost admin password is reset from the host shell (`cadence admin reset-password`), never by email.
- Passkeys (WebAuthn) are the v2 upgrade; nothing in v1 prevents adding them.

## Worker protocol

Workers pull, as CI runners do: a worker dials the control plane, publishes what it can run, long-polls for leases
and reports back. The control plane never calls a worker, the worker never touches the database, and the River job
of kind `step` stays the orchestrator (R14, R40; the Go side is `internal/steps`).

| Operation | Path | Purpose |
| --- | --- | --- |
| `workerRegistrations.new` | `POST /worker-registrations` | At start: the runtime, its step kinds and model families (below); answers the worker (`wrk_`) |
| `workerLeases.claim` | `POST /worker-leases:claim` | Long-poll (`wait` ≤ 30 s, default 20) with the worker id and card telemetry; answers a lease or nothing |
| `workerLeases.report` | `POST /worker-leases/{id}:report` | Heartbeat with progress and card telemetry; answers `stop` with a reason |
| `workerLogs.new` | `POST /worker-leases/{id}/worker-logs` | NDJSON log lines, ≤ 1 MiB per request |
| `workerMetrics.new` | `POST /worker-leases/{id}/worker-metrics` | Metric points, ≤ 5 000 per batch |
| `workerLeases.release` | `POST /worker-leases/{id}:release` | Completion: the step outcome with its output artifacts and final metrics, or a typed error |
| `workerArtifacts.set` | `PUT /worker-artifacts/{hash}` | Upload one blob, hash verified; for workers without the shared volume (remote, later) |

Rules:

- Tag `worker`: exempt from the verb vocabulary, the command pipeline and MCP, like `host`. Bearer `cwk_` only; a
  worker token reaches nothing else, and no other credential reaches these paths.
- Registration: the worker publishes its runtime (name, version, image, digest, environment lock, plugin version),
  every step kind it carries (`StepKindDescriptor`: version, parameter schema with `x-cadence`, `consumes` and
  `produces` by artifact type, resources, family role or `neutral`, secret names, help slug) and its model families.
  Each is stored as a registry version of kind `runtime`, `step_kind` or `model_family` (collections
  `runtime/<name>`, `step-kind/<name>`, `model-family/<name>`), named by its published JSON, so a restart with the same
  pack changes nothing and a changed pack is a new version. `runtimes.list|get`, `stepKinds.list|get` and
  `modelFamilies.list|get` read them.
- Scheduling: a claim gets a queued `step` job only when the worker's runtime publishes the pinned `kind@version`
  (a neutral core kind matches in any runtime with the same version and schema hash), a card slot is free under the
  card's memory cap, the card allows the job kind, the card's availability window fits the estimate (R19), then by
  project priority and FIFO. Card slots belong to the control plane per host and card, not per worker, so two runtimes
  never double-book a card. One training slot per card; an eval or data step shares the card's remaining memory only
  when its `memoryGb` fits beside what runs.
- Lease: `lse_` id, the job id, the step spec (resolved parameters, input artifact refs, output types, resources,
  priority, estimate, `overrides.batchScale` and `resumeFrom`, attempt, the run id when there is one), input URIs
  (`cas://b3:<hash>`), the card index and memory cap in MB, the secret environment, the job's `traceparent` and the
  heartbeat interval.
- Lifecycle of a step job: `queued` → `leased` (claim) → `running` (first report) → `done`, `failed` or `cancelled`
  (release). The worker runs each step as a subprocess in its own scratch directory, with the card's memory fraction
  set to cap ÷ card memory (0.5 on the staging card), and materialises directory inputs there by hard links.
- Heartbeats: `report` every 10 s. The answer `stop: true` carries `cancelled`, `paused` or `window-closed`; the step
  saves training state when it can and releases as `cancelled`. Three missed beats reap the lease: the step fails with
  error type `lost` (retryable) and the card slot frees. Card telemetry (memory used by every process, resident
  services included, utilisation, temperature, power) feeds the `gpu` topic and compute health.
- Completion: `release` sends state, outputs (`hash`, `type`, `size`, neutral `meta`), final metrics, or an error of
  type `oom`, `step`, `lost`, `cancelled` or `input`. The control plane checks every output hash is in the store,
  records the artifacts, runs the output hooks (`dataset`, `checkpoint`, `calibration`) in the transaction that marks
  the step `done`, and advances the pipeline. `oom` gets one automatic retry with `batchScale` 0.75.
- Secrets: a step kind declares secret names; at lease time the control plane reads the values from the secret store
  (R9) and puts them in the lease's `env` for that subprocess only. They never appear in the spec, job rows, events,
  logs, artifacts or an agent context; the worker neither logs nor writes them.
- Tracing: the job's `traceparent` reaches the subprocess, so one trace runs UI → API → job → step.

## Artifacts, metrics and logs

Step outputs are content-addressed blobs; metrics are rows; logs are files. The API is the only way the UI and agents
read any of them (R15).

- Content store: `CADENCE_CAS_DIR` (default `$CADENCE_DATA_DIR/cas`; `/var/lib/cadence/cas` in compose) on the
  staging host's NVMe, shared by volume with the worker in v1. An artifact is addressed by `b3:<64 hex>`, the BLAKE3-256
  of its bytes, and lives at `cas/b3/<first two hex>/<64 hex>`. Writers stream into `cas/tmp/` and rename, so a blob
  exists whole or not at all; blobs are immutable and writing one twice is a no-op; every write verifies the hash.
- Directory artifacts (a Shar set, a checkpoint directory): a manifest `{files: [{path, hash, size}]}`, canonical JSON
  sorted by path, stored as a blob whose hash is the artifact's hash; each file is its own blob, so equal files are
  stored once.
- The `artifacts` table records hash, type, size, neutral metadata (R42) and the producing pipeline step (with its
  pipeline run and project); registry versions, checkpoints and eval records reference artifacts by hash.
  `artifacts.get` answers metadata and manifest; panels read typed views through their entity's operations, never
  paths in the store.
- The next step reads an artifact through its lease (`cas://` URI). A step whose `kind@version`, resolved parameters
  and input hashes equal a finished step's reuses that step's outputs instead of running.
- Tiers: before mounts exist the store is the only tier; mounts (phase 4) become further tiers behind the same hash,
  and remote workers read and write by hash over HTTP. v1 deletes no blob.
- Metrics: one Postgres table `metrics` (job, pipeline step, run when there is one, name, step, epoch, value, wall
  time), indexed by run, name and step; thousands of points per run need no TSDB. Points arrive from
  `workerMetrics.new`, stream on `run.{id}.metrics`, and `metrics.get` answers series binned for charts (R53). Points
  live as long as their run.
- Logs: one NDJSON file per job under the data directory (`job-logs/<jobId>.ndjson`; `logs/` holds the control plane's own log files), lines `{t, level, msg, fields}`; they tail on
  `job.{id}.log`. Field search is job-scoped in v1; `warn` and above are indexed for global search. Files are deleted
  14 days after the job ends.

## Operations

Cadence upgrades itself the way it upgrades models: versioned, forward-only, with a nightly backup that is restored on a schedule to prove it works.

| Concern | Rule |
| --- | --- |
| Versioning | Cadence releases are semver; each release pins every runtime image by digest (v1: NeMo Speech 26.07), Dockview and the agent adapters, and states the matrix in the release notes (a help article) |
| Install | `docker compose up`; the first start creates the admin account and the default mounts |
| Migrations | Embedded in the binary, forward-only, expand-and-contract, run at start under an advisory lock; data migrations run as jobs with progress events |
| Upgrade | Pull the release, `compose up`; a failed migration stops the start and leaves the previous image runnable; rollback is the previous image plus, if data changed, the last backup |
| Backups | Nightly `pg_dump` and content-store sync to a mount; a weekly automated restore into a scratch database with a report; targets: 24 h RPO, 1 h RTO |
| Failures | River retries with backoff; a worker heartbeat every 10 s, leases reaped after three missed beats (step error `lost`); an OOM gets one automatic retry at 0.75× batch; a full cache pauses freezes; an unhealthy card closes its slot — every case is an event, so it notifies |
| Availability windows | Each compute card has windows per job kind (training, eval, shadow, export; weekly, local time; none means always open, the default). The queue starts a job only if its estimate fits before the window closes; a job without an estimate starts in any open window. Training saves a checkpoint and its training state every 20 minutes; at a close the heartbeat answers `stop: window-closed`, the step saves and releases, and the job waits for the next window and resumes from the last training state (`resumeFrom`). The same path makes long runs preemption-safe on the shared staging card (R19) |
| Health | `/healthz` on the control plane, worker heartbeat, mount checks; a status card in Settings; a Prometheus endpoint |
| Retention | Job log files are deleted 14 days after the job ends; metric points live as long as their run; content-store blobs are kept (v1); the audit log is kept one year; production audio follows the retention policy |

## Notifications

Two channels — the in-app history and a Telegram bot — one routing table by event class, and approvals that can be decided from the phone with the same audit trail as from the UI.

| Event class | In-app | Telegram | Timing |
| --- | --- | --- | --- |
| Approval requested (agent, automation, registry) | Yes | Message with inline Approve / Deny buttons and the estimate | Immediate |
| Job failed, mount unhealthy, card closed, backup failed | Yes | Yes | Immediate |
| Gate verdict, promotion, schedule finished, batch closed | Yes | Yes | Immediate |
| Progress (step done, checkpoint saved, triage item added) | Yes | No | — |
| Daily digest: runs, evals, spend against budgets, open approvals | Yes | Yes | 09:00 local |

- The bot talks only to allow-listed chat ids; every inline action carries a single-use signed token, and an approval from Telegram is recorded with actor and channel like any other.
- Quiet hours suppress Telegram except failures; reviewers get batch-assigned and batch-closing messages only.
- Routing rules are a Settings table; each row is an event class, a channel set and a timing; email is a later channel behind the same table.

## Testing strategy

Every layer has a test that runs on every change, the smoke project is the nightly end-to-end, and the agents themselves are tested against fixtures the same way code is.

| Layer | What | When |
| --- | --- | --- |
| Unit | Go packages, TypeScript shell (computeSnap, registries, migrations), Python step kinds with tiny fixtures | Every pull request |
| Contract | OpenAPI ↔ generated server, client and MCP tools; the Dockview adapter against the pinned version; workspace round-trips | Every pull request |
| Integration | Control plane with Postgres and a worker stub: commands, outbox, SSE resume, approvals, tokens | Every pull request |
| UI | Playwright on the shell (drag, dock, float, popout, palette) and on the document anatomy of each panel | Every pull request |
| Audio and charts | The audio view's FFT against librosa on fixtures (≤ 0.5 dB); track and chart screenshots in both themes; chart palettes in the contrast and colour-vision checks (R51–R53) | Every pull request |
| Framework conformance | Each framework pack on fixtures: calibrate → train a few steps → average → transcribe (file and streaming) → export → parity → score (R45); the CPU `toy` pack keeps the seams honest | Toy pack every pull request; NeMo pack nightly on the staging card |
| Agent evals | Skills and playbooks executed by both agents on a fixture project; pass criteria are the expected tool calls and outcomes, not the wording (`agent-host/evals`, `make evals`) | Offline with a scripted agent on every pull request; live with both drivers nightly on the staging host and on skill changes |
| End to end | The smoke project on the staging card: ingest, freeze, 300 steps, eval, gate, export, parity | Nightly |
| Performance | Workspace restore, drag frame time, SSE fan-out, search latency against the budgets | Weekly |

Fixtures are tiny public subsets (FLEURS, Common Voice) kept in the repository; nothing in CI touches production data or the production card.
