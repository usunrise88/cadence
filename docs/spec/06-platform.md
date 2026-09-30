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
| `job.{id}`, `job.{id}.log` | Job state and progress (`job.state_changed`, `job.progress`); log lines from workers (`job.log`, ≤ 200 lines per event) |
| `pipeline_run.{id}` | `pipeline_run.started`, `pipeline_run.step_changed`, `pipeline_run.state_changed` |
| `run.{id}.status`, `run.{id}.metrics` | Training run state; metric points (`run.metrics`) |
| `eval.{id}.progress` | Cells completed, utterances scored |
| `deploy.{id}`, `shadow.{deployment}` | Deployment stage changes; divergence samples |
| `queue`, `gpu`, `mount.{id}` | Queue changes (`queue.changed` with `change`); card telemetry (`gpu.telemetry`, ≤ 1 per 5 s per host); mount health |
| `triage.new`, `approvals` | New triage items; approval requests and decisions |
| `agent.session.{id}`, `agent.sessions` | One transcript; the session list |
| `recipe.{path}` | File changes in the project repository: commits (`recipe.changed`) and, from the agent host's worktree watcher, a running turn's uncommitted edits (`recipe.working`) |
| `compute.{id}` | Host health (`compute.health`, on a state change only) and workers registering (`worker.registered`); host edits go on `entity.compute.{id}` |
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
  worker token reaches nothing else, and no other credential reaches these paths. A token names its compute host
  (the credential's subject): a worker registers, claims and reports only for that host (`403 forbidden` otherwise).
- Registration: the worker publishes its host, its `instance` (hostname, pid and boot id), its runtime (name, version,
  image, digest, environment lock, plugin version), every step kind it carries (`StepKindDescriptor`: version,
  parameter schema with `x-cadence`, `consumes` and `produces` by artifact type, resources, family role or `neutral`,
  secret names, help slug) and its model families. Each is stored as a frozen registry version of kind `runtime`,
  `step_kind` or `model_family` (collections `runtime/<name>`, `step-kind/<name>`, `model-family/<name>`; a step-kind
  payload adds `name`, `runtime`, `runtimeVersionId` and `schemaHash`, the sha256 of the canonical parameter schema),
  and equal content reuses its version, so a restart with the same pack changes nothing and a changed pack is a new
  version. Refused: a parameter without complete `x-cadence` (`validation-failed`), a framework kind `name@version`
  another runtime already publishes, a neutral kind whose schema hash differs (`step-kind-conflict`). There is one
  worker row (`wrk_`) per runtime and host; a registration with a new `instance` reaps the old process's leases at
  once. `runtimes.list|get` (with the workers that registered each version), `stepKinds.list|get` and
  `modelFamilies.list|get` read them.
- Scheduling (`internal/queue`, pure functions; `internal/workers` applies them under per-card row locks): a claim
  gets a waiting step job only when the worker published its pinned `kind@version` (neutral core kinds match in any
  runtime, since their schema hashes must agree), the job is neither paused nor cancelled, and a card of the worker's
  host fits it: the card allows the job kind, holds no other training job when this is one, has the reservation left
  under its memory cap, shows that much free memory in the last telemetry when no Cadence step runs on it (1 GB
  slack for resident services and the driver), and the kind's availability window is open with the estimate ending
  before it closes (R19). The reservation is the step's declared `memoryGb`, or the card's whole remaining cap when it
  declares none, so a training step takes the card alone. Candidates are taken by the project's queue priority (higher first;
  `budgets.queuePriority`, read live from the project so `projects.edit` reorders waiting jobs; a job without a project
  takes the `defaults.yaml` default), then the job's priority (higher first; set from the pipeline run's `priority`,
  changed with `jobs.edit`), then first come. Card slots (`card_slots`) belong to the control
  plane per host and card, not per worker, so two runtimes never double-book a card. A step with `gpu: false` takes
  no card (lease card index -1) and may go to a worker that reported no cards (the CPU toy runtime).
- Lease: `lse_` id, the job id, the step spec (resolved parameters, input artifact refs, output types, resources,
  priority, estimate, `overrides.batchScale` and `resumeFrom`, attempt, the run id when there is one), input URIs
  (`cas://b3:<hash>`), the card index and memory cap in MB, the secret environment, the job's `traceparent` and
  `heartbeatSeconds` (10).
