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
- Audio is not an event. The manual transcription test (R48) is the one media channel: a WebSocket per session carries audio up and words down, relayed by the control plane to a worker job; the job reports its state on `job.{id}` like any job, and nothing of the session is stored ("Media: audio and the live channel" below).

Topic scheme, canonical for both tabs:

| Topic | Carries |
| --- | --- |
| `entity.{kind}.{id}` | Revision changes of one entity, including drafts |
| `job.{id}`, `job.{id}.log` | Job state and progress (`job.state_changed`, `job.progress`); log lines from workers (`job.log`, ≤ 200 lines per event) |
| `pipeline_run.{id}` | `pipeline_run.started`, `pipeline_run.step_changed`, `pipeline_run.state_changed` |
| `run.{id}.status`, `run.{id}.metrics` | Training run state; metric points (`run.metrics`) |
| `eval.{id}.progress`, `entity.eval.{id}` | Cells done and total and the eval's state (phase 3); the eval's revision changes, its gate verdict included |
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
- Reviewers as built (phase 4, stream A; `internal/credentials/invitations.go`, migration 0038): `users.role` is
  `admin` or `reviewer`. `invitations.new` (admin; `POST /batches/{id}/invitations`, role `annotator` or
  `adjudicator`) creates the reviewer by name when needed (no password) and issues a credential of kind `invitation`
  (token `cri_`, shown once, its subject the batch) that expires at the batch's due date, after
  `annotation.invitation_max_days` (14) at most, and when the batch freezes. `auth.accept` (`POST /auth:accept`, tag
  `auth`) redeems the token for a browser session that never slides and ends with the invitation. A reviewer's
  session reaches only `/auth`, help, `/defaults` (the audio view's settings), its batch's document, items and
  annotations, and the media of that batch's items (`bit_…` ids); everything else answers `403 forbidden`. Audio
  plays through the signed links below, with no download link.

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
| `workerOutputs.new` | `POST /worker-leases/{id}/worker-outputs` | An intermediate output during the lease (a validation checkpoint), recorded and hooked at once |
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
  under its memory cap (`interactive` jobs, phase 3, share a card with training but never with a benchmark; "Media"
  below), shows that much free memory in the last telemetry when no Cadence step runs on it (1 GB
  slack for resident services and the driver), and the kind's availability window is open with the estimate ending
  before it closes (R19). The reservation is the step's declared `memoryGb`, or the card's whole remaining cap when it
  declares none, so a training step takes the card alone. Candidates are taken by the project's queue priority (higher first;
  `budgets.queuePriority`, read live from the project so `projects.edit` reorders waiting jobs; a job without a project
  takes the `defaults.yaml` default), then the job's priority (higher first; set from the pipeline run's `priority`,
  changed with `jobs.edit`), then first come; the claim reads them in pages of 50 (at most 20 pages per attempt)
  until one fits, so waiting jobs that cannot fit never hide one further down that can. Card slots (`card_slots`) belong to the control
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
- Heartbeats: `report` every 10 s (progress → `job.progress`, which leaves the job's `rev` alone so a queue command
  sent with a revision read a moment ago does not race the heartbeats; telemetry → `card_slots`, the `gpu` topic at most once
  per 5 s per host, and compute health). The answer `stop: true` carries `cancelled`, `paused` or `window-closed`
  (training only; other kinds run on past a close); the step gets SIGTERM, sees `should_stop()`, saves its training
  state when it can and releases as `cancelled` with it; after `CADENCE_STOP_GRACE_SECONDS` (60) its process group is
  killed. A pause or a window close puts the job back to `waiting` in its place with `overrides.resumeFrom` set to
  that `training-state` output. Three missed beats reap the lease (a periodic reaper every 10 s): the step fails with
  error type `lost` (retryable) and the card slot frees; a host whose workers all went quiet turns `unreachable`.
  A control plane that stops while a step job waits on a worker does not fail it: the River job is snoozed with its
  attempt kept, the lease runs on, and the next start waits for the same lease's outcome (a re-lease of a running
  step, after a pause or a window close, re-derives the run's status).
  Card telemetry is memory used by every process (resident services included), utilisation, temperature and power.
- Completion: `release` sends state, outputs (`hash`, `type`, `size`, neutral `meta` with `layout: file|dir`), final
  metrics, or an error of type `oom`, `step`, `lost`, `cancelled` or `input`. The control plane checks every output
  hash is in the store (`artifact-missing`), and the pipeline engine records the artifacts, runs the output hooks in
  the transaction that marks the step `done` (`dataset` in phase 2 wave 1; `checkpoint` and `calibration` arrive with
  runs) and advances the pipeline. `oom` gets one automatic retry at 0.75× the failed attempt's `batchScale` (so after
  a manual retry at 0.5 it runs at 0.375, never back at 0.75), `lost` one retry.
