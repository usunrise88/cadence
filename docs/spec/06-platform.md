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

- Mutations require the session cookie plus a custom header, or a Bearer token; SameSite plus the header is the CSRF defence. The cookie is `cadence_session` (an opaque `cws_` token); the header is `Cadence-Client: web`; tokens are `cdk_` (API key), `cst_` (agent session), `cwk_` (worker), `cah_` (the agent host: its protocol only) and `cep_` (the egress proxy: its allowlist only, 2026-09-30), stored as SHA-256. Unauthenticated requests answer `401 unauthenticated`, out-of-scope ones `403 forbidden`.
- All credentials live in one table: kind, scope, hash, expiry, last use; revocation is one click and is itself an audited command.
- TLS terminates at a reverse proxy in the compose file (Caddy with an internal certificate); the control plane listens on localhost only.
- Login attempts are rate-limited; a lost admin password is reset from the host shell (`cadence admin reset-password`), never by email.
- Passkeys (WebAuthn) are the v2 upgrade; nothing in v1 prevents adding them.

## Operations

Cadence upgrades itself the way it upgrades models: versioned, forward-only, with a nightly backup that is restored on a schedule to prove it works.

| Concern | Rule |
| --- | --- |
| Versioning | Cadence releases are semver; each release pins the NeMo Speech container tag, Dockview and the agent adapters, and states the matrix in the release notes (a help article) |
| Install | `docker compose up`; the first start creates the admin account and the default mounts |
| Migrations | Embedded in the binary, forward-only, expand-and-contract, run at start under an advisory lock; data migrations run as jobs with progress events |
| Upgrade | Pull the release, `compose up`; a failed migration stops the start and leaves the previous image runnable; rollback is the previous image plus, if data changed, the last backup |
| Backups | Nightly `pg_dump` and content-store sync to a mount; a weekly automated restore into a scratch database with a report; targets: 24 h RPO, 1 h RTO |
| Failures | River retries with backoff; a worker heartbeat, jobs reaped after three missed beats; an OOM gets one automatic retry at 0.75× batch; a full cache pauses freezes; an unhealthy card closes its slot — every case is an event, so it notifies |
| Health | `/healthz` on the control plane, worker heartbeat, mount checks; a status card in Settings; a Prometheus endpoint |
| Retention | Logs rotate after 14 days; the audit log is kept one year; production audio follows the retention policy |

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
- **Retention**: the audit log is pruned daily after one year (`audit.prune`, phase 1). Job logs are stream W's.
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
| Framework conformance | Each framework pack on fixtures: calibrate → train a few steps → average → transcribe (file and streaming) → export → parity → score (R45); the CPU `toy` pack keeps the seams honest | Toy pack every pull request; NeMo pack nightly on the staging card |
| Agent evals | Skills and playbooks executed by both agents on a fixture project; pass criteria are the expected tool calls and outcomes, not the wording (`agent-host/evals`, `make evals`) | Offline with a scripted agent on every pull request; live with both drivers nightly on the staging host and on skill changes |
| End to end | The smoke project on the staging card: ingest, freeze, 300 steps, eval, gate, export, parity | Nightly |
| Performance | Workspace restore, drag frame time, SSE fan-out, search latency against the budgets | Weekly |

Fixtures are tiny public subsets (FLEURS, Common Voice) kept in the repository; nothing in CI touches production data or the production card.