- Lifecycle: the `step` job's River handler (its own River queue, `steps`) calls `steps.Leases.Await`, which puts the
  job in the queue table `step_jobs` (`waiting → leased → ended`) and blocks until the step ends. The job mirror is
  `running` from the moment its handler starts, also while it waits for a card; the Queue shows `waiting`, `paused`,
  `running` or `stopping`; the pipeline step turns `running` when the lease is granted
  (`pipelines.Engine.Leased`). A lease is `active`, then `released` or `reaped`; a report or release on an ended lease
  answers `409 lease-ended` and the worker stops the step. The worker runs each step as a subprocess
  (`python -m cadence_worker.run_step`) in its own scratch directory on the store's file system
  (`CADENCE_WORKER_SCRATCH`), materialises inputs there by hard links, sees its card as device 0
  (`CUDA_VISIBLE_DEVICES`), gets the cap as `CADENCE_MEMORY_CAP_MB` and, when torch is present,
  `set_per_process_memory_fraction` of cap ÷ card memory (0.5 on the staging card). A worker runs at most
  `CADENCE_WORKER_MAX_LEASES` (2) leases at once; the control plane decides what fits on a card.
- Heartbeats: `report` every 10 s (progress → `job.progress`, telemetry → `card_slots`, the `gpu` topic at most once
  per 5 s per host, and compute health). The answer `stop: true` carries `cancelled`, `paused` or `window-closed`
  (training only; other kinds run on past a close); the step gets SIGTERM, sees `should_stop()`, saves its training
  state when it can and releases as `cancelled` with it; after `CADENCE_STOP_GRACE_SECONDS` (60) its process group is
  killed. A pause or a window close puts the job back to `waiting` in its place with `overrides.resumeFrom` set to
  that `training-state` output. Three missed beats reap the lease (a periodic reaper every 10 s): the step fails with
  error type `lost` (retryable) and the card slot frees; a host whose workers all went quiet turns `unreachable`.
  Card telemetry is memory used by every process (resident services included), utilisation, temperature and power.
- Completion: `release` sends state, outputs (`hash`, `type`, `size`, neutral `meta` with `layout: file|dir`), final
  metrics, or an error of type `oom`, `step`, `lost`, `cancelled` or `input`. The control plane checks every output
  hash is in the store (`artifact-missing`), and the pipeline engine records the artifacts, runs the output hooks in
  the transaction that marks the step `done` (`dataset` in phase 2 wave 1; `checkpoint` and `calibration` arrive with
  runs) and advances the pipeline. `oom` gets one automatic retry with `batchScale` 0.75, `lost` one retry.
- Secrets: a step kind declares secret names; at lease time the control plane reads the values from the secret store
  (R9) and puts them in the lease's `env` for that subprocess only, named in upper case with `-` and `.` as `_`
  (`hf-token` → `HF_TOKEN`); a missing secret fails the step at lease time with error type `input`. Values never
  appear in the spec, job rows, events, logs, artifacts or an agent context; the worker redacts them from forwarded
  logs and removes its own token and URL from the step's environment.
- Tracing: the job's `traceparent` reaches the subprocess, so one trace runs UI → API → job → step.
- Worker configuration (`worker/cadence_worker/config.py`): `CADENCE_URL`, `CADENCE_WORKER_TOKEN_FILE` (re-read on
  every call), `CADENCE_CAS_DIR`, `CADENCE_WORKER_HOST`, `CADENCE_WORKER_SCRATCH`, `CADENCE_RUNTIME` or
  `CADENCE_RUNTIME_FILE` (the runtime descriptor baked into the image), `CADENCE_WORKER_GPU`, `CADENCE_CLAIM_WAIT_SECONDS`
  (20). Compose runs one service per runtime: `worker` (profile `gpu`, runtime `nemo-speech`, the NVIDIA device) and
  `worker-toy` (profile `toy`, CPU), both on the `artifacts` volume at `/var/lib/cadence` with the control plane.

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
- The `artifacts` table (migration 0013) records hash, type, size, `directory`, neutral metadata (R42), the first
  producer's project (none for a registry artifact) and its producing pipeline step; the row is written once, and
  `artifact_projects` links every project that later produced or consumed it, so a project-scoped credential reads the
  artifacts linked to its project and registry read covers the rest. A directory artifact's size is the sum of its
  files. Registry versions, checkpoints and eval records reference artifacts by hash. `artifacts.get` answers
  metadata, the producer and a directory's files, and with `content=true` (plus `path` for one file of a directory)
  content of at most 1 MiB inline; panels read typed views through their entity's operations, never paths in the
  store.