- Intermediate outputs: a step may publish instances of its outputs while it runs (`ctx.publish(output, path, meta,
  metrics)`; a training step publishes every validation's checkpoint). The harness hashes the path into the store,
  removes it from the scratch directory and sends `workerOutputs.new` (`name` = one of the step's outputs, the artifact
  of that output's type, optional metrics) on its own thread, in order, before the release. The control plane checks
  the lease is active, the name and type match the step spec and the blob is in the store (`artifact-missing`), then
  records the artifact (producer: the pipeline step and output name) and runs the output hooks of its type in that
  request's own transaction (`pipelines.Engine.Published`): a checkpoint registers on its run and re-ranks the top k
  at once, so it survives a pause, a window close, a failure or a reaped lease. Hooks are idempotent per hash, so a
  retried publication or a final output equal to a published one registers once. The release's outputs stay the
  step's outputs (what the next step reads); a failed publication is a warning in the job log, not a step failure.
- Secrets: a step kind declares secret names; at lease time the control plane reads the values from the secret store
  (R9) and puts them in the lease's `env` for that subprocess only, named in upper case with `-` and `.` as `_`
  (`hf-token` → `HF_TOKEN`); a missing secret fails the step at lease time with error type `input`, and so does a
  secret whose scope does not allow the step's project (a `project:<slug>` secret serves only that project's steps;
  `instance` serves all, including steps without a project). Values never
  appear in the spec, job rows, events, logs, artifacts or an agent context; the worker redacts them from forwarded
  logs (a mount's `<accessKeyId>:<secretAccessKey>` also half by half, audit F1) and removes its own token and URL
  from the step's environment.
- Tracing: one trace runs UI → API → job → step. A job keeps the traceparent of the request that enqueued it (River
  args) and each attempt runs in a `job <kind>` span continuing it (file traces, `traces.jsonl`); the step handler
  stores that span's traceparent on the step job (`step_jobs.traceparent`) and the lease hands it to the worker (a
  job queued without a span gets one derived from the job and lease ids). The worker opens a `step <kind>@<version>`
  span under it, passes it to the subprocess as `TRACEPARENT`, adds `trace_id` and `span_id` to the fields of every
  log line it forwards, and appends the finished span as a JSON line to `CADENCE_WORKER_TRACE_FILE` when set.
- Worker configuration (`worker/cadence_worker/config.py`): `CADENCE_URL`, `CADENCE_WORKER_TOKEN_FILE` (re-read on
  every call), `CADENCE_CAS_DIR`, `CADENCE_WORKER_HOST`, `CADENCE_WORKER_SCRATCH`, `CADENCE_RUNTIME` or
  `CADENCE_RUNTIME_FILE` (the runtime descriptor baked into the image), `CADENCE_WORKER_GPU`, `CADENCE_CLAIM_WAIT_SECONDS`
  (20). Compose runs one service per runtime: `worker` (profile `gpu`, runtime `nemo-speech`, the NVIDIA device) and
  `worker-toy` (profile `toy`, CPU), both on the `artifacts` volume at `/var/lib/cadence` with the control plane.
- Mounts in the lease (phase 4, stream M): every lease carries the registered mounts (`lease.mounts`, schema
  `LeaseMount`: name, kind, root, readOnly and, for `s3` and `hf`, endpoint, region, revision and the env variable
  holding the credentials when the step may read them: it names the mount, or a step that produced one of its
  inputs did, back through their inputs — never every data step; audit F1). The harness resolves `mount://<mount>/<path>[#t=<start>,<end>]
  [&ch=<n>]` to a local path (`cadence_worker.mounts`; `CADENCE_MOUNTS` for helper processes): `local`, `nfs` and
  `smb` read in place under `root` (compose binds `${CADENCE_CORPORA_DIR}` → `/mnt/corpora`, read-only, and
  `${CADENCE_EXPORTS_DIR}` → `/mnt/exports` into the control plane and the `worker` and `worker-toy` services); `s3`
  and `hf` download once into the mount cache. Mount health is the core step `mount_check@1` (reachable, free space,
  a `storage.mount_check_sample_mb` throughput sample), run by `mounts.verify`, at registration and every
  `storage.mount_check_hours` (6); health is per mount, not per host.
- Further runtimes (phase 4; decision log in 00): `worker-services` (profile `services`, `worker/Dockerfile.services`,
  runtime `services`, CPU, no model) runs step kinds that call a running service an auxiliary names —
  `oasis_transcribe@1` dials `host.docker.internal:50051` through `extra_hosts: host-gateway` and checks
  `GetModelInfo` first (`auxiliary-unavailable`); Cadence never starts the service. `worker-omni` (profile `omni`,
  `worker/Dockerfile.omni`, runtime `omni`, GPU) is python 3.12 slim with PyTorch 2.8 (CUDA 12.8), torchaudio 2.8 and
  fairseq2 0.6 (≈ 11.6 GB) for `align_reference@1`, because fairseq2 needs torch 2.8 and the NeMo Speech image ships
  2.12 (R45's one-off allowance; the model loads per job, about 2 GB on the card). Neither binds the corpora mount yet.

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
- The next step reads an artifact through its lease (`cas://` URI). A step whose `kind@version`, runtime version, resolved parameters
  and input hashes equal a finished step's in the same project reuses that step's outputs instead of running (unless
  the run asks for `fresh`); output hooks run for reused outputs too, so they are idempotent per artifact hash.
- Tiers: before mounts exist the store is the only tier; mounts (phase 4) become further tiers behind the same hash. As built (phase 4, stream M; `internal/cache`, `internal/mounts`, migration 0033): a blob copy on a mount is a `blob_copies` row; the store is the local cache tier, accounted by `storage.get`, and only dataset shards with a mount copy are evicted (02 "Storage and mounts").
  A worker without the shared volume uploads by hash (`workerArtifacts.set`, verified: `artifact-hash-mismatch`); a
  download path for remote workers comes with them. Only `artifacts.evict` deletes blobs (Retention below); the backup mirror copies each new blob once.
- Retention (design 2026-10-01, built 2026-10-01 by stream E — "as built" at the end of this list): training states are large (7.66 GB for the 0.6B model, one per pause,
  window close and `state_every_minutes`) and only ever read to resume, so they are the first thing to reclaim.
  - Evictable: a `training-state` artifact whose run has ended and that nothing can resume from any more — every
    state of a run whose train step finished (`done`, a final checkpoint registered), and every state but the newest
    of a run that ended `cancelled` or `failed` (the newest stays for `runs.resume`). Never evictable: a state named
    by a waiting or leased step job's `overrides.resumeFrom`, or produced by a pipeline run still running.
  - Shared files: a directory artifact's files are blobs other artifacts may list too, so eviction needs a file
    index (`artifact_files (hash, file_hash)`, filled by `artifacts.Record` and backfilled once from the manifests);
    a file blob is deleted only when no artifact outside the eviction set lists it, and "bytes freed" counts those.
  - Command: `artifacts.evict` (verb `evict`, registry scope, admin) with a filter (type `training-state` only in
    v1, optional run or project, `olderThanDays`). The dry run lists the candidates, their runs and the bytes it would
    free. A real call is always gated, for people too (no destructive data operation without an approval command):
    it answers an approval id; the approval's decision runs a job that deletes the blobs, marks the rows
    `evicted_at` (the row and its metadata stay; `artifacts.get` shows it evicted, a step input naming it fails
    `artifact-missing`), emits `artifact.evicted` and leaves an audit entry with the bytes freed.
  - Reversibility: the backup mirror never prunes, so an evicted blob can be copied back from it (the vocabulary's
    `evict` is reversible); on an instance without backups the dry run and the approval say the eviction is
    permanent. Automatic retention (a high-water mark on the store) comes later, through the same command.
  - As built (`internal/eviction`, migration 0020): `POST /artifacts:evict`, body `{type, runId, project, olderThanDays,
    hashes}` (all optional). The dry run answers `{artifacts (with the reason), kept (with the reason), bytesFreed,
    blobs, permanent}`; the real call answers `202 {approvalId}` for everyone (preset rule `store-eviction`,
    `everyone: true`; agents hit `agents-never-evict`, forbidden, first); the approved replay queues the
    `artifacts.evict` job and answers `202 {jobId}` as the approval's result. Named `hashes` that may not go answer
    `409 artifact-not-evictable` before any approval is created. Added to the design: (1) "newest" and "finished"
    are read from the producing **pipeline run** (`done`: all states; `failed`/`cancelled`: all but the newest by
    record time; `running`: none), so pipeline runs without a training-run facade follow the same rule; (2) the
    never-evictable set also covers inputs of running pipelines and unfinished steps, registry-version payloads,
    checkpoint rows and files of other live artifacts (all matched by hash); (3) with backups configured, a state
    whose blobs the mirror does not hold yet is kept ("evict it after the next backup"), so every eviction can be
    undone; (4) training states that stopped steps saved only into lease outcomes (a pause or window close records
    no artifact row) are indexed at selection time, producer = the step, time = the lease's end. The job re-plans
    under an advisory lock, marks `evicted_at`/`evicted_by`/`eviction_job_id` and emits `artifact.evicted` on
    `entity.artifact.{hash}` in one transaction, then deletes the blobs and writes an audit entry whose new `detail`
    column holds the artifacts, `bytesFreed` and blobs; a retry deletes what its marked rows left, a second eviction
    finds nothing. `artifact_files (hash, path, file_hash, size)` is filled by `artifacts.Record` and, for
    directories recorded earlier, by a start-time backfill, which also clears the eviction of artifacts whose blobs
    verify again — the restore path: copy the blobs back from `CADENCE_BACKUP_DIR/cas/` to the same relative paths
    and restart (recording the same artifact again clears it too). `artifacts.get` shows `evicted {at, by, jobId}`, a
    directory's files from the index, and content "evicted … restore it from the backup mirror"; a pipeline input
    naming it answers `artifact-missing`. Audit fix (2026-10-01): the job decides and deletes under one content-store
    advisory lock that `artifacts.Record`, step reuse and the restore hold shared from their store check to their
    commit; the deleting transaction re-reads which rows are still marked (a Record that found the bytes meanwhile
    cleared the mark) and which blobs a live artifact lists, and a Record after the deletion answers
    `artifact-missing`. The reference checks are index lookups (migration 0022: GIN on every `b3:` hash in step job
    specs, pipeline inputs and registry payloads; a btree on `checkpoints.artifact_hash`), no longer text scans.
  - Eval artifacts by age (owner decision 2026-10-03; built the same day): the per-utterance artifacts of an eval
    record — types `scores`, `hypotheses` (`eval_records`) and `metric_scores` (`eval_metrics`) — are evicted
    `eval.artifact_retention_days` (30, range 1–3650) after the record's last use, the newest of its creation and the
    creation of every eval whose cells link it (a metric row: the cells of its model, golden set and decoding). Kept:
    whatever a registered model's eval links (`model` versions' `evalId` → its cells' records), whatever a queued or
    running eval links, whatever the checks above protect (step jobs, running pipelines and steps, registry payloads,
    checkpoints, files of live artifacts), and — with a backup mirror — anything the mirror does not hold yet (so the
    eviction can be undone; `permanent` is false then). A periodic job `evalArtifacts.retention` (daily, and at start)
    plans and, when something is evictable and no retention job of the system actor is queued, enqueues the
    `artifacts.evict` job with `{retentionDays}` instead of hashes, as the system actor, without an approval (the owner
    set the policy); the job re-plans under the store lock, marks, emits `artifact.evicted`, deletes and audits like an
    approved eviction (`detail.retentionDays`). `eval_records`, `eval_cells.delta` and `evals.gate` are never touched.
    Readers: `evals.get` → the cell's `evicted` note and no `worst`; the gate reuses stored deltas and only a
    recomputation (another significance) turns inconclusive; `words.get` → `410 artifact-evicted`; `evals.new` computes
    a record with evicted artifacts again and refreshes it (`scores_hash`, `hypotheses_hash`, provenance; not the
    summary). The backup mirror is not pruned when the store evicts: it keeps every blob it copied (that is what
    makes the eviction reversible); its own retention, `backups.keep_nightly` / `keep_weekly`, prunes the database sets
    only (07 "Open questions": mirror growth).
- Metrics: one Postgres table `metric_points` (migration 0012: job, run when there is one, pipeline step, project,
  name, optimiser step, epoch, value, wall time), indexed by (run, name, step) and (job, name, step); thousands of
  points per run need no TSDB. Points arrive from `workerMetrics.new` (≤ 5 000 per batch) and stream as `run.metrics`
  on `run.{id}.metrics` when the step belongs to a run; `internal/telemetry.Get` answers one series per metric in step
  order thinned evenly to a maximum number of points (first and last kept), which `metrics.get` exposes with runs
  (phase 2 wave 2, R53). Points are never deleted in v1: they live as long as their run.
- Logs: one NDJSON file per job, `$CADENCE_DATA_DIR/job-logs/<jobId>.ndjson` (`logs/` holds the control plane's own
  log files), lines `{t, level, msg, fields}` (`msg` ≤ 16 000 characters, ≤ 1 MiB per `workerLogs.new`). An oversize
  line is truncated, never refused: a line over 64 KiB is stored cut with `[truncated: the line had N bytes]` in its
  `msg`, and bytes past 4 MiB in one request are discarded, so the lines after it still land; each request
  becomes `job.log` events on `job.{id}.log` of at most 200 lines. `jobLogs.list` reads a job's lines with their line
  numbers, filtered by minimum level and message text, paging with `after` or reading the `tail`. Field search and a
  global search index of `warn`+ lines (R15) are not built yet. A daily chore deletes log files untouched for 14 days.

## Media: audio and the live channel (phase 3)

Audio reaches people through four endpoints tagged `media`, the only surfaces besides the event stream that do not
answer JSON (R25, R47–R50; plan streams A and T, gated by spikes S5 and A5).

**The `media` tag.** Like `auth`, `me`, `host` and `worker`, operations tagged `media` are exempt from the verb
vocabulary and are never MCP tools; the generator enforces both. They still need an identity (the session cookie, or
a signed URL below), and an agent session token reaches none of them: agents read no raw audio (05 "What the agent
sees") and test models through evals. Members: an utterance's audio, its peaks, `transcriptions.new` and the
transcription socket.

**Audio serving (R25).**

- `GET …/utterances/{id}/audio?channel=&start=&end=` answers a span of an utterance as 16 kHz 16-bit PCM, with range
  requests (`Range: bytes=…` → `206 Partial Content`, RFC 9110). R25 wrote the path as `/utterances/{id}/audio`;
  utterances live under `/registry/utterances` (R1), and the contract fixes the path (07 "Open questions").
- `GET …/utterances/{id}/peaks`: min/max peaks per channel for the waveform track; short audio from its PCM, long
  audio from a `peaks` artifact computed at ingest (10 ms int8 min/max per channel, ≈ 720 KB per channel-hour as
  measured by spike S5, which corrects R51's 450 KB; phase 4).
- Players fetch audio through short-lived signed URLs (the signature binds the utterance, span, viewer and expiry), so
  a media element needs no headers and a copied link soon stops working; the lifetime is an open question.
- Play-only mode (the reviewer role, "Authentication and access" above): audio is streamed through Media Source Extensions with no download control and no
  file URL in the page. This is a deterrent, not a guarantee: anyone who can hear audio can record it.
- The audit log records who played what: viewer, utterance, span, time.

Audio serving as built (2026-10-02, stream A; `internal/media`, `internal/server/handlers_media.go`, migration 0029):

- Five operations under `/registry/utterances/{id}` (id: `utt_…` or the audio's `b3:` hash), tag `media`, people
  only — the actor must be a user (a session, or a signed link for the viewer it names); agent actors and `cst_`
  tokens, API keys (`cdk_`, automation actors), worker and host tokens are refused (`403 forbidden`, 2026-10-02),
  and so is `transcriptions.new` — registry read required: `audio.get` (WAV, byte ranges, `416`
  `range-not-satisfiable`), `audio.sign` (POST, `{channel?, start?, end?}` → `AudioLink`: a relative URL with
  `viewer`, `exp`, `sig`), `peaks.get` (`hopMs` a multiple of 10, `start`, `end`), `spectrogram.get` (the manifest, or
  one tile with `tile=c<ch>/l<L>/<i>`) and `words.get` (`hypotheses`, `scores` artifacts → the row's timed words with
  `op`/`ref` from the alignment, deletions, partials). `audio.get` and `spectrogram.get` answer bytes from the
  non-strict router layer.
- `audio.get` serves the stored file as is when it is 16 kHz 16-bit PCM and asked whole; any other span is decoded
  (PCM 8/16/24/32-bit, float 32), the channel picked, resampled to 16 kHz (polyphase Hann-windowed sinc, ±0.01 dB to
  0.9 × Nyquist, −60 dB stopband) and encoded as 16-bit PCM, up to `media.max_span_s` (600 s since 2026-10-02, the
  audio view's `browser_stft_max_s`; it was 3600 s, which held ≈ 0.7 GB per conversion). As built 2026-10-02: the
  conversion runs in 10 s blocks (a few MB whatever the span) straight into the span cache — files under the content
  store's `cache/media-spans`, named by audio hash, frame span and channel, never backed up, least recently served
  dropped beyond `media.span_cache_mb` (2048) — so the ranges a media element fetches read a file instead of
  converting again; at most `media.max_conversions` (2) run at once across viewers, one more answers `429
  media-busy` with `Retry-After`. No MSE segments yet: the
  element plays the signed WAV URL with `controlsList="nodownload"` and no context menu (play-only remains a
  deterrent).
- Signed links: HMAC-SHA256 over utterance, channel, start, end, viewer and expiry with a key derived from the master
  key (links die with it); lifetime `media.signed_link_ttl_s` (300 s). A request carrying `sig` passes the session
  check and is the viewer's play; any change to the query or an expiry gives `403 media-link-invalid`, and so does a
  link whose viewer is no user, or a reviewer whose invitation to the item's batch has ended (revoked, expired or the
  batch froze: `credentials.ReviewerMayPlay`; audit F1) — revoking an invitation stops the links it minted at once.
- Audit: `audio.sign` and every `audio.get` that starts a play write an audit row with the utterance, audio hash,
  span, channel, `via` (`session` or `link`) and the `range` asked; further ranges of the same play do not. As built
  2026-10-02 a play is the first request of a viewer, utterance, span and channel within `media.play_audit_window_s`
  (600 s), whatever its `Range` (before, only requests from byte 0 counted, so `bytes=1-` played unaudited); the
  window is kept in the control plane's memory, so a restart audits a play again rather than never. The row is
  written before the first byte is sent.
- Peaks are computed on the first `peaks.get` from the stored audio at 10 ms (int8 min/max, clipping frames) and
  recorded as a registry `peaks` artifact (`meta.audio`, `meta.format` `cadence.peaks/1`); later reads pool it. Phase 4
  meant to compute them at ingest; as built they are still computed on first view (`ROADMAP.md` "Phase 4 notes").
- The server tile pyramid is the worker step `spectrogram_tiles@1` (manifest `cadence.spectrogram-tiles/1`, uint8 dB
  `-120 + 0.5 × v`, 512-frame tiles, bins up to the origin's Nyquist, levels max-pooled by two); `spectrogram.get`
  serves the newest such artifact whose `meta.audio` is the utterance's hash. Nothing starts the step automatically
  yet (phase-3 golden sets are short; long calls arrive with phase 4).

**Manual transcription tests (R47).** A person runs one to three models on a file, the microphone or an utterance
span and watches the words appear; nothing outlives the session.

- `transcriptions.new` (`POST /projects/{p}/transcriptions`, id `trs_`) is the only operation: nothing to get or
  list. It takes the input kind, targets (checkpoint, model version or base model; the staging Triton deployment from
  phase 5), each with its latency profile, boost list or none and language, an optional blind option (lanes unnamed
  until the person picks one; the pick is not recorded) and `analysis: [features, emissions]` for the audio view's
  model tracks. It answers the session with a `streamUrl` and a single-use ticket valid 60 s; the socket also checks
  `Origin`.
- Inputs: a file chosen in the browser (≤ 15 minutes of audio; its bytes go to the worker's temporary directory for
  the session only, decoded with ffmpeg and the training resampler, deleted when the socket closes, and a sweep
  removes what a crashed session left within an hour; as built 2026-10-02, ffprobe reads the local file first —
  `-protocol_whitelist file`, `-format_whitelist` of the audio containers wav, flac, mp3, ogg, mov/mp4/m4a, aac,
  matroska/webm, so a playlist or a concat list never reaches a demuxer — and a declared duration over the limit or a
  rate above 192 kHz is refused before decoding; ffmpeg then decodes channel 0 cut at the limit + 1 s (`-t`) and
  capped in size (`-fs`), so a small file that expands to hours costs nothing), the microphone, or an utterance span
  (`utt:123#t=1.2,3.4`). One streaming decoder serves all three; a file plays at real-time pace or as fast as the card
  allows, and the microphone and paced files show latency. The worker resamples every input with the import and
  training resampler, streaming polyphase (`resample_poly`; 03 "Augmentation", spike A5).
- Streaming families decode at the target's profile, so the page shows what production would have written; adding
  the same checkpoint at `1120ms` shows the gap to the high-latency reference. A typed reference gives WER and a diff
  on the page. The Language pack's "test a phrase" is a two-target transcription, boost on and off.
- Kept: only the interactive job's record (who, when, which targets, GPU time) for the queue and the allowance. The
  page can copy the text; no audio, text or metric is stored.

**The live channel (R48).** The browser opens one WebSocket, `/api/transcriptions/{id}/stream`, with the ticket. No
WebRTC: its Opus encoding and echo processing would change the audio under test. The protocol has the shape the
streaming vendors converged on (Deepgram, AssemblyAI, Speechmatics, Soniox, NVIDIA NIM): configuration first, then
binary audio; partials replace each other, finals never change. The message schemas are the contract components
`LiveClientMessage` and `LiveServerMessage` (JSON messages discriminated by `type`), and the TypeScript types are
generated like the rest.

| Message | Direction | Frame | Carries |
| --- | --- | --- | --- |
| `start` | Client → server, first | JSON | The input: microphone (capture rate and `getSettings()`), file, or utterance span; telephony simulation on or off; the pace for files |
| audio | Client → server | Binary | 16-bit little-endian mono PCM at the capture rate, at most 20 ms per frame (`transcriptions.frame_ms`, spike A5); or a file's bytes |
| `fileEnd` | Client → server | JSON | The file's bytes are complete |
| `finalize` | Client → server | JSON | Flush pending words without closing |
| `keepalive` | Client → server | JSON | Keep the session open without audio |
| `end` | Client → server | JSON | Flush, summarise and close |
| `started` | Server → client | JSON | The effective configuration per target and its model load time |
| `partial` | Server → client | JSON | Target, segment, sequence, text, audio end; replaces the segment's previous partial |
| `final` | Server → client | JSON | Target, segment, words with audio-time start, end and confidence, the endpoint reason |
| `stats` | Server → client | JSON | Real-time factor, queue, relay time |
| `error` | Server → client | JSON | A problem+json body |
| `summary` | Server → client, last | JSON | The session's totals; then close code 1000 |

- Every result states the audio offset it covers, so latency is measured on audio time; the client adds its own
  wall-clock stamps. Latency and stability figures on the page come from the session's own events (R54) and go with
  it.
- The control plane relays frames to a `live` job and enforces backpressure, caps and timeouts. The worker dials out
  for the job (`/worker/live/{jobId}`, tag `worker`), so the pull model of R14 holds. In the NeMo runtime the job
  runs NeMo's streaming pipeline API (`nemo.collections.asr.inference`, the cache-aware RNNT pipeline): one socket per
  stream id, streams batched continuously, end-of-utterance detection, boosting and language per stream. A hard
  finalize pads the right context with silence; up to three targets receive the same audio. Spike A5 measured it
  (`docs/spikes/A5-live-transcription.md` "Result": p95 from `finalize` to the last final 43 ms at `160ms`, 70 ms
  beside training; model load ≈ 22 s; the pipeline API needs two shims for Nemotron 3.5) and proposed the message
  details, 20 ms frames and the worker's live-job protocol; stream T specifies them as it builds them. Evals move to
  the same decoder so live and eval words agree (03 "Runtimes, model families and latency profiles").
- Limits in v1: one session per user, 15 minutes each, closed after 5 minutes idle.

**Interactive compute (R49).**

- Job kind `interactive` (transcription sessions): a memory reservation from the family (Nemotron 0.6B: 6 000 MB —
  3.7 GB steady plus the 5.6 GB load peak and margin — and 2 600 MB per further distinct checkpoint, fp32 weights;
  targets of one checkpoint share its weights; measured by spike A5, replacing the 3 GB placeholder), the highest queue priority, beside training under the card's cap but never beside a benchmark (R30), and
  counted in a daily GPU-hour allowance per project (default 1 GPU-hour, 03 "Key defaults").
- When no card has room the session waits in the queue, the page shows its place, and live mode is disabled with the
  reason. Cards allow the kind like any other (Compute: allowed job kinds, availability windows).
- From phase 5 a Triton target needs no worker job.

**Capture in the browser (R50).**

- The microphone is captured with an AudioWorklet at the device rate; the worker resamples with the training data's
  resampler. MediaRecorder's lossy formats are not used.
- `getUserMedia` runs with echo cancellation, noise suppression and automatic gain off by default ("raw
  microphone"), as Google and Deepgram advise for recognition; a toggle turns them on to hear what a call stack does
  to the audio. Only channel 0 is taken (Safari returns stereo with audio on the left when echo cancellation is off).
- Telephony simulation: down to 8 kHz through the codec of the project's augmentation profile (G.711 by default),
  then back up to 16 kHz with the training resampler, the path NeMo recommends for telephone audio; the profile's SHA
  and seed are shown with the result.
- A device picker and an input level meter with a clipping mark; a secure context is required (HTTPS through Caddy,
  or localhost).
- Display: Hebrew right to left with bidi isolation around digits and Latin text; grey partials update in place,
  finals are solid with endpoint marks; confidence shades words, timestamps show on hover; live p50/p95 time to final
  and the real-time factor sit under the lanes.

Transcriptions and the live channel as built (2026-10-02, stream T; `internal/transcriptions`,
`internal/server/handlers_transcriptions.go`, migration 0026, `cadence_worker/live.py`, the packs' `live` role kinds,
the Transcription panel):

- Operations: `transcriptions.new` (`POST /projects/{p}/transcriptions`, tag `media`, a command; `200` for a dry run,
  `201` with `streamUrl` and `ticket`), `stream.connect` (`GET /transcriptions/{id}/stream?ticket=`, tag `media`) and
  `workerLive.connect` (`GET /worker-live/{jobId}`, tag `worker`). The worker's path is `/worker-live/…`, not
  `/worker/live/…`: the worker credential is confined to `/worker-*` paths. Message schemas: `LiveClientMessage`
  (`start`, `fileEnd`, `finalize`, `keepalive`, `ping`, `end`) and `LiveServerMessage` (`waiting`, `started`,
  `partial`, `final`, `stats`, `pong`, `error`, `summary`). `waiting` (from the relay: queued with position, reason
  and reservation, then loading) and `ping`/`pong` (the relay's own hop) are additions to the table above.
- Targets: one family per session (one worker job serves them all); the family descriptor names the kind in role
  `live` and the reservation in `interactive {memoryMb, extraCheckpointMb}`. Lanes are `A`–`C`; blind shuffles them
  on the server and still returns the labels (the page hides them until the pick). Telephony is
  `{codec: ulaw|alaw|none, sampleRate: 8000}` set at `transcriptions.new` (not in `start`); the project's augmentation
  profile is not read yet.
- The ticket is kept as a SHA-256 hash and is single-use; the socket checks `Origin` against the request's host (or
  `X-Forwarded-Host`, or `CADENCE_ALLOWED_ORIGINS`) and that the signed-in person owns the session — since 2026-10-02
  before the socket is registered in the relay's hub, so someone else's socket never holds a session's place. A
  socket that registered but then failed its ticket or upgrade hands back a worker socket it was given (re-parked for
  the session's next socket). The worker dials with the lease's `CADENCE_LIVE_TOKEN` (header `Cadence-Live-Token`; a
  fresh token per lease, hash kept) or its `cwk_` credential, which must belong to the host that holds the job's
  active lease; the harness sets `CADENCE_LIVE_URL`.
- Interactive jobs are River kind `live` awaiting a step job of kind `interactive` (`steps.LiveJobKind`): leased
  before every other kind, beside training under the card's cap (training's whole-cap reservation leaves no room, so
  a session waits for a training step that took the whole cap), never beside a `benchmark`; priority
  `transcriptions.interactive_job_priority`. Their lease wall time counts against
  `budgets.manual_test_gpu_hours_per_project_per_day` and not against the project's GPU budget. Since 2026-10-02
  the allowance is granted, not only checked: `transcriptions.new` grants a GPU session its seconds (the smaller of
  `transcriptions.session_max_minutes` and what is left) under a per-project advisory lock, from what is left net of
  the other open sessions' grants less what their jobs leased (`transcriptions.session_seconds`, migration 0032), so
  sessions opened side by side cannot together overspend; the relay caps the session at its grant less the card time
  its job leased (the model's loading included). A CPU session is granted nothing and spends nothing.
- The relay is in process (one control plane). Close codes: 1000 after the summary, 4001 idle, 4002 cap, 4003 worker
  lost, 4004 not started (job ended, queue wait limit), 1013 backpressure, 1001 stopping; at the idle and cap limits
  it injects `end` so the summary still arrives. A 30 s sweep ends sessions whose ticket expired unused or whose
  socket is gone. Problem types: `transcription-in-progress`, `transcription-allowance-exhausted`,
  `transcription-ticket-invalid`, `transcription-input-invalid`, `transcription-limit`. Limits: `transcriptions.*`
  in `defaults.yaml`.
- One decoder (A5 proposal 2): the NeMo pack's `pipeline.py` (NeMo's cache-aware streaming pipeline, model restored
  on the CPU, per-stream phrase boosting, and five shims: the per-stream prompt, tag stripping, features computed once
  their whole window has arrived and cut like the reference loop's chunks, the first step's pre-encoded frames, the
  feature buffer's length) serves `nemotron_live@1` and `nemotron_transcribe@3` (decoding
  `decoder: nemo-pipeline-cache-aware`; evals pick @3 as the newest transcribe kind, so @2 records do not mix). On the
  stand card, FLEURS he fixtures and ru clips with the base model: @3 has @2's WER at every profile (he 75.6 / 77.9 /
  65.1 / 66.3 / 69.8, ru 16.2 / 17.2 / 17.2 / 16.2 / 14.1 at 80 / 160 / 320 / 560 / 1120 ms, the same empty clips),
  its words differing only where @2 drops an utterance's last tokens; a live session in 20 ms frames and @3 at batch 1
  give the same words (22/22 at every profile; batch 8 changes 3 of 22 at 80 ms with equal WER, so evals keep `transcribe_batch_size` 8 for throughput and 1 is the exact-match setting).
  One model peaks at 2.8 GiB allocated, load 29–30 s, RTF about 0.06 at batch 1. NeMo's own frame path was worse (he
  at 160 ms: 5 empty clips, WER 80.2; at 80 ms WER 91.9). Two distinct models in one job turn NeMo's CUDA-graph
  decoder off (it crashes with two). The training telephone stage moved to polyphase resampling
  (`nemotron_finetune@2`).
- Not built: `analysis: [features, emissions]`; a conformance stage for the live role; the Triton target (phase 5).

Audio indexed in place and the tracks (phase 4, stream A; `internal/media` `window.go`, `tracks.go`):

- Media ids beyond `utt_` and `b3:`: a triage item (`tri_…`) plays its segment ± 2 s of the source file, an
  annotation item (`bit_…`) its window (segment ± `annotation.context_s`, every channel); a draft dataset version's
  utterances play from their mount URI. The control plane reads local, NFS and SMB mounts at the workers' paths and
  serves PCM, float and G.711 (μ-law, A-law) WAV in place — the telephone calls play without a copy; other codecs
  play once the version is frozen. Peaks of a window are cached under its `mount://…#t=` key.
- `tracks.get` (`GET /registry/utterances/{id}/tracks?hopMs=`, tag `media`, people only): per channel the level in
  dBFS per hop, speech regions from an energy VAD (the channel's 10th-percentile floor plus `annotation.vad_margin_db`
  12, never under `annotation.vad_floor_db` −55, pauses under `annotation.vad_min_silence_ms` 300 joined), the
  estimated bandwidth (the highest band within `annotation.bandwidth_floor_db` 50 of the loudest; 8 kHz audio shows
  ≤ 4 kHz), and with a caller and a bot channel the end-of-utterance gap. Computed on request, not stored. The audio
  view draws them as the energy, VAD and channel tracks.
- Not built: the tile pyramid on demand for long audio, peaks at ingest, a reference-alignment track in the audio view.

## Operations

Cadence upgrades itself the way it upgrades models: versioned, forward-only, with a nightly backup that is restored on a schedule to prove it works.

| Concern | Rule |
| --- | --- |
| Versioning | Cadence releases are semver; each release pins every runtime image by digest (v1: NeMo Speech 26.07), Dockview and the agent adapters, and states the matrix in the release notes (a help article) |
| Install | `docker compose up`; the first start creates the admin account and the default mounts |
| Migrations | Embedded in the binary, forward-only, expand-and-contract, run at start under an advisory lock; data migrations run as jobs with progress events |
| Upgrade | Pull the release, `compose up`; a failed migration stops the start and leaves the previous image runnable; rollback is the previous image plus, if data changed, the last backup |
| Backups | Nightly `pg_dump` and a content-store mirror into `CADENCE_BACKUP_DIR` (the `cadence-backups` volume), or since phase 4 the mirror onto a writable path mount (`backups.mirror_mount`); a weekly automated restore into a scratch database with a report; targets: 24 h RPO, 1 h RTO (as built below) |
| Failures | River retries with backoff; a worker heartbeat every 10 s, leases reaped after three missed beats (step error `lost`, one retry); an OOM gets one automatic retry at 0.75× batch; a host whose workers went quiet turns `unreachable` (`compute.health`); a project over its cache quota cannot freeze (`storage-quota-exceeded`, phase 4); an unhealthy card closes its slot (not built: card health is per host today) — every case is an event, so it notifies |
| Availability windows | Each compute card has windows per job kind (training, eval, shadow, export, data; none means always open, the default): each window is a set of weekdays, an opening and a closing time `HH:MM` (an end at or before the start closes the next day, `24:00` is midnight; a window past midnight belongs to the day it opens) and an IANA time zone per window (default: the instance time zone, `policies.timezone`, resolved when the queue checks the window — so a policy change moves windows that name none), edited with `compute.edit`. The queue starts a job only if its estimate fits before the window closes; a job without an estimate, or one resuming from a training state, starts in any open window. Training saves a checkpoint and its training state every 20 minutes (the training step's duty; the NeMo pack's); at a close the heartbeat answers `stop: window-closed` to training steps only (other kinds finish), the step saves and releases, and the job waits in its place for the next window and resumes from the last training state (`resumeFrom`). The same path makes long runs preemption-safe on the shared staging card (R19) |
| Health | `/healthz` on the control plane, worker heartbeat, mount checks (`mount_check@1`, every `storage.mount_check_hours`; each emits `mount.health`; a step that reads a mount whose last check failed does not start, `mount-unhealthy`); a status card in Settings; a Prometheus endpoint |
| Retention | Job log files are deleted 14 days after their last line (a daily chore); metric points live as long as their run; content-store blobs are kept until a person approves `artifacts.evict` (superseded training states only; Settings → Content store, and a `storage.low_space` failure notification below `cache.store_low_free` free), except eval records' per-utterance artifacts, evicted `eval.artifact_retention_days` (30) after the record's last use by a daily sweep (owner decision 2026-10-03); dataset shards with a copy on a mount are evicted by the cache sweep (phase 4, below); the audit log is kept one year; production audio follows the retention policy |

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

Phase 4 as built (2026-10-04, streams M and I):

- **Cache sweep** (`internal/cache`): a periodic job every `storage.cache_sweep_minutes` (15) evicts unpinned dataset
  shards that have a mount copy, least recently used and projects over `storage.project_quota_gb` first, once the
  store passes `storage.cache_high_water_pct` (85) and down to `cache_low_water_pct` (70). It runs as the system actor
  without an approval (the owner set the policy, like the eval-artifact retention); evicted rows keep their manifest
  and `datasets.materialize` copies the shards back, verifying each hash. Pinned: dataset versions a queued or running
  job names, the lineage datasets of aliased model versions and golden sets' datasets.
- **Backups to a mount** (stream I): with `backups.mirror_mount` naming a writable path mount, the content-store mirror
  goes to `<root>/cas/b3/…` instead of `CADENCE_BACKUP_DIR/cas`, and every mirrored blob is recorded in
  `blob_copies` (`eviction.Service.Mirror`), so the cache may evict it and `datasets.materialize` brings it back.
  Compose keeps `/mnt/exports` writable in the control plane for that.

## Notifications

Two channels — the in-app history and a Telegram bot — one routing table by event class, and approvals that can be decided from the phone with the same audit trail as from the UI.

| Event class | In-app | Telegram | Timing |
| --- | --- | --- | --- |
| Approval requested (agent, automation, registry) | Yes | Message with inline Approve / Deny buttons and the estimate | Immediate |
| Job or pipeline step failed, compute host unreachable, mount unhealthy, card closed (not built), backup failed | Yes | Yes | Immediate |
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
- Classification (`internal/notify/classify.go`, mirrored by `web/src/shell/notifications/classes.ts`; a test keeps
  the two tables equal): approvals on `approvals`; a job's `job.state_changed` on its job topic (`failed` → failure,
  `done` → progress) except step jobs, whose pipeline step tells them — `pipeline_run.step_changed` on
  `pipeline_run.{id}` (step `done` → progress, `failed` with no retry left → failure); a host turning `unreachable`
  (`compute.health` on `compute.{id}`) → failure; backups by type. The table also names types no stream emits yet —
  `mount.unhealthy`, `deployment.promoted`, `schedule.finished`, `batch.closed`, `checkpoint.saved`,
  `triage.item_added` arrive with their phases; `compute.card_closed` joins when per-card health closes a card's slot
  (not built).
- Evaluation (2026-10-02, phase-3 audit): the gate verdict is `eval.gated` (evals.gate; `passed` or `failed`, both
  outcome — a failed gate is a result, not an operational failure; the placeholder `gate.verdict` is gone); an eval's
  end is `eval.status_changed` (`failed` → failure, `done` → progress, telling to run `evals.gate`); `sweep.ended` →
  outcome; `golden_set.frozen` → progress. They announce on entity topics only (`entity.eval.{id}`,
  `entity.experiment.{id}`, `entity.golden_set.{id}`), which the router reads for these types and the web history
  subscribes to. The steps of an eval's pipeline run (its `pipeline_run.step_changed` carries `runId: evl_…`) tell
  nothing when done — an eval of a few hundred cells would send as many notices; a failed step still does. The daily
  digest lists the gate verdicts of its window ("project: subject — verdict", at most 20).
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