- The next step reads an artifact through its lease (`cas://` URI). A step whose `kind@version`, resolved parameters
  and input hashes equal a finished step's in the same project reuses that step's outputs instead of running (unless
  the run asks for `fresh`); output hooks run for reused outputs too, so they are idempotent per artifact hash.
- Tiers: before mounts exist the store is the only tier; mounts (phase 4) become further tiers behind the same hash.
  A worker without the shared volume uploads by hash (`workerArtifacts.set`, verified: `artifact-hash-mismatch`); a
  download path for remote workers comes with them. v1 deletes no blob; the backup mirror copies each new blob once.
- Metrics: one Postgres table `metric_points` (migration 0012: job, run when there is one, pipeline step, project,
  name, optimiser step, epoch, value, wall time), indexed by (run, name, step) and (job, name, step); thousands of
  points per run need no TSDB. Points arrive from `workerMetrics.new` (≤ 5 000 per batch) and stream as `run.metrics`
  on `run.{id}.metrics` when the step belongs to a run; `internal/telemetry.Get` answers one series per metric in step
  order thinned evenly to a maximum number of points (first and last kept), which `metrics.get` exposes with runs
  (phase 2 wave 2, R53). Points are never deleted in v1: they live as long as their run.
- Logs: one NDJSON file per job, `$CADENCE_DATA_DIR/job-logs/<jobId>.ndjson` (`logs/` holds the control plane's own
  log files), lines `{t, level, msg, fields}` (`msg` ≤ 16 000 characters, ≤ 1 MiB per `workerLogs.new`); each request
  becomes `job.log` events on `job.{id}.log` of at most 200 lines. `jobLogs.list` reads a job's lines with their line
  numbers, filtered by minimum level and message text, paging with `after` or reading the `tail`. Field search and a
  global search index of `warn`+ lines (R15) are not built yet. A daily chore deletes log files untouched for 14 days.

## Operations

Cadence upgrades itself the way it upgrades models: versioned, forward-only, with a nightly backup that is restored on a schedule to prove it works.

| Concern | Rule |
| --- | --- |
| Versioning | Cadence releases are semver; each release pins every runtime image by digest (v1: NeMo Speech 26.07), Dockview and the agent adapters, and states the matrix in the release notes (a help article) |
| Install | `docker compose up`; the first start creates the admin account and the default mounts |
| Migrations | Embedded in the binary, forward-only, expand-and-contract, run at start under an advisory lock; data migrations run as jobs with progress events |
| Upgrade | Pull the release, `compose up`; a failed migration stops the start and leaves the previous image runnable; rollback is the previous image plus, if data changed, the last backup |
| Backups | Nightly `pg_dump` and a content-store mirror into `CADENCE_BACKUP_DIR` (the `cadence-backups` volume; a mount from phase 4); a weekly automated restore into a scratch database with a report; targets: 24 h RPO, 1 h RTO (as built below) |
| Failures | River retries with backoff; a worker heartbeat every 10 s, leases reaped after three missed beats (step error `lost`, one retry); an OOM gets one automatic retry at 0.75× batch; a host whose workers went quiet turns `unreachable` (`compute.health`); a full cache pauses freezes (phase 4); an unhealthy card closes its slot (not built: card health is per host today) — every case is an event, so it notifies |
| Availability windows | Each compute card has windows per job kind (training, eval, shadow, export, data; none means always open, the default): each window is a set of weekdays, an opening and a closing time `HH:MM` (an end at or before the start closes the next day, `24:00` is midnight; a window past midnight belongs to the day it opens) and an IANA time zone per window (default UTC), edited with `compute.edit`. The queue starts a job only if its estimate fits before the window closes; a job without an estimate, or one resuming from a training state, starts in any open window. Training saves a checkpoint and its training state every 20 minutes (the training step's duty; the NeMo pack's); at a close the heartbeat answers `stop: window-closed` to training steps only (other kinds finish), the step saves and releases, and the job waits in its place for the next window and resumes from the last training state (`resumeFrom`). The same path makes long runs preemption-safe on the shared staging card (R19) |
| Health | `/healthz` on the control plane, worker heartbeat, mount checks; a status card in Settings; a Prometheus endpoint |
| Retention | Job log files are deleted 14 days after their last line (a daily chore); metric points live as long as their run; content-store blobs are kept (v1); the audit log is kept one year; production audio follows the retention policy |

Phase 2 as built (2026-09-30, stream O):

- **Backups** (`internal/backups`, migration 0015 `backups`): a set is `CADENCE_BACKUP_DIR/sets/<time>-<trigger>-<id>/`
  with `cadence.dump` (`pg_dump --format=custom` on a snapshot exported by a repeatable-read transaction that also
  records the key tables' row counts and the latest migration), the sealed secret values (never the master key) and
  `manifest.json`; content-store blobs are mirrored once into `CADENCE_BACKUP_DIR/cas/` (new blobs only — they are
  immutable) and never pruned. Jobs `backup` and `backup.restore_test`; a periodic `backups.schedule` enqueues the
  nightly set at `backups.nightly_at` (the set of the restore-test weekday is the weekly one) and the restore test at
  `backups.restore_test_weekday` + `restore_test_at`, local to `policies.timezone`, catching up after downtime. The
  restore test creates a scratch database on the same server, `pg_restore --no-owner --no-privileges
  --exit-on-error`, compares row counts and the migration version with the manifest, re-hashes up to 20 mirrored
  blobs, drops the scratch database and stores the report on the set. Retention keeps `backups.keep_nightly` (7)
  nightly and manual sets and `backups.keep_weekly` (4) weekly sets; older sets lose their files, the rows stay
  (`prunedAt`). Events `backup.queued|started|succeeded|failed|restore_queued|restore_passed|restore_failed|pruned` on
  `backups` and `entity.backup.{id}`. API `backups.list|get|new|verify` (admin). The control-plane image ships
  `postgresql-client-17`; `CADENCE_PG_DUMP` / `CADENCE_PG_RESTORE` override the commands, and a client older than the
  server fails the set with the version to install. Compose mounts the `cadence-backups` volume at `/backups`.
- **Retention**: the audit log is pruned daily after one year (`audit.prune`, phase 1); job log files untouched for
  14 days are deleted by a daily chore (`workers.PruneLogs`); no metric point and no content-store blob is deleted.
- **Upgrade**: `docs/help/guides/upgrading.md` (pull, compose up, migrations under the advisory lock, rollback = the
  previous image plus the last backup, the release matrix); restoring by hand is in `docs/help/guides/backups.md`.

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

Phase 2 as built (2026-09-30, stream O):

- **Routing table** (`notification_rules`, ids `ntr_<class>`, seeded with the table above): classes
  `approval_requested`, `failure` (the only class that ignores quiet hours), `outcome`, `progress`, `digest`; channels
  `inApp` and `telegram`; timing `immediate | digest | daily | none`, which applies to Telegram (the in-app history is
  always immediate; its checkbox switches a class off). `digest` holds an event for the next digest; `daily` belongs to
  the digest row only; progress may reach Telegram only through the digest. `notificationRules.list|edit`.
- **Settings** (`notification_settings`): quiet hours (off; 22:00–08:00), digest time (09:00), the allow-listed chat
  ids (≤ 20), chats that wrote to the bot without being allowed, and the bot's status (username, last error, last
  message, polling). Times are local to `policies.timezone` (new; `defaults.yaml` `operations.timezone` = UTC).
  `notificationSettings.get|edit`; the token is the secret `telegram-bot-token` (kind `telegram`), set write-only by
  `telegramBot.set` (If-Match on the settings once one exists); `telegramBot.verify` runs `getMe` and sends a test
  message.
- **Router, sender, poller** (`internal/notify`): the router reads the outbox after its own cursor (`event_cursors`
  `notify`, started at the end of the outbox so history is never sent), classifies each event and writes a
  `notification_deliveries` row per Telegram message in the transaction that advances the cursor (`queued`,
  `suppressed` in quiet hours, `digest`); the sender delivers queued rows to every allowed chat with retries (5
  attempts, exponential backoff); the poller long-polls `getUpdates` (no webhook; `CADENCE_TELEGRAM_API` sets the Bot
  API base URL, a fake server in tests). The in-app history stays the web shell's, built from the event stream and
  filtered by the same table (`web/src/shell/notifications/classes.ts` mirrors the classification).
- **Approve / Deny from the phone**: each button's `callback_data` is `<a|d>.<id>.<mac>` (41 bytes): a 16-character
  random id stored in `notification_tokens` with the approval and its expiry, and HMAC-SHA256 over action and id
  under a key derived from the master key. A press from an allowed chat verifies the MAC, spends the token and its
  sibling (single use), then runs `approvals.approve|deny` through the command pipeline as the admin with
  `actor.channel = telegram` (new contract field), so the approval's `decidedBy`, the audit row and the replay are
  the same as from the UI; the message loses its buttons and says who decided. Tampered, spent, expired or
  foreign-chat presses decide nothing.
- **Digest**: a periodic job sends it once a day after the digest time (`digest_sent_on`): jobs of the last 24 h by
  kind (runs and evals are jobs until their entities land), each project's GPU-hours against its daily budget
  (`notify.SpendFunc`; "not metered yet" until stream R plugs in the meter), open approvals, events held for the
  digest and the last backup; in-app as `notification.digest` on `notifications`, on Telegram as a message.
- Classification (`internal/notify/classify.go`): approvals on `approvals`; a job's `job.state_changed` on its job
  topic (`failed` → failure, `done` → progress; step jobs included); backups by type. The table also names types no
  stream emits yet — `mount.unhealthy`, `gate.verdict`, `deployment.promoted`, `schedule.finished`, `batch.closed`,
  `checkpoint.saved`, `triage.item_added` arrive with their phases; `compute.card_closed` and `pipeline_step.done`
  have no emitter (the engine emits `pipeline_run.step_changed`, the worker protocol `compute.health` with state
  `unreachable`, and neither is classified).
- The control plane reaches `api.telegram.org` over the compose `default` network (not internal); nothing else is
  needed. Reviewer messages (batch assigned/closing) arrive with batches in phase 4.

## Testing strategy

Every layer has a test that runs on every change, the smoke project is the nightly end-to-end, and the agents themselves are tested against fixtures the same way code is.

| Layer | What | When |
| --- | --- | --- |
| Unit | Go packages, TypeScript shell (computeSnap, registries, migrations), Python step kinds with tiny fixtures | Every pull request |
| Contract | OpenAPI ↔ generated server, client and MCP tools; the Dockview adapter against the pinned version; workspace round-trips | Every pull request |
| Integration | Control plane with Postgres and a worker stub: commands, outbox, SSE resume, approvals, tokens | Every pull request |
| UI | Playwright on the shell (drag, dock, float, popout, palette) and on the document anatomy of each panel | Every pull request |
| Audio and charts | The audio view's FFT against librosa on fixtures (≤ 0.5 dB); track and chart screenshots in both themes; chart palettes in the contrast and colour-vision checks (R51–R53) | Every pull request |
| Framework conformance | Each framework pack on fixtures through the real harness path with a local store (`python -m cadence_worker.conformance --runtime <runtime>`, `make conformance`): schemas (complete `x-cadence`, help, declared profiles, every role mapped), then `dataset_import` of the fixtures → calibrate → train a few steps → stop → resume → average → transcribe (every latency profile; partial events for streaming ones) → score; export and parity join in phase 5 (R45). The CPU `toy` pack keeps the seams honest | Toy pack every pull request; NeMo pack nightly on the staging card |
| Agent evals | Skills and playbooks executed by both agents on a fixture project; pass criteria are the expected tool calls and outcomes, not the wording (`agent-host/evals`, `make evals`) | Offline with a scripted agent on every pull request; live with both drivers nightly on the staging host and on skill changes |
| End to end | The smoke project on the staging card: ingest, freeze, 300 steps, eval, gate, export, parity | Nightly |
| Performance | Workspace restore, drag frame time, SSE fan-out, search latency against the budgets | Weekly |

Fixtures are tiny public subsets (FLEURS, Common Voice) kept in the repository; nothing in CI touches production data or the production card.
