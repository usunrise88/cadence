# Cadence roadmap

Six phases, each closed by a gate, not a date. The phase list and gates are the "Build order" figure of the Claude Doc
"Cadence — spec v0.2" (UI shell tab); what goes into each phase is distributed from `docs/spec/*` by the rule stated
there: *the shell first, the agent loop second, then the blocks in the order a first fine-tune needs them; each
block's endpoints and MCP tools land before its panels.*

| Phase | Delivers | Gate (exit criterion) | Spikes |
| --- | --- | --- | --- |
| 0 · Shell | Contract toolchain, control-plane core, Dockview shell | S1–S4 pass | S1, S2, S3, S4 |
| 1 · Agent loop | Auth, MCP, agent host, sessions, approvals, projects, registry core | An agent edits a mix and the open Mix panel updates live, attributed | A1, A2, A4 |
| 2 · Training | Worker protocol, artifact store, queue, runs, playbooks; runtimes and model families (seams for other frameworks) | The agent runs a fine-tune on the staging card end to end; the CPU toy pack passes the conformance suite | A3 |
| 3 · Evaluation | Golden sets, streaming eval, gates, language packs; manual transcription tests (file, microphone); the audio view | A gate verdict is produced on staging | A5, S5 |
| 4 · Data | Mounts, ingest pipeline, freeze, registry in full, annotation | A dataset version frozen by Cadence is trained on | — |
| 5 · Deploy and flywheel | Export, shadow/canary, triage, schedules | A canary promotion is approved in the UI | — |

How to use this file: work top-down inside a phase; tick items as they merge; a phase is done only when its gate is
demonstrated and recorded (a note in the phase section + the spike Results). Two kinds of blocking lines:

- **decide** — an open question from `docs/review/2026-09-29-spec-kickoff-review.md`; settle it in the Doc and the
  decision log (`docs/spec/00-overview.md`) before the items under it.
- **spec** — a block the items rely on that the spec does not describe yet; write it (Doc first, then re-export)
  before implementing. Found by the dependency audit at the end of this file.

Every **decide** and **spec** line now carries a resolution (`→ R#`) in `docs/spec/08-resolutions.md`. R1–R39 and
the second pass R40–R54 (framework seams, manual transcription tests, audio views and charts) were ported into the Doc
("Resolutions 2026-09-29" tab + decision log) on 2026-09-29, so every such line is decided; tick it when the affected
spec section is rewritten. Lines marked *confirm* wait for the owner's answer and are built against the proposed
default; the one phase-4 **decide** on non-NeMo models is settled when that phase starts. What the owner deferred is
listed under "Deferred" and is not planned into any phase.

Every phase also carries the standing obligations from `CLAUDE.md`: spec file updated, help articles for new
panels/steps/errors, `make gen` output committed, tests at the right layer of the pyramid (`docs/spec/06-platform.md`).

---

## Phase 0 · Shell

**Goal.** The contract toolchain works end to end and the desktop shell stands on it: one OpenAPI file generates the
Go stubs, the TS client and the MCP manifest; the control plane owns state, emits events and serves layouts; the
Dockview shell restores workspaces, runs commands and themes every surface.

**Gate — passed 2026-09-29** (branch `feat/phase-0-shell`): S1–S4 `Status: done`, numbers in each spike's Result
(p95 frame 16.7 ms with 10 floats and tab docking intact; tooltip, menu and palette render in a popout; 311 Dockview
surfaces themed in both modes, contrast script green after three token fixes; 20-panel restore 171 ms, 4 subscriptions
for 4 visible panels). Re-measured weekly by `.github/workflows/performance.yml`.

Original gate: S1–S4 Results filled and `Status: done`: 60 fps drag with 10 floats and tab docking intact (S1); Base UI
popovers, tooltips and palette work in a popout (S2); no unthemed Dockview surface, contrast script passes (S3);
20-panel workspace restores in < 300 ms with zero hidden subscriptions (S4).

Decide before starting:
- [x] **decide** → *R1* Naming rules for operations: verb list gaps, one name per action, entity casing, HTTP-shape → verb,
      global vs project item paths (review A1–A4, A6). The generator enforces them, so nothing is generated before.

Contract and toolchain
- [x] `api/openapi.yaml` rewritten to the rules: shared components for `Idempotency-Key`, `If-Match`, `dryRun`,
      problem+json (RFC 9457, `412` with `currentRev`), `202 {jobId}` / `202 {approvalId}`; `CadenceEvent.projectId` optional (B8)
- [x] `make gen`: oapi-codegen strict server, `@hey-api/openapi-ts` client + TanStack Query hooks, `mcpgen` tool
      manifest that refuses verbs outside the vocabulary
- [x] `make lint` / `make test` wired for real (golangci-lint, ESLint, ruff, mypy --strict; go test, Vitest, pytest)
- [x] CI on every pull request running lint, unit, contract and integration layers; nightly job slot reserved for
      agent evals and e2e (later phases), weekly performance job (restore, drag frame time, SSE fan-out, search)
- [x] Contract tests: OpenAPI ↔ generated server/client/tools
- [x] Pin every dependency (web, agent-host); fix `@base-ui/react` name; Go module path to the real origin
- [x] SPA embedded in the Go binary (single-binary shape); compose builds all images with one version

Control plane core
- [x] Postgres via pgx; forward-only migrations embedded, run under an advisory lock
- [x] Fixed development actor (the single admin) so commands carry an actor before real login lands in phase 1
- [x] Command pipeline: actor, idempotency keys (repeat → original result), revisions + `If-Match`, `dryRun`
- [x] Transactional outbox → dispatcher in `seq` order → `GET /events` SSE with topics, wildcards, `Last-Event-ID` resume
- [x] Projects (list/create, minimal) and per-user workspaces `GET/PUT /me/projects/{p}/workspaces/{name}`
- [x] Observability: structured JSON logs (`slog`) with rotation, `/healthz`, Prometheus endpoint, OpenTelemetry
      traces to a file exporter, trace context carried UI → API (extended to jobs and agent tool calls later)
- [x] problem+json errors with `type` → `docs/help/errors/<slug>`; help articles bundled in the binary,
      `GET /help/{slug}`, `GET /help/context`; CI fails when a panel, step kind or error type lacks its article

Web shell
- [x] Vite + React + TanStack Router/Query + Zustand app frame; chrome (menu bar with project switcher, status bar,
      notification history)
- [x] Panel registry + `PanelManifest`; documents vs tools; ESLint rules: no panel↔panel imports, no Dockview outside
      the adapter, no `asChild`
- [x] Selection bus: active document + selection inside it, tool panels follow it or are pinned
- [x] Entity manifest (`EntityManifest`, three state templates) and its primitives `EntityHeader`, `ActionBar`,
      `StatusChip`, `NextStep`; lint rejects hand-drawn headers; list, Compare and empty-state conventions
- [x] Workspaces: `toJSON`/`fromJSON`, `schemaVersion` + migrations, alias map, placeholder for unknown panels,
      default layouts as code factories, debounced save with `If-Match`, deep links `/p/:project/w/:workspace?doc=…`
- [x] Command registry + Ctrl/Cmd+K palette (`>` commands, `?` help), keyboard map, no browser-reserved keys
- [x] Floating-window snapping: pure `computeSnap()` + `dockview-adapter.ts`; keyboard/non-drag alternatives
- [x] Theme: Radix Slate + Indigo tokens in `theme.css`, Dockview `--dv-*` mapping, dark mode, popout stylesheet injection
- [x] Accessibility: WCAG 2.2 AA table (focus, targets ≥ 24 px, live region), contrast CI script
- [x] SSE client: one multiplexed stream, visibility-scoped subscriptions, cache patching, per-frame coalescing
- [x] Generic panels: Library (shell only — lists whatever registry kinds exist; empty until phase 1), Inspector, Help

Tests: `computeSnap` table tests, adapter contract test on the pinned Dockview, workspace round-trip fixtures,
Playwright on drag/dock/float/popout/palette, integration test for outbox → SSE resume.


Phase 0 notes (what differs from the plan above):
- Operations of later phases are already named in the contract (`x-cadence.planned`) and answer 501 until built.
- Dockview's public `transformFloatingGroupDrag` replaced most adapter internals; Dockview's keyboard navigation and
  Smart Guides are enterprise-only, so the command registry owns every window key.
- Three Theming steps changed for contrast (S3); floats are `border-box` (S4). Decision log rows of 2026-09-29.
- Open questions recorded in `docs/spec/07-audit-risks-sources.md`: the `container` state template for projects, the
  `view.*` namespace for client-only commands, 404/405 for unknown API paths.
- Not in phase 0 by design: auth (phase 1 replaces the fixed dev actor), River jobs, Caddy TLS (B7 rest), idempotency
  key cleanup (a phase 1 job), per-user filtering of workspace events.
- After the gate, a staging stand runs at `https://cadence.llmto.day` (compose, one `CADENCE_VERSION`): TLS ends at the
  host's own Caddy with basic auth until phase-1 sign-in, the event stream unbuffered and uncompressed, `/metrics`
  closed; compose publishes the control plane on 127.0.0.1 only and Postgres not at all.
---

## Phase 1 · Agent loop

**Goal.** Claude Code and opencode run as supervised sessions inside a project worktree, act only through generated MCP
tools, and every change they make reaches open panels live and attributed; gated actions stop at an approval.

**Gate — passed 2026-09-30** (branch `feat/phase-1-agent-loop`): A1, A2 and A4 `Status: done`, numbers in each spike's
Result. Run on the real system (Postgres in Docker, `cadence serve`, the SPA on Vite, the agent host with tsx in its
unprivileged mode), a fresh project from `projects.new` per run and the Mix panel open in Chromium. One prompt per
session had the agent set a mix's temperature five times and ask for a training-run estimate. Commit → draft visible
with the session badge, 20 edits per driver: Claude Code (claude-agent-acp, `sonnet`, the owner's subscription) p50 37 ms
/ p95 56 ms; opencode (`opencode/big-pickle`, the free OpenCode Zen model — the owner's MiniMax key is not installed
yet) p50 47 ms / p95 68 ms. The person's concurrent table edit got `412` and the conflict notice in 8 of 8 runs, with
nothing overwritten. Every run ended with a `runs.new` dry-run estimate (0.83 GPU-h, basis table), every mutation was
attributed to its session and tool call, and no permission request reached a person. Two host bugs were found and
fixed on the way (the runaway rule, session cleanup; A4 Result).

Original gate: an agent session (both drivers) edits a mix through MCP; the open Mix panel shows the change as a draft
with the session badge within 300 ms; a concurrent UI edit gets a `412` conflict, not an overwrite (A4 acceptance);
A1 and A2 acceptance met, including a dry-run estimate for a training run.

Decide before starting:
- [x] **decide** → *R2* Session token never lands in git: env substitution / ACP `session/new`, config paths excluded (B1)
- [x] **decide** → *R3* Agent credential isolation: per-session `CLAUDE_CONFIG_DIR`, no host `~/.claude` in the agent's reach (B2)
- [x] **decide** → *R4* Network sandbox for the shell: container egress proxy vs Claude-only guardrail (B4)
- [x] **decide** → *R8* Reserved aliases (`baseline`, `production`) not settable via `aliases.set` (B3)
- [x] **decide** → *R6* Claude subscription for every session kind; opencode on MiniMax Token Plan (owner, 2026-09-29)
- [x] **decide** → *R5* Separate timeouts for idle-in-turn vs waiting-for-approval (C3)
- [x] **decide** → *R11* `defaults.yaml` location and loading — the wizard's Recommended mode and the budgets read it here,
      and pipelines stop hard-coding defaults (C1)
- [x] **decide** → *R13* Mix: saved as a revision of a project entity or frozen as a version (C5) — before the Mix entity

Write before starting:
- [x] **spec** → *R7* Permission presets and the policy engine: the preset format, how the Guardrails table renders into
      `.claude/settings.json` allow/deny/ask rules and opencode's `permission` block, and the rule format the policy
      engine uses to answer requests the table already decides
- [x] **spec** → *R9* Secret storage: where write-only values from `POST /secrets` live (the proposed default "compose env
      file" cannot be written from the UI), how jobs and bootstrap read them
- [x] **spec** → *R10* The internal bare repository: where it lives, how the server clones and pushes, how a person reaches
      it (git over SSH/HTTP or not at all); GitHub repository creation with the stored token
- [x] **spec** → *R12* Estimate model: how `dryRun` computes GPU-hours, duration and data volume (calibration throughput ×
      steps, or a table in `defaults.yaml`) — needed by the phase-1 estimate-only dry run and A2
- [x] **spec** → *R13* `mixes.edit`: the gate has an agent edit a mix, but the spec's tool catalogue has only `mixes.new` and
      `mixes.preview` (05); add the tool and its API operation

Identity and access (06 Authentication)
- [x] First start: create the admin account (replaces the phase-0 development actor)
- [x] Admin: Argon2id password + optional TOTP, HttpOnly session cookie, custom-header CSRF rule, rate-limited login,
      `cadence admin reset-password`
- [x] Credentials table (kind, scope, hash, expiry, last use); agent session tokens; `cdk_` API keys; revoke as a command
- [x] Exposure (B7, R39) on the staging stand: TLS at the host's Caddy, control plane on 127.0.0.1, Postgres unpublished
      (in place since phase 0); basic auth dropped from the Caddy site file on 2026-09-30 once sign-in and the admin's
      two-factor were on (`/metrics` stays 404 outside); a Caddy compose profile stays the option for hosts without a
      proxy of their own

Registry core (completed in phase 4)
- [x] Collections and immutable versions named `YYYY-MM-DD.<sha>`, tags, aliases per project, adoption by reference,
      "used by"; registry events without `projectId`
- [x] Kinds needed now: base model (catalogue, pinned HF revision, its model family id — R41; the descriptor arrives
      with the worker in phase 2), dataset version (registered from fixtures so a mix has something to reference),
      template (instruction templates, permission presets, skills)
- [x] Compute entity (hosts, cards, memory caps, allowed job kinds) — Settings edits it now, the queue uses it in phase 2
- [x] Secrets entity (names only; values per the **spec** above)
- [x] Minimal `defaults.yaml` (wizard fields, budgets, the estimate table) + the `defaults://` resource; full
      `x-cadence` coverage of step parameters follows in phase 2
- [x] Policies `GET/PATCH /policies` (default budgets now; retention, PII and cache quotas added in phases 4–5)

MCP and approvals
- [x] MCP server (Streamable HTTP) generated from operations, curated descriptions, data-marked tool results
- [x] MCP resources: `project://summary`, `selection://current`, `help://{slug}`, `defaults://`
- [x] Approvals (project/registry scope, once / for session), policy engine; in-app notification history for them;
      a gated fixture command for integration tests until real gated commands arrive (over-budget runs in phase 2)
- [x] Drafts on draftable kinds with Accept/Revert; presence ("agent editing"); audit log with `causedBy`
- [x] Training-run dry run ahead of phase 2: `runs.new?dryRun=true` returns an estimate from defaults and the compute
      entity (A2 acceptance); the real run lands in phase 2. Its request carries `init: base | checkpoint` and `gpus`
      from the start (R44), so phase 2 does not change the shape

Agent host (05 Agent integration)
- [x] One ACP client; drivers for `claude-agent-acp` and `opencode acp` behind the ACP-shaped interface (`src/drivers/`)
- [x] Session lifecycle and states: create → worktree on `session/{id}` → spawn → turns → commit per turn → end/merge;
      pause/resume (native or summary injection); failure handling
- [x] Budgets (turns, tokens) and runaway checks (3 identical calls, token limit, inactivity) → pause with reason event;
      confirm in A1 that ACP reports token usage for both drivers
- [x] Transcript persisted as `agent.session.{id}` events; permission requests mapped to approvals
- [x] Worktree watcher emitting `recipe.{path}` events; session branch merge: fast-forward when clean and allowed,
      else "Session changes" with three-way diff
- [x] Per-session worktree mounts only; images install both agents; driver contract tests on recorded transcripts
- [x] Session kinds: interactive and read-only ("Explain this") now; playbook in phase 2; scheduled in phase 5

Projects
- [x] Wizard (Recommended three fields + Customise; the recordings-mount field is skippable until phase 4) and
      `projects:bootstrap` job: repository (GitHub or internal bare), `project.yaml`, `AGENTS.md`/`CLAUDE.md` from
      instruction templates, agent config from permission presets, product skills from `control-plane/templates/skills/`,
      starter pipelines, default workspaces (language packs join via `projects.sync` in phase 3)
- [x] Agent profile + `PATCH /projects/{p}/agent-profile`; catalogues (base models, agent models, instruction templates)
- [x] `projects.note` → `NOTES.md`; `projects.sync` / template sync as a draft commit
- [x] Mix entity, minimal (enough for the gate): `mixes.new|edit` with revisions and drafts over fixture dataset versions
- [x] Context bridge: selection → prompt references (`@run:123`), Ask agent / Ctrl/Cmd+I, references in replies as
      links, attribution badge → tool call in Chat
- [x] Project archive (read-only artifacts, worktrees removed); source archive comes with Sources in phase 2
- [x] Saved searches: Saved search entity, `PUT /me/projects/{p}/views/{name}`, Library views and palette entries
- [x] Search index fed from the outbox (Postgres FTS + trigram), qualifier grammar, `search.query` (`projects.search`, R1), palette entity search

Panels: Chat, Agent sessions, Approvals, Agent settings, Settings (compute, secrets write-only, catalogues, credentials),
Getting started, Project document, Mix (minimal), Recipe (read + session-branch diffs).

Tests: integration for tokens, approvals, drafts, `412`; agent evals harness on a fixture project with both drivers.

Phase 1 notes (what differs from the plan above):
- The agent host speaks its own protocol, `hostSessions.claim|report|ask|decision|release` with a `cah_` credential
  (`CADENCE_HOST_TOKEN_FILE`), and the session token is minted at claim, not at create (05 "Phase 1 as built"). A host
  that shuts down releases its sessions, so the next host takes them at once; the Chat reads "reconnecting" meanwhile,
  and a turn or permission request the restart interrupted is reported to the agent as interrupted, not declined.
- MCP tool names are sanitised by the agents: `mixes.get` is `mcp__cadence__mixes_get` in Claude and
  `cadence_mixes_get` in opencode; the renderer and the server map them back (A2).
- `recipe.{path}` events come from pushes (a session's per-turn commit, UI commits, pushes to the internal repository)
  as `recipe.changed`, and from the agent host's worktree watcher while a turn runs as `recipe.working` (paths and
  sizes of uncommitted files through `hostSessions.report` `working`, kept as `AgentSession.working`; no content, so
  the diff shows once the turn commits). Session branches merge by `agentSessions.accept` (fast-forward or a merge
  commit, `409 merge-conflict`) or auto-merge when clean at session end; a conflicting file opens in a three-way view
  (`branches.compare`: base, main, branch and diff3 hunks) in the Chat's "Session changes" and the Recipe branch
  view. Conflicts are resolved on the branch, not in the browser.
- The gated command for tests is a real one: `aliases.set baseline` (R8) returns an approval.
- The agent evals harness is `agent-host/evals/` (`make evals`, after the gate): a fresh fixture project per eval ×
  driver through `projects.new`, graders over the API (drafts, aliases, approvals, audit, session use), JSON results
  plus a table. Offline (CI) a scripted ACP agent plays each prompt's reference answer through the real host, preset
  and MCP; `CADENCE_LIVE_AGENTS=1` runs the real drivers. First evals: the gate prompt, a read-only "Explain this"
  session, `aliases.set baseline` → approval. Live runs are by hand (no model accounts on CI runners); they run on the
  staging stand through its own agent host (`CADENCE_EVALS_TARGET`, a project key allowed agent sessions). First live
  run, 2026-09-30: 6/6 — Claude `sonnet` and opencode `minimax/MiniMax-M3` (MiniMax found the earlier run's pending
  baseline approval, named it and did not ask again; the grader accepts that on a shared project).
- The gate ran opencode on the free Zen model; the owner then connected MiniMax (`minimax/MiniMax-M3`) in Settings →
  Agents and checked both agents on the staging stand (2026-09-30). `haiku` was too unreliable for the gate prompt
  (subagents, a runaway pause), so Claude runs on the profile default `sonnet`.
- Closed after the gate (2026-09-30 punch list): opencode's commands get the agent's tool-call id once the host
  reports the call, so the badge lands on it; claude.ai connectors are off in every Claude session (a setup-token
  lacks the scope anyway, checked on the stand; `ENABLE_CLAUDEAI_MCP_SERVERS=false` covers an interactive login);
  Claude no longer asks permission for the Cadence tools the preset allows (Claude Code ignores a repository's allow
  rules, so the control plane sends them as `HostStart.allowedTools`); an idle-paused session reads "asleep" and wakes
  on the next message; layout autosaves are debounced and not audited.
- Agent accounts are connected in Settings → Agents (`agentCredentials.*`); `docker compose run --rm -it agent-host
  login claude|opencode` stays as the CLI fallback. Basic auth is off the stand since 2026-09-30 (Exposure, above).

---

## Phase 2 · Training

**Goal.** A run is one reproducible optimisation stage — pinned base or checkpoint, frozen mix, committed recipe,
step budget — scheduled on a card under its memory cap, watched live and continued by resume or a new stage.

**Gate — passed 2026-10-01** (branch `feat/phase-2-training`, stand at `fa68968`): a Claude Code session (`sonnet`)
running the "Fine-tune from a dataset version" playbook in project `test` on the staging stand, started from a project
API key allowed to run sessions. The playbook dry run showed the estimate first (0.19 GPU-h, within the 8 GPU-h daily
budget); the agent mixed `dataset/fleurs-he` (FLEURS he_il imported through `pipelines/import`, 9.18 h train + 101
validation clips, the source cleared for training by the owner through an approval), calibrated
(`oomptimizer_calibrate`: 0.491 s/step, base batch 13), dry-ran then started `runs.new` (300 steps, estimate basis
measured, 0.085 GPU-h with lease overhead; actual 0.090 GPU-h, 5.4 min), followed it and listed the checkpoints: one
checkpoint at step 300 registered with validation WER 0.379 (top-k kept). The plan ticked on the server, every spending
command had its dry run first, metrics (loss, lr, grad norm, throughput, card memory, val WER) streamed to the Run and
Metrics panels, and the step process peaked at 21.5 GB under the 22 GB cap with vLLM untouched (23.8 GB). A3: training,
streaming eval and ONNX parity (0.00 points) met; Triton holds ~2 real-time streams (phase 5). Seams: the toy pack
passes the conformance suite in CI and `internal/contract/seams_test.go` keeps framework names out of Go and web code.
The run found two stand issues, fixed on the branch: a project bootstrapped before phase 2 keeps old-format pipelines
until `projects.sync` (the agent stopped and explained; the sync fixed it), and an API key's session opt-in did not
cover `playbooks.run`. The real-card rehearsal before it is in `docs/review/2026-10-01-phase-2-rehearsal.md`.

Original gate: the agent (a session running the phase-2 playbook below) calibrates and runs a Nemotron 3.5 fine-tune on the staging card under the
memory cap (22 GB on the 48 GB staging card beside vLLM; the spec's 24 GB did not fit, spike A3) from a dataset version, with a dry-run estimate shown first; metrics stream to the Run panel; checkpoints
are registered with validation WER. A3 acceptance met (incl. ONNX parity numbers recorded for phase 5). The seams
hold: the CPU toy pack passes the conformance suite in CI, and no control-plane or web code names the Nemotron family
(R40–R45).

Decide before starting:
- [x] **decide** → *R18* How phase 2 gets training data before the Data phase — proposed: an import step that registers a
      pre-built Shar / NeMo manifest (FLEURS subset, the A3 Hebrew subset) as a frozen, fingerprinted dataset version
      through `pipelines.run` (`pipelines/import:run`), registering a Source with the licence the format carries (03)

Write before starting:
- [x] **spec** → *R14* Worker ↔ control-plane protocol: how a Python worker on another host leases River jobs it cannot
      consume directly, reports progress, heartbeats, streams logs and metrics, and receives secrets; the worker's
      credential kind (missing from the credentials table)
- [x] **spec** → *R15* Artifact and content store: how step outputs (manifests, Shar shards, checkpoints, eval reports) are
      written, addressed by hash, read by the next step and the UI; where it lives before mounts exist
- [x] **spec** → *R15* Metric and log storage: schema and retention for `run.{id}.metrics` points and `job.{id}.log` lines
- [x] **spec** → *R16* Playbook definition format: chain of pipelines, prefilled prompt, estimate, plan ticks; plus a
      "Train from an imported dataset" playbook (mix → calibrate → train → register checkpoints) — none of the four v1
      playbooks can run before phase 4, yet the phase-2 gate is a playbook session
- [x] **spec** → *R17* Replay data: which corpus supplies the 15 % replay share across the base model's other 39 locales
      (e.g. FLEURS per locale), its sources and licences, and the replay-locale golden sets the phase-3 gate checks
- [x] **spec** → *R19* Compute availability windows on the shared staging card (07 "Also unspecified")
- [x] **spec** → *R40–R45* Extensibility seams, before step kinds and the worker harden: a runtime on every step kind
      and in the lease; the Nemotron model-family descriptor (roles, latency profiles, capabilities, defaults section);
      neutral artifact types (`hypotheses`, `analysis`, `deployable`); `init` and `gpus` in the run and resource
      schemas; the conformance suite. Everything beyond the seams is deferred (owner, 2026-09-29)

Environment
- [x] Staging host ready: GPU card with the 24 GB cap, NeMo Speech 26.07 container pinned by digest, the base model
      at its pinned revision, other resident services left intact (A3 setup) — done on the stand 2026-10-01;
      the cap is 22 GB (A3), the runtime descriptor pins the image digest (`worker/runtime/nemo-speech.json`)
- [x] GPU telemetry for the `gpu` topic and the status bar (card memory and compute per card) — rides on claims and
      heartbeats (no separate collector), `gpu.telemetry` throttled per host; the status bar's GPU badge (memory
      used/total and utilisation per card, stale after 2 min) opens Queue & GPU

Worker and jobs
- [x] Worker per the protocol above: publishes the step registry at start, runs steps as subprocesses, heartbeat,
      reaping after 3 missed beats; secrets injected into jobs only — an HTTP long-poll protocol (`worker` tag, `cwk_`),
      not River leases (notes)
- [x] River queue: one training slot per card, per-project priorities, pause/resume/cancel, `jobs.wait` — the waiting
      queue is the `step_jobs` table; order: project `queuePriority`, job priority, first come
- [x] Artifact store per the **spec**; OpenTelemetry trace carried into jobs — request → `job <kind>` span → the
      worker's `step <kind>@<version>` span (`TRACEPARENT`, log fields, optional span file)
- [x] Pipeline engine: YAML pipelines pinning `kind@version`, artifact types validated at `dryRun`, per-step status,
      retry one step, input-hash idempotence — `pipelineRuns.list|get|cancel|retry|wait`; reuse is per project
- [x] `defaults.yaml` + `x-cadence` on every step parameter; "departures from defaults" recorded on entities — the
      worker refuses to publish a kind with an incomplete `x-cadence` and the conformance suite checks it
- [x] OOM → typed error → one retry at 0.75× batch
- [x] Playbook engine and playbook sessions (plan from the chain, `dryRun` before each spending step, estimate first);
      the fifth v1 playbook "Fine-tune from a dataset version" (R16) — the gate runs it (`templates/playbooks/
      finetune-from-dataset.yaml`; people start playbook sessions, agents do not)
- [x] Minimal data entities for imports: Source (licence, eval-only until cleared, archive), Utterance, Transcript,
      per-utterance fingerprints — the full ingest path arrives in phase 4 (stream D: internal/data, `sources.*`,
      `utterances.*`, the `dataset` output hook)
- [x] Replay corpus and replay golden sets imported per the **spec** above — on the stand 2026-10-02 from
      `pipelines/replay-base.yaml` (`dataset_import@3`, streamed and capped): `dataset/replay-base` with 34 locales,
      34.06 h and 10 789 utterances, and 34 eval-only golden sets `dataset/replay-golden-<locale>` (≤ 300 test
      utterances each); the source `fleurs` stays eval-only until a person clears it for training
- [x] Runtimes (R40): the worker announces `runtime@version` and the NeMo runtime is registered from it;
      `runtimes.list|get`; card slots are owned per host and card, so two runtimes could share a card; `runtimes.new`
      (a second runtime, with approval) waits with the deferred packs — `nemo-speech` registers when its worker
      starts (stream N image); `toy` registers in CI
- [x] Model families (R41, R43): published by the runtime; Nemotron 3.5 streaming first, with latency profiles
      `80ms`–`1120ms`; base models and checkpoints carry their family; no code branches on a family name — the seams
      are built (entry point `cadence.families`, `modelFamilies.list|get`, `family` on runs and checkpoints, a test
      that no control-plane or web source names the family or runtime); the Nemotron descriptor is published by the
      NeMo worker (`model-family/nemo.fastconformer-rnnt.cache-aware` on the stand, five latency profiles)
- [ ] CPU `toy` framework pack (a tiny CTC model) and the conformance suite in CI; the NeMo pack runs it nightly (R45)
      — the toy half is built (`make conformance` in CI; the trained model must beat a one-step baseline and reach
      WER ≤ 0.1). The NeMo pack passed the full suite on the RTX PRO 6000 card on 2026-10-01 (calibrate 0.109 s/step,
      train → stop → resume → average, transcribe at all five profiles, WER 0.57–0.70 under the 0.9 bound) — the
      `nemo-conformance` job in `nightly.yml` is real but still waits for a runner on the staging host (the repository
      is public, so a self-hosted runner is not used; a host cron is the proposal)

Step kinds: `oomptimizer_calibrate`, `nemotron_finetune` (bf16, Noam with computed peak LR shown, `target_lang`),
`checkpoint_register` (top-k), `checkpoint_average`, minimal dataset import, `nemotron_transcribe` (file decode in
streaming simulation at a latency profile → `hypotheses`; the evaluation reuses it in phase 3). Echo step updated as the real template
(x-cadence, input hash).

API/MCP: `mixes.preview`, `pipelines.list|run`, `runs.calibrate|new|resume|stage` with real estimates, `jobs.*`, `metrics.get`,
`checkpoints.list|average`; GPU budget per project/session with over-budget approval (the first real gated command);
`runtimes.list|get`, `modelFamilies.list|get`.

Also:
- Telephony augmentation profile applied on the fly: codec, band-limit, level and speed transforms now; background
  noise from a public set imported as a registry noise-bank version; own-call noise waits for phase 4;
  `augment.preview` once audio serving exists (phase 3); `augment.evaluate` with the robustness matrix (phase 3).
- Notifications: Telegram bot with inline approve/deny, routing table, quiet hours, daily digest.
- Operations: nightly `pg_dump` + artifact-store copy to a local path (to a mount once phase 4 lands), weekly restore
  test, upgrade path, log and audit retention jobs.

Panels: Run, Mix (full, with preview), Queue & GPU, Metrics, Checkpoints, Logs; Training workspace. Metrics is the first
chart on the stack and tokens of R53 (chart colours join the contrast and colour-vision checks).

Phase 2 notes (what differs from the plan above):
- Audit of 2026-09-30 (stream H): every API/MCP operation above is served (no 501s); the Also items and the panels
  are built (plus a Pipeline run panel); the framework step kinds, the family descriptor and the augmentation
  transforms are the NeMo pack's (stream N); `dataset_import` and the updated `echo` are built.
- Workers do not lease River jobs: they long-poll an exempt `worker` tag with a `cwk_` credential (one per host,
  `cadence admin worker-token`); a River `step` job's handler waits on its own queue for the outcome and the waiting
  queue is the `step_jobs` table (06 "Worker protocol"). Completion is `workerLeases.release`; a heartbeat answers
  `stop` with a reason (`cancelled`, `paused`, `window-closed`); one subprocess per lease; packs are separate Python
  distributions and runtime-neutral core kinds (`echo`, `dataset_import`) ship in every image.
- `pipelines.run` answers 201 with the pipeline run, not 202 with a job; `pipelineRuns.list|get|cancel|retry|wait` carry
  the rest. Output hooks run in a savepoint of the step's `done` transaction and also for reused outputs, so they are
  idempotent. Runs are facades over one `train-stage` pipeline run: there is no `runs.pause|cancel` (the Run panel
  uses `jobs.*` on the run's current job) and `runs.resume` takes a failed or cancelled run. `checkpoint_register` is
  not a step kind: the control plane's `checkpoint` output hook registers checkpoints and keeps the top k
  (`training.keep_top_k`). A `base_model` input accepts a checkpoint, so one recipe serves both inits.
- GPU spend is lease time on GPU cards, per project per day (instance time zone) and per agent session; the policy
  engine gates an agent's spending command over budget with an approval; people are not gated; calibration and
  averaging count. The staging card is a 48 GB Blackwell and its training cap 22 GB at a measured 0.7 s/step (A3).
- Data: imports are canonical 16 kHz 16-bit WAV named by BLAKE3; the `dataset` artifact is a directory with
  `manifest.jsonl`, fingerprinted over (audio, split, text); new sources start eval-only. Replay is capped at ≈ 1 h per
  locale from FLEURS (34 of 39 locales, five through a sister variant); golden sets are dataset versions for now.
  Background noise is its own registry kind `noise_bank` (`dataset_import` with `purpose: noise`,
  `pipelines/noise-bank` for the MUSAN noise subset); applying it on the fly joins the NeMo pack's codec, band-limit,
  level and speed transforms.
- Playbooks run from the bundled templates; people start playbook sessions (`playbooks.run` is under
  `sessions-are-for-people`); the dry-run rule is per operation, used up by the real call, and enforced in playbook
  sessions only.
- Telegram is long-polled from an outbox cursor (no webhook); quiet hours drop Telegram messages rather than defer
  them; a button press decides as `usr_admin`. Backups are `pg_dump` custom format plus the content-store mirror and
  the sealed secrets (never the master key); job logs are NDJSON files kept 14 days; content-store blobs and metric
  points are never deleted. The chart palette adjusts R53's step-9 rule (00 decision log) and neighbouring series
  differ by dash as well as colour.
- Closed in hardening (stream H, 2026-09-30): the status bar's GPU and Queue badges; one trace from the request
  through the job span to the worker's step span (`step_jobs.traceparent`); the noise bank; the R41 seam test (no
  control-plane or web source names the family or runtime); a job cancelled between River's fetch and its handler
  now ends cancelled (the flaky pipeline-run cancel); the toy model converges (a layer norm: WER 0 at 300 steps on
  every seed tried) and the conformance suite requires the trained model to beat a one-step baseline.
- Still open, not small (07 "Open questions"): per-card health and `compute.card_closed`; job-log field search; the
  nightly NeMo conformance runner; backups to a separate host (the 16 TB backup server); the noise import; clearing
  the `fleurs` source for training (a person's licence decision, R17).
- Hardening after the gate (2026-10-01, PRs #8 and #10 and their follow-ups), from an audit of the phase and a day on
  the stand (a Serbian fine-tune, budget and restart reproductions): budgets fail closed and count queued work; reaper
  grace after a control-plane start; language check before GPU time and shared parameters; transliteration and a
  streaming capped import (`dataset_import@3`); periodic training states published mid-lease and resumed after a lost
  lease; optional step outputs (a finished fine-tune writes no training state); training states skip the backup
  mirror (07 D); the content store in Settings with a low-space warning; the Recipe editor; the agent's own commits
  scanned and pushed; Telegram `/status` and `/approvals`; and every High, Medium and Low audit finding except two the
  owner took (step access to the worker token, binding an artifact to a project by its hash).

---

## Phase 3 · Evaluation

**Goal.** A checkpoint is judged only in true streaming at deployment latency, on frozen golden sets it never
trained on, against the baseline, with confidence intervals, and a gate turns the matrix into a verdict.

**Gate.** An eval matrix of a phase-2 checkpoint on the imported golden sets (FLEURS he plus the replay-locale sets
from phase 2) runs on staging and `evals.gate` returns a verdict (target-locale WER vs baseline with bootstrap CI,
replay-locale regression ≤ 0.5, deletions/insertions check). The telephone golden set from own calls needs phase 4.

**Gate run (2026-10-02, staging).** `evals.gate` returned a verdict on eval `evl_01a0fd78…` of the phase-2 Serbian
checkpoint (`ckp_01a0f902…`, run `run_01a0f902…`, 1 000 steps on `dataset/fleurs-sr-latn` under the `hr-HR` prompt, no
replay) against the base model, on 35 frozen golden sets: `golden-set/fleurs-sr-latn-test` (700 utterances, 2.1 h;
FLEURS he was replaced by the locale the phase-2 checkpoint was trained on) and the 34 replay golden sets (33 h).
76 cells, 79 audio hours computed in 25 minutes (0.81 GPU-hours of transcribe steps), `languages: {sr-RS: hr-HR}`.
- Verdict **failed**, as it should: the target passed — WER 0.346 → 0.256 at `160ms`, Δ −0.090 [−0.101, −0.081], and
  deletions were not traded for insertions — but 32 of 34 replay sets regressed far beyond 0.5 points (e.g. ru-RU
  0.149 → 0.447, de-DE 0.109 → 0.326, uk-UA 0.167 → 0.717): catastrophic forgetting of a run without replay, which is
  exactly what the gate is for. hr-HR held (+0.004, interval includes zero); th-TH passed only because the base model
  emits nothing for it (below).
- The target across profiles (subject / baseline WER): 80 ms 0.266 / 0.356, 160 ms 0.256 / 0.346, 320 ms 0.246 /
  0.333, 1120 ms 0.234 / 0.310; unstable partial word ratio 0.61, 0.55, 0.40, 0.16; latency to final (simulated pace,
  frame-VAD ends) p50 ≈ 1.05–1.15 s at every profile — the endpointer, not the look-ahead, dominates it. The choice of
  the primary profile (160 ms is not a trained look-ahead, A5) waits for the owner (07).
- Found and fixed on the way: leakage checks counted a trainable import's test split (now train and validation only);
  evals could not decode a locale the model has no prompt for (`evals.new.languages`); zh/ja/th were scored by words
  over unsegmented text (now CER, `eval.character_error_languages`); the eval GPU estimate was 8× high (now 0.025
  GPU-hours per audio hour: the pipeline decoder at batch 8 runs at RTF 0.0165, plus a 1.5× margin). Open: the base model's empty output for th-TH (checked with stream T's
  decoder).

Decide before starting:
- [x] **decide** → *R20* The latency set and primary cell (`[56,1]` vs `[56,0]`, offline vs `[56,13]`) (C2), named as
      latency profiles (R43) — `160ms` primary, matrix `80ms`/`160ms`/`1120ms` (03 "Key defaults", `eval.*`)
- [x] **decide** → *R22* What evaluations are keyed by before a model version exists: registry Eval records are keyed by
      model version, but phase 3 evaluates checkpoints and registration is phase 5 — key by checkpoint content hash,
      or move `models.register` into phase 3 — both: weights-hash key (plan decision 4) and `models.register` in
      phase 3 (02 "Evaluation entities")
- [x] **decide** → *R23* Baseline before anything is in production: the base model at its pinned revision — its
      `base_model` version is the baseline, set with `aliases.set` (plan decisions 1–2; 02 "Evaluation entities")

Write before starting:
- [x] **spec** → *R21* Normalizer identity: 02 makes it a registry entity (Eval records keyed by normalizer version), 03 makes
      it a file in a project language pack with "no registry version" — a cross-project Eval record cannot pin one
      project's file SHA; pick one — 02 "Evaluation entities", 03 "Language pack"
- [x] **spec** → *R25* Audio serving: an endpoint that streams an utterance span (range requests) for the Audio panel, with
      a play-only mode for reviewers; missing from the 13-item backend contract — 06 "Media", 11 "Backend contract"
- [x] **spec** → *R47–R50* Manual transcription tests and the live channel: message schemas in the contract, the `media` tag in
      the generator, interactive jobs beside training, capture defaults; *R51–R54* the audio view, spectrogram defaults,
      charts, streaming metric definitions — 06 "Media", 10 "Audio view and charts", 11 "Panel catalogue", 03
      "Scorers and metrics", 04 "Block 3"

Spikes before the items they gate: A5 (live transcription) before live mode — it needs a phase-2 checkpoint and the
worker protocol; S5 (audio view) before the Audio panel — it needs only the phase-0 shell and can run any time before.

- [x] Golden sets as registry assets from imports (FLEURS he), freeze with approval, fingerprint exclusion, runs
      cannot reference them; adoption re-runs the leakage check against fingerprints of imported versions — as built:
      frozen from a dataset version registered eval-only, one locale each, an approval for people too; the checks
      count only train and validation splits (02 "Evaluation entities")
- [x] Language packs `lang/<locale>/` (normalizer, ITN, translit, LID, boost lists, golden recipe); he-IL starter pack;
      added to existing projects through `projects.sync`; entities pin the pack SHA; a new normalizer version forces
      a new baseline; the search index starts using per-locale normalisation — starter packs for he-IL and sr only;
      `langpacks.list|get|edit`, `boost.edit`; sync is three-way by `data.lock`; no operation freezes a new normalizer
      version yet (seeds `normalizer/basic`, `normalizer/he-il`)
- [x] `streaming_eval` (cache-aware, per latency profile — R43), WER/CER/S/D/I, per-utterance rows, duration buckets,
      punctuation-insensitive companion score — no `streaming_eval` kind: `evals.new` generates the eval pipeline
      (the family's `materialize` and `transcribe` roles → core `wer_score@1`; `werNoPunct`; 03 "The eval pipeline")
- [x] Scorer step kinds available now (R54 definitions): entity accuracy for number classes (names and addresses need
      annotated spans, phase 4); latency to final at p50/p95 with audio at real-time pace, utterance ends from a NeMo
      frame-VAD model until per-channel VAD lands in phase 4; partial stability as the unstable partial word ratio.
      Emission delay PR50/PR90 needs aligned references and end-of-utterance needs per-channel VAD (both phase 4); RTF
      and streams per card come from the benchmark step (phase 5) — `entity_score@1`, `latency_score@1` (core) and
      `frame_vad@1` (NeMo, MarbleNet v2.0) write `metric_scores`; partial stability is in `wer_score@1`. Only WER is
      gated; entity accuracy, latency and stability are reported. Latency uses a simulated real-time pace; emission
      delay waits for phase 4
- [x] Eval records cache (per the **decide** above); only missing cells computed — `eval_records` (global) and
      `eval_metrics`; not yet protected from eviction
- [x] Baselines (approval), gates per project (`gates.edit`), 1 000-sample bootstrap CI on every delta, resampling whole
      calls or speakers (R54) — baseline = `aliases.set baseline`; `gates.yaml` in the repository; the gate reads
      decoding 0 and augmentation 0 at the primary profile; `call` groups wait for call ids (phase 4)
- [x] Decoding config as an eval axis (R24): static RNNT context biasing in the eval decoder; `langpacks.get|edit`,
      `boost.edit`; boosted vs unboosted cells in `evals.new` (entity recall + general WER); the Language pack
      "test a phrase" box as a one-utterance eval — `nemotron_transcribe@2` (NeMo GPU phrase boosting, weight 0.5);
      boosted cells compare general WER; boosted-term recall has no scorer yet and "test a phrase" is not built
- [x] `models.register` (R22): publish a checkpoint whose gate passed, with its eval report and model card; export
      stays in phase 5 — `POST /projects/{p}/models:register`, an approval for agents, adopted by the project
- [x] Robustness matrix: augmentation profile as another `evals.new` axis (golden set × profile × latency) —
      `augment_dataset@1` on target golden sets; GSM-FR, AMR-NB and Opus left out of the draw; `evals.get` answers
      the matrix, the Eval report draws it in its Robustness section (stream U2)
- [x] Experiments and sweeps: grid/random over recipe params, GPU-hour cap, Compare N, register best —
      `experiments.new|list|get`, `sweeps.run` (one per project, approval covers its runs); Experiment document
- [x] Lineage both ways: `GET /registry/{kind}/{id}/lineage`, "used by" (before the Lineage panel) — one read,
      `GET /registry/{id}:lineage` (`direction`, `depth`, `limit`), with the Lineage panel
- [ ] Audio view (R51, R52) as a shell primitive: waveform, spectrogram (FFT in a Web Worker, WebGL2), model input and
      emissions from the transcribe step, hypothesis words with confidence, reference/hypothesis alignment (S/D/I),
      streaming timeline; spans as selections and chat references (`#t=`); TextGrid, CTM and WebVTT export
      — built 2026-10-02 (stream A): `@/shell/audio` with waveform, spectrogram (worker FFT, one WebGL2 renderer per
      window, server tiles from `spectrogram_tiles@1`), hypothesis words with confidence and S/D/I, spans and `#t=`
      references, the three exports, the Audio panel, Diff's embedded view; open: model input and emissions (needs
      the transcribe step's `analysis` artifact), streaming timeline lane, presets, TextGrid/CTM import
- [x] Transcription tool (R47–R50): a file, the microphone or an utterance span through one WebSocket session; up to
      three targets, blind compare; telephony simulation; a typed reference gives WER on the page; nothing is stored;
      "test a phrase" becomes a two-target transcription
      — built 2026-10-02 (stream T; 06 "Transcriptions and the live channel as built"): `transcriptions.new`, the
      relay, the worker's `live` role (NeMo and toy), the Transcription panel; open: "test a phrase" in the Language
      pack, `analysis` (features, emissions), Firefox/Safari/Caddy checks (owner)
- [x] Eval charts (R53): matrix heatmap, forest plot of deltas with intervals, S/D/I, buckets, latency CDFs, WER
      against latency; the utterance table opens rows in Diff and Audio
      — built 2026-10-02 (streams U, U2): beside the four above, the per-utterance WER ECDF (subject against
      baseline; `evals.get?worst=` caps at 200, so a larger golden set shows its worst 200 rows, labelled), entity
      accuracy per class, and folding "Streaming" (WER against latency per model with the primary marked and the
      delta's interval, latency to final as p50/p95/max per profile — the API has no full distribution — with the
      unavailable reasons, partial stability) and "Robustness" (degradation heatmap) sections; `@/shell/charts` gained
      the `line` spec. Not drawn (no API data yet): confusion pairs, WER by SNR, bandwidth or speaker, emission delay
- [x] Generator: the `media` tag — exempt from the verb rule and from MCP — for R25's audio endpoint, `…/peaks`,
      `transcriptions.new` and its socket (R48)
      — the tag and audio serving built 2026-10-02 (stream A: `audio.get|sign`, `peaks.get`, `spectrogram.get`,
      `words.get`); `transcriptions.new` and `stream.connect` by stream T
- [x] Queue: job kind `interactive` beside training under the card's cap, never beside a benchmark, 1 GPU-hour per
      project per day (R49) — built 2026-10-02 (stream T): leased before every other kind; the reservation comes
      from the family descriptor (`interactive`); lease wall time against the manual-test allowance

Panels: Eval report, Diff, Audio, Golden set, Lineage (over what the registry holds so far), Language pack,
Experiment, Transcription; Eval workspace.

Phase 3 notes (what differs from the plan above and from `docs/review/2026-10-02-phase-3-plan.md`; streams G, Y, E,
L, R, U, X and spikes S5, A5, folded into the spec by stream S2 on 2026-10-02):
- Golden sets are frozen only from dataset versions registered `evalOnly`, hold one locale, and are an approval for
  everyone (people included); `groups: call` waits for call ids. Leakage checks count only the training side's train
  and validation splits (found at the gate: FLEURS sr's test split sat beside its train split). The seed is
  `normalizer/he-il` (collection names are lower case); Serbian scores with `normalizer/basic`.
- No `streaming_eval` or `gate_evaluate` kind: `evals.new` generates one pipeline per eval with only the missing
  cells (`materialize-m<n>` → `transcribe-u<n>` → `score-u<n>`, plus augment, VAD and metric steps), answers `201`
  with the eval, and estimates 0.025 GPU-hour per audio hour (`eval.gpu_hours_per_audio_hour`, calibrated on the
  stand). Base models are materialised by the family role
  `materialize` (`checkpoint_from_base@1`, toy `toy_checkpoint_from_base@1`); step kinds may declare
  `optionalInputs`.
- The gate reads decoding 0 and augmentation 0 at the primary profile; without golden sets in `gates.yaml` the
  project's locales are targets and the rest replay. `models.register` is a project path that writes a registry
  version (`model/<project slug>` by default), needs a passed gate, is an approval for agents, and adopts the version.
- Metrics beside WER are their own artifact (`metric_scores`, table `eval_metrics`) and are reported, not gated;
  latency to final uses a simulated real-time pace and the NeMo pack's frame-VAD (pinned in `packs.nemo.vad_*`). The
  augmentation enters the decoding hash. Boosted-term recall has no scorer; the default boost weights disagree
  (`langpacks.boost_weight` 1.0 vs the measured 0.5; 07 "Open questions").
- Lineage is one operation (`GET /registry/{id}:lineage`) over the ids a payload names; evals add their own source.
  Agents' language-pack edits land on a `langpack/<locale>-<date>` branch under the draft policy.
- Web: one `models.register` and one `evals.new` command; the Eval report draws every chart of the ROADMAP item
  (stream U2 added the ECDF, entity accuracy, the Streaming and Robustness sections). Audit fix F4 added evaluation
  setup: Adopt into project on the Golden set, Set as baseline (`aliases.set`, approval-gated) on the Model document
  and a base model, a gate editor (`gates.get|edit`) and the Run eval… form (`evals.new` with every axis, dry-run plan
  first). "test a phrase" and the Eval workspace's Playwright smoke are not built.
- Spike S5 (done with caveats): no wavesurfer.js; `AudioView` + `useAudioAxis()`, one renderer per window; peaks 720 KB
  per channel-hour; a JavaScript FFT in a Web Worker; new `views.audio` defaults (stream A adds them).
- Spike A5 (partial; Firefox, Safari and Caddy left to the owner): the live path meets the 160 ms budget beside
  training (p95 finalize → final 43 ms, 70 ms beside training); the interactive reservation is 6 000 MB + 2 600 MB per
  further checkpoint; live and eval need one decoder (the transcribe kind moves to NeMo's pipeline API) and one
  polyphase resampler; `160ms` is not a trained look-ahead — the primary cell is the owner's call (07).
- Not done: eviction protection of eval records and a playbook estimator for `evals.new` (the playbook's eval step
  uses a fixed 0.5 GPU-hour hint). Done since waves 1–2: the eval GPU-hours factor is calibrated (0.025),
  `playbooks.CurrentPhase` is 3, and streams A (audio endpoints, the `media` tag, the Audio panel) and T
  (transcriptions, the `interactive` job kind) are built (items above).

---

## Phase 4 · Data

**Goal.** Raw corpora on mounts become frozen, fingerprinted, licence-checked dataset versions in the base model's
text style, indexed in place and materialised only when a job needs them; the registry pattern is complete; the
telephone golden set is built from own calls.

**Gate.** A dataset version ingested from a mount and frozen by the `data-ingest` pipeline (leakage check passed,
dataset card generated) is trained on through the phase-2 path — ideally as the "Adapt a new language" playbook.

**Gate run (2026-10-04, staging).** Passed as a data path; the model it produced failed its own gate, as the phase-3
one did. Mount `corpora` (approved) → `pipelines/pseudo-label` (`plr_01a10639-2160…`): `sdp_ingest@2` indexed the
2 944 FLEURS Serbian train files in place (10.7 h, `test/` excluded by default), Nemotron (`hr-HR`, 80 ms) and Whisper
large-v3 transcribed them (OASIS skipped: not adopted), Whisper LID, and the ensemble kept 490 segments (1.9 h) and
sent 2 454 to triage → draft `dataset/fleurs-sr-pseudo` → `datasets.freeze` (leakage passed against 35 golden sets,
quality checks passed, card written): 475 utterances, 1.84 h → mix with `dataset/replay-base` at 0.15 → run
`run_01a10658-e06d…` (1 000 steps, 5 min, best validation WER 0.140 at step 500, measured on pseudo-labels) → eval
`evl_01a1065d-870e…` (72 cells, 77 audio hours) → `evals.gate`:
- Verdict **failed**: target inconclusive — WER 0.356 → 0.349 at `80ms`, Δ −0.007 [−0.016, +0.002]; replay 31 of 33
  sets regressed (German 0.125 → 0.314, Ukrainian 0.175 → 0.545) despite the 0.15 replay share at peak LR 2e-4.
- Pseudo-label quality against the withheld FLEURS transcripts: Nemotron alone 0.336, Whisper alone 0.121, the kept
  labels 0.121 — with the base model as a member, agreement keeps only what the weak member gets right.
- Leakage demo: the `test/` folder ingested on purpose (`exclude: []`, `.txt` sidecars from `test.tsv`) drafts fine and
  its freeze answers `golden-set-leakage`: 700 of 700 utterances shared with `golden-set/fleurs-sr-latn-test`.
- Synthetic calls: `data-ingest` on `calls-synth-sr` indexed 577 segments (315 caller, 262 bot with script text); the
  annotation batch `calls-synth-sr-1` (40 items, 4 double, end-of-utterance p50 2.3 s, p90 6.1 s) waits for people.
- Seven product bugs found on the way were fixed in the branch (Phase 4 notes). The playbook ran in parts by hand
  (CLI), not end to end as an agent session; the Try Cadence smoke stopped at its source-clearing step.

Decide before starting:
- [x] **decide** → *R45* The models phase 4 needs that are not NeMo models, while packs beyond NeMo are deferred: R26's
      pseudo-label members (the ivrit.ai Whisper fine-tune, the omnilingual model) and a CTC model to align Hebrew
      references for NeMo Forced Aligner (R51) — run them in the NeMo runtime where its libraries serve them;
      otherwise the ensemble starts with NeMo models only and references stay unaligned
      **Owner (2026-10-03):** Whisper loads per job in the NeMo runtime; the owner's OASIS service is called from a CPU
      pack `services`; the omniASR CTC aligner runs in a one-step runtime `omni` (fairseq2 needs torch 2.8; 00 decision log)

Write before starting:
- [x] **spec** → *R26 · confirm* The language-ID model and the pseudo-label ensemble members as registry references (the starter
      pipeline names `nemotron-3.5-base` and `omnilingual-3b`, neither is a registry entry) (C10) — registry kind
      `auxiliary`, licence table in the phase-4 plan (02 "Auxiliary models")
- [x] **spec** → *R27* Where annotation guidelines live: 04 calls them "a help article version", but help articles ship
      inside the binary (11) — project-authored guidelines need a versioned home — `annotation/guidelines/<name>.md`,
      the batch pins the commit (04 "Annotation workflow")

- [x] Mounts: local, NFS/SMB path, S3-compatible, HF Hub; health checks; adding a mount needs approval
- [x] Content-hash index (`mount://…` URIs), NVMe cache with pinning, LRU eviction, per-project quotas, `materialize`/`evict`
- [x] Sources in full: licence clearing for training, ingest history, "no licence, no ingest"; `utterances.search`
- [x] Step kinds: `sdp_ingest` (stereo split per party, bot track self-labelled, resample, VAD, segment),
      `pseudolabel_ensemble` + LID + agreement, `text_normalise`, `manifest_filter`, `speaker_disjoint_split`,
      `dataset_freeze` (fingerprint, quality checks, dataset card), `shar_export`
- [x] Registry in full: all kinds, adoptions with licence/locale checks, `data.lock`, soft-delete rules, step-kind deprecation; Library gains Adopt and the this-project / all filter
- [x] Noise bank mined from own calls (per-channel VAD) + licensed public sets
- [x] Interoperability: import NeMo manifests, Lhotse cuts/Shar, HF datasets, folder + CSV; export Shar/manifest/Hub
      (approval), Cadence bundle
- [x] Backups and exports can target a mount
- [x] Annotation workflow (needs call recordings, stereo split and VAD from this phase): sampling policy, reviewer
      invitations per batch (play, no download), Annotation batch, double annotation 10 %, adjudication,
      inter-annotator WER ≤ 5 %, freeze as the telephone golden set (built on synthetic calls:
      `golden-set/calls-synth-sr`); entity spans for names and addresses; end-of-utterance metric from per-channel VAD
- [x] For long audio (call recordings): waveform peaks at ingest and freeze, the spectrogram tile pyramid on demand,
      an estimated bandwidth per utterance so 8 kHz-origin audio is shown to 4 kHz (R51, R52); energy/VAD and channel
      tracks — bandwidth, energy/VAD and channel tracks from `tracks.get` (computed on request); peaks stored when a
      version is registered (`media.peaks`, multi-level) and the pyramid built on first view (`media.spectrogram`),
      both control-plane jobs (notes below)
- [x] A reference alignment step (NeMo Forced Aligner, per the **decide** above) so golden sets carry word timings for
      the reference track; emission delay PR50/PR90 joins the scorers (R54) — `align_reference@1` uses omniASR CTC
      emissions with torchaudio's aligner (NFA reads only NeMo checkpoints); `latency_score@3`
- [x] Dataset version statistics charts (R53); the numbers come from `datasets.get`, as the agent sees them
- [x] Playbooks: "Adapt a new language"; smoke project ("Try Cadence": 2 h FLEURS, full loop ≈ 1 GPU-hour —
      its export and parity steps complete once phase 5 lands)

Panels: Source, Dataset version (leakage result, shard locations), Recipe (full editing), Storage, Pipeline run,
Annotation batch, Triage (Annotate mode); Data workspace.

Phase 4 notes (what differs from the plan above and from `docs/review/2026-10-03-phase-4-plan.md`; streams M, D, X,
R, I, A, L and B, folded into the spec by stream S on 2026-10-04; the gate is still to run on the stand):
- Mounts: `mounts.list|get|new|scan|verify` (`verify`, not `health`, which is not a vocabulary verb); adding one is
  an approval for everyone (`mount-registration`); config is immutable; scans and health checks run in the control
  plane, per mount, not per host. The `corpora` mount is registered with root `/mnt/corpora` (the container path);
  compose binds `${CADENCE_CORPORA_DIR}` there read-only and `${CADENCE_EXPORTS_DIR}` at `/mnt/exports`. S3
  credentials are one secret `<accessKeyId>:<secretAccessKey>`.
- Cache: the content store is the cache. `storage.get`; `datasets.materialize|evict|export` are collection actions
  with the version in the body. Defaults `storage.cache_high_water_pct` 85, `cache_low_water_pct` 70,
  `project_quota_gb` 200; only blobs with a mount copy (`blob_copies`) are evictable, so shards that could be
  re-derived are not; the sweep runs as the system actor.
- The frozen cut is `cadence.dataset/1` (one canonical WAV per utterance + Lhotse MonoCut shards of 2 000), not Shar
  tars; Shar is `shar_export@1`. `datasets.freeze` reruns the draft step in cut mode and checks the project quota.
  Ingest VAD is an energy VAD per channel in the core; ITN in `text_normalise@1` is a literal list. "No licence, no
  ingest" is enforced at planning and in the draft hook.
- Auxiliary payloads carry `roles[]`, engine, conditions and `service.tokenSecret`; five seeds. LID is
  `lid_classify@2` in runtime `omni` (VoxLingua107; phase-4 tail below); OASIS writes lower case without
  punctuation and wins the pick when it agrees (`vote`), so pseudo-labels can lose case and punctuation (07). Whisper
  writes Serbian in Cyrillic: the pipeline transliterates `sr-Cyrl-Latn`.
- Registry: `versions.archive` (terminal, refused while in use); adoption checks licence and locale (unless
  `purpose: replay`); `data.lock` resolutions are reported (`PlanStep.locked`), not substituted; step-kind
  deprecation comes from the pack, with no `deprecate` verb.
- Imports: `dataset_import@4` copies into the content store and freezes at import (Lhotse cuts/Shar, NeMo
  offset/duration, the bundle, `mount://` on path mounts); only `sdp_ingest` indexes in place. The dataset bundle is
  per dataset version; the project bundle came in the phase-4 tail (below). Hub pushes need `hub-export`, private by
  default.
- Annotation: Triage is a tool panel (a queue). Annotators pull in their own hashed order; adjudication folds case and
  punctuation (not the project normalizer, `annotation.adjudicate_wer` 0); `foreign`/`unintelligible` items are
  excluded. Guidelines are pinned at HEAD; reviewers see the path and commit. Reviewers are invited by the admin
  (`invitations.new`, `auth.accept` redeems a `cri_` token). The golden set built is `golden-set/calls-synth-sr`
  (synthetic calls), not the telephone golden set, which waits for real calls.
- Alignment runs in runtime `omni` (≈ 11.6 GB image, torch 2.8, fairseq2 0.6) and by hand once per golden set
  (`pipelines/align-reference.yaml`); `latency_score@3` reports emission delay or `n/a` with a reason; `srp_Latn` is
  not in omniASR's list.
- Playbooks gain `person:`, `optional:`, `when:`; `weekly-flywheel` is available from phase 5; "Try Cadence" imports
  FLEURS from the Hub; `cadence smoke --project SLUG` (`make e2e` needs `SMOKE_PROJECT`). `defaults.yaml` is version 16.
- Corpora: `scripts/corpora/` (`fleurs.sh`, `calls_synth.py`); `fleurs-sr` and `calls-synth-sr` (40 calls, G.711 μ-law
  stereo) on the stand; `sdp_ingest` on them gives 577 segments (315 caller, 262 bot), turn coverage ≈ 80 %.
- Closed in the phase-4 tail (UI stream, 2026-10-04): reviewers read the guidelines text in the Annotate view
  (`guidelines.get`, the file at the batch's pinned commit; a reviewer's session reaches it for its own batch only);
  the Dataset version document renders its card (`texts.get`: only a blob a registry version names as text to read,
  256 KiB, sanitised Markdown via `@/shell/markdown`); an export UI there (Export card: plan, then export; Hub:
  approval; the version's exports with state and files); dry runs of `runs.new|stage` and `pipelines.run` warn
  `needs-materialize` (version, bytes and mounts to copy back) instead of refusing — the real call is still refused —
  and `pipelineRuns.get` lists `needsMaterialize`, shown with **Materialize** in Mix, Run and Pipeline run.
- Open: aligning the replay golden sets on the stand (now one `goldenSets.align` call); `web/e2e/training-panels.spec.ts` (not in `make ui-e2e`) is stale: its scripted worker publishes
  `dataset_import@1` while the bundled import pipeline pins `@4`.
- Closed in the phase 4 tail (2026-10-04, migration 0045; 06 "Media", 00 decision log):
  - Waveform peaks are stored when a dataset version is registered: a second `dataset` output hook queues the
    control-plane job `media.peaks` (one per version and artifact, so a draft's readable segments and then its frozen
    cut); peaks files are `cadence.peaks/2` with levels pooled ×16, and `peaks.get` reads only the span it asks for from
    the coarsest level dividing `hopMs` (`computed: true` marks the first-view fallback, which remains). The audio view
    reads long audio's overview at a stored level and 10 ms detail around the visible range.
  - The spectrogram tile pyramid is built once, on the first `spectrogram.get` (`202` with a `media.spectrogram` job;
    `422 media-tiles-failed` until `media.tiles_retry_s`; `media.tiles_max_s` 4 h), by the control plane with the
    STFT of `spectrogram_tiles@1` (byte-identical on a cross-check); narrowband audio keeps bins to 4 kHz (manifest
    `narrowband`, `bandwidthHz`). Not a worker step: windows of mount files have no artifact, utterances no project.
  - The audio view's reference-word track: `words.get?goldenSet=` (or `alignment=`) answers `reference` from the golden
    set's newest alignment; Eval report and Diff rows pass the cell's golden set (`&gs=` in selection items).
  - End-of-utterance gaps: `sdp_ingest` writes `eou` per segment of a split file (per-channel VAD, the annotation
    item's rule); `dataset_freeze` carries it into `manifest.jsonl` (draft and `cadence.dataset/1`) and `stats.eou`
    (`datasets.get`), and the card states the gap percentiles. Steps reused by input hash keep their old output
    (`fresh: true` re-ingests).
- Found and fixed running the gate on the stand (2026-10-04): every worker binds the mounts (core steps reached
  `worker-services`/`worker-omni`, which had no `/mnt/corpora`); the default preset allows `mounts.verify`; an
  optional step naming an auxiliary the project has not adopted is skipped with a warning; a retry leaves the plan's
  skips skipped; `projects.sync` adds annotation guidelines a project lacks (never over its own); Whisper LID and the
  Whisper member follow the retry's batch scale and LID batches 8 by default; corpora files must be world-readable
  (workers run as uid 65532). Telegram became quieter on the owner's request (silent outcomes and digest, one loud
  message per failed run, approvals batched for 120 s).
- Open, from the gate: a step that reads a mount is reused by its input hash even when the mount's files changed (the
  hash does not cover mount content; `fresh: true` works around it); every retry of an approved pipeline run asks
  for a new GPU-spend approval while the estimate is unknown; replay golden sets in `nb-NO` need `languages:
  {"nb-NO": "no-NO"}` (the base model's tag is `no`); `data-ingest` cannot draft untranscribed calls (frame the
  annotation batch on the ingest's segments artifact instead); the Try Cadence playbook asks to clear a source that
  is already cleared; a pseudo-label ensemble with the base model as a member keeps only what the weak member gets
  right (2 454 of 2 944 FLEURS segments disputed, kept labels no better than Whisper alone).
- Phase-4 tail (stream measure, 2026-10-04): `goldenSets.align` (new verb `align`) aligns several golden sets in one
  pipeline run, one optional aligning step per dataset artifact, skipping sets already aligned, with an unfinished
  aligning step, or in a language the adopted aligner lacks (of the 34 replay golden sets only those in he, sr, hr or bs are in
  omniASR CTC 1B's list), with a known estimate (`eval.align_step_overhead_s` 30 s + `eval.align_seconds_per_audio_hour`
  15 s; measured 6.9 s per audio hour on 2.12 h), so the batch is one `gpu-spend` decision. NeMo's
  `endpointing.residue_tokens_at_end` stays 2: 0 and 1 remove the 80 ms one-token finals only by switching end of
  utterance off (no segment closes before the stream ends; latency to final +385 ms mean at 80 ms; WER unchanged on
  700 FLEURS sr utterances); a sixth decoder shim (no end of utterance while the newest frame holds a token) removes
  the splits with endpointing intact and WER unchanged — measured, recommended, not built (07 "Open questions").
- Closed by the owner's decisions of 2026-10-04 (00 decision log): `pipelines/calls-ingest.yaml` stops at the
  segments (the frame of `batches.new`); a playbook's clearance step ticks from the `sources.get` that found the
  source already cleared; the pseudo-label template votes Whisper + OASIS (`pseudolabel_ensemble@2`, OASIS required,
  Whisper's written-form text kept when the two agree).
- Phase-4 tail, stream infra (2026-10-04; help `guides.project-bundles`):
  - Project bundles: `projects.export` writes a project to a writable path mount from a control-plane job (export
    format `cadence-project-bundle`, migration 0047): `repository.bundle`, `data.lock`, one dataset bundle per dataset
    version and noise bank, other payload blobs under `cas/`, `bundle.json` last. `bundles.adopt` (an existing
    project) and `projects.new` with `bundle` (a new one from the bundle's history) import it behind `bundle-import`,
    an approval for everyone. Versions the instance lacks register under the bundle's version strings; golden sets
    are re-frozen through this instance's leakage checks; aliases are set where the project has none. Not carried:
    alignments (re-align with `pipelines/align-reference.yaml`), mount URIs of indexed utterances, work (mixes, runs,
    evals), a custom `AGENTS.md` of the bundle (re-rendered).
  - LID is `lid_classify@2` in runtime `omni` (VoxLingua107, `auxiliary/lid-voxlingua107`, speechbrain 1.1.1 over
    torch 2.8, image size unchanged). The NeMo pack's Whisper `lid_classify@1` is no longer published; Whisper's
    detection stays the ensemble's second opinion through the member. Templates pin `@2`, so `pseudo-label` needs the
    VoxLingua107 auxiliary adopted (an approval). On the FLEURS Hebrew fixture it names Slovenian for 2 of 10
    three-second clips (confidence 0.72 and 0.81, above `pseudolabel.lid_min_confidence`); peak 549 MiB.
  - `scripts/nightly-gpu.sh` also builds `worker/Dockerfile.omni` from `main` and runs the omni pack's GPU tests
    (alignment, LID) when 8 GB are free, in the same Telegram summary (`NIGHTLY_SKIP_OMNI=1` turns it off).
  - Annotation e2e: `web/e2e/annotation.spec.ts` with the stack's `seed-annotation` fixture
    (`internal/e2etools/seedannotation`): a batch from the API, two items annotated in Triage Annotate mode, a reviewer
    invitation that sees only its batch, blind double annotation, adjudication, the freeze blockers and the freeze
    approval. Found: the floating Audio panel covers the Annotation batch document's lower sections; Annotate shows a
    done item instead of "Nothing left to annotate" once a person's queue is empty (both closed below).
  - `recipes/projects/hebrew/annotation/guidelines/default.md`: the template adapted to he-IL (no niqqud, plene
    spelling, prefixes attached, numbers in the pack's written forms).
- Closed in the phase-4 tail (polish, 2026-10-04; 00 decision log):
  - A default workspace builds nothing floating: its Floating column is the slot the Audio panel floats in when
    something opens it, so no empty Audio covers the Annotation batch, Dataset version or Eval report documents;
    workspace schema 3 drops the Audio float from stored layouts (11 "Default workspaces", 10 "Persistence").
  - Annotate shows "Nothing left to annotate in this batch." once a person's queue is empty (the server's `next`, never
    the first item by default); the annotation e2e checks both.
  - The Dataset version document charts `stats.eou` (End-of-utterance gap: histogram with p50/p90 marks, table view and
    CSV copy; only when the version has gaps measured).
  - Annotation and triage windows get their peaks before the first view: `batches.new` queues `media.peaks` for the
    batch's item windows, and a hook after the triage hook queues it for the windows of the items a segments artifact
    indexed; the first-view computation stays the fallback (06 "Media").
  - Tile pyramids the control plane built are kept by last view: `media.tiles_retention_days` (14; `defaults.yaml`
    version 18), the daily `mediaTiles.retention` queues `artifacts.evict` as the system actor (permanent, no
    approval), the next view builds them again (02 "Content store", help `guides.freeing-store-space`).
  - Fixed on the way: the Dataset version's export formats left out `cadence-project-bundle` (a `tsc` error after the
    infra merge; projects.export's alone).

---

## Phase 5 · Deploy and flywheel

**Goal.** A gated checkpoint becomes a verified model version and climbs shadow → canary → production through
human-run delivery scripts; production audio where the model is likely wrong comes back as the next dataset version.

**Gate.** A registered model version passes parity and latency, reaches the shadow volume threshold (≥ 20 h replayed
calls), and a canary promotion is approved in the UI (confirm modal) with its signed Promotion record.

Decide before starting:
- [ ] **decide** → *R28 · confirm* Retention vs immutability: what happens to frozen versions containing expired production audio (B6)
- [ ] **decide** → *R28, R29 · confirm* Confirm the proposed defaults: 90-day retention, PII masking rules (00 decision log)

Write before starting:
- [ ] **spec** → *R30* Staging serving: how Triton runs on the staging card for shadow replay and benchmarks, beside training
- [ ] **spec** → *R29* PII redaction step: NER model for Hebrew, span alignment to cut audio, what "redacted" guarantees
- [ ] **spec** → *R32 · confirm* The Эра interfaces: samples push payload, operator corrections, dialogue context for the judge,
      the per-call boost-list field and its cap
- [ ] **spec** → *R33* What makes a Promotion record "signed"
- [ ] **spec** → *R31 · confirm* Parity tolerance and the latency budget per chunk size — the gate uses both, the defaults table has
      neither (04 says only "beyond tolerance"); A3 records the first numbers

Deployment
- [ ] `models.export` (ONNX cache-aware
      encoder/decoder/joint; GGUF where a CPU target exists); `models.parity`; Triton repository with sequence
      batching; `models.benchmark` (p50/p95, RTF, streams per card)
- [ ] Deployments and Promotions: shadow (nightly replay from the call-recording mount), canary and production as
      approvals; delivery script generated for the production host; rollback by script; boost lists as decode config
- [ ] Hot words at decode (RNNT phrase boosting), dynamic per-call candidates per the Эра **spec**
- [ ] Deployment targets declare the families and formats they serve; promotion checks them (R46)
- [ ] Transcriptions against the staging Triton deployment: production and candidate side by side, live and from
      files (R47); benchmark and shadow charts; PII spans and boosted terms as audio-view tracks (R51, R53)

Flywheel
- [ ] Production samples: sampling policy (10 % + low confidence), PII redaction, retention, monthly registry Source
- [ ] Signals: model disagreement, low confidence, LLM judge on redacted text only (spend budget), operator corrections
- [ ] Triage (admin, agent, invited reviewer), 10 % of agent decisions to human review, worst utterances of failing
      eval cells as candidates, correction batches → Source
- [ ] Schedules and scheduled sessions (no person present, approvals to Telegram, never touch production);
      "Weekly flywheel", "Improve on telephony", "Fix names and terms" playbooks
- [ ] Samples push API `POST /projects/{p}/samples` before the first canary

Panels: Model, Shadow, Triage queue (triage mode); Ops workspace.

---

## Deferred

Not planned into any phase; each comes back only by the owner's decision.

- By the owner, 2026-09-29 — the phase-2 seams keep each of these to one framework pack:
  - training from scratch, tokenizers, multi-card gang leases, adapter runs, multi-node training (R44);
  - packs beyond NeMo — sherpa-onnx, Hugging Face transformers, k2/icefall — with `runtimes.new` and spike F1 (R45);
  - OpenAI-API inference servers (NVIDIA Speech NIM, vLLM) as eval baselines (R46).
- Beyond phase 5 (spec "Could" or v2): data governance under immutability beyond B6 (tombstones, access audit,
  data-subject requests), passkeys, pgvector search and dataset embedding maps, snapping to docked groups, email
  notifications, team roles.

---

## Not placed yet

- [x] **spec** → *R34* The `cadence` CLI surface (07 lists it as unspecified). Principle 1 says it is generated from the
      contract; `make e2e` (`cadence smoke`) and `cadence admin reset-password` depend on it. Proposed: generated
      commands arrive with phase 1, `smoke` with phase 4. Generated commands built in phase 1 (R34 in 08); `smoke`
      stays with phase 4.
- [x] Recipe document edits every text file of the project repository, not only augmentation profiles — today
      `recipes.edit` is reachable in the web only from `AugmentationForm`, so a person cannot change a pipeline
      (`pipelines/*.yaml`) without git or an agent session, although the guardrails preset names `recipes.edit` "the
      Recipe document's (a person's) path". Needs: a text editor in the Recipe panel (YAML for pipelines, with
      `pipelines.run?dryRun` validation before save), If-Match on `history[0].sha`, help page. Found on the test stand
      2026-10-01 (Serbian fine-tune needed `target_lang` on both train-stage steps). Built 2026-10-01: Edit in the Recipe
      document (plain text; Check = dry run), and `recipes.edit|new` plan a `pipelines/*.yaml` before committing it.
- [ ] Release process: semver, the pinned matrix (NeMo container, Dockview, agent adapters) in release notes — first
      needed when there is something to upgrade from, i.e. before the first real project after phase 2.

## Notes on the mapping

- The coverage audit in `docs/spec/07-audit-risks-sources.md` tags blocks "phase 1–4" on an older numbering; this file
  places each block where it is first used (e.g. language packs, tagged phase 2, land with evaluation in phase 3
  because scoring needs the normalizer). Update the audit table in the Doc when the order is confirmed.
- The Doc's Build order text puts Help in phase 0, its figure in phase 1; this file follows the text (Help panel in
  phase 0, context help and "Explain this" sessions in phase 1).
- Phase order puts Training before Data on purpose ("the order a first fine-tune needs them"): phase 2 trains on an
  imported dataset version; phase 4 replaces the import with Cadence's own pipeline.
- Extensibility, manual tests and audio views (R40–R54, 2026-09-29) follow the same rule: the seams land in phase 2
  where step kinds are born, because retrofitting them means migrating every stored checkpoint and eval record; the
  fields they need appear in phase 1 already (a family on base models, `init` and `gpus` on the run request); manual
  transcription tests and the audio view in phase 3 where audio serving arrives; long-audio tracks and alignment in
  phase 4 with call recordings; Triton targets in phase 5; everything beyond the seams is deferred by the owner.

## Dependency audit (2026-09-29)

Every item was checked for what it relies on. Changes made to the first draft:

| Found | Fix in this file |
| --- | --- |
| Commands need an actor and workspaces need `me` in phase 0; login was phase 1 | Development actor in phase 0 |
| Mix (phase 1 gate) references dataset versions; registry was phase 4 | Registry core in phase 1, fixture dataset versions |
| A2 (phase 1) needs a training dry-run estimate; runs were phase 2 | Estimate-only `runs.new?dryRun` in phase 1 |
| Settings (phase 1) edits compute; compute entity was phase 2 | Compute moved to phase 1 |
| Wizard (phase 1) reads `defaults.yaml`; its decision was phase 2 | Decision moved to phase 1 |
| Approvals (phase 1) had no gated command and no in-app notification history | Fixture gated command; notification history in the phase-0 chrome |
| Phase-2 gate uses a playbook session; playbooks appeared nowhere before phase 4 | Playbook engine in phase 2 |
| Annotation and the telephone golden set (phase 3) need call recordings, stereo split and VAD (phase 4) | Annotation moved to phase 4; phase-3 gate on imported golden sets |
| Entity accuracy, end-of-utterance and RTF metrics (phase 3) need spans, VAD and the benchmark step | Split across phases 3, 4, 5 |
| Eval records keyed by model version, but registration is phase 5 | New **decide** in phase 3 |
| Bootstrap (phase 1) copies language packs (phase 3) | Packs join via `projects.sync` in phase 3 |
| Backups to a mount (phase 2) before mounts (phase 4) | Local path first, mount in phase 4 |
| Blocks the spec relies on but never describes | **spec** lines: permission presets and policy engine, secret storage, internal git repository, worker protocol, artifact store, metric/log storage, estimate model, playbook format, audio serving, LID/pseudo-label models, staging serving, PII redaction, Эра interfaces, signed promotions, CLI |
| Second pass (independent review): phase-2 gate had no runnable playbook; phase-1 estimate lacked defaults and the estimate model; no `mixes.edit` tool; boosting needed at decode before phase 5; replay data unspecified; imports lacked Source/Utterance/Transcript; parity and latency thresholds undefined; Normalizer and guidelines homes contradictory; Lineage endpoint after its panel | Phase-2 playbook **spec** + replay **spec**; defaults and estimate model moved to phase 1; `mixes.edit` **spec**; static boosting in phase 3; minimal data entities in phase 2; new **spec** lines in phases 3–5; lineage endpoint in phase 3 |
| Spec items missing from the first draft | Added: saved searches, context bridge, archive, policies, pipelines/augment/langpack tools, weekly perf job, CI, selection bus, entity manifest conventions, worktree watcher, first-start admin creation, help context and CI check, OTel propagation, GPU telemetry, staging host setup, retention jobs, SPA embedding, release process |

Second pass (2026-09-29): R40–R54, the owner's answers and the staging stand, checked the same way:

| Found | Fix in this file |
| --- | --- |
| Base models are created in phase 1, but the family descriptor comes with the worker in phase 2 | Phase-1 base models store their family id |
| The phase-1 estimate-only `runs.new` would fix a request shape without `init` and `gpus` | Both fields in the phase-1 request |
| Seams without an exit criterion get cut under schedule pressure | Toy-pack conformance joins the phase-2 gate |
| `runtimes.new` matters only for a second runtime, and packs are deferred | The worker registers the NeMo runtime; `runtimes.new` is deferred |
| Transcriptions (phase 3) need the `media` tag and an `interactive` job kind, and neither was placed | Both are phase-3 items |
| R54's latency to final needs utterance ends, and emission delay needs aligned word ends (phase 4) | Phase 3 takes ends from a NeMo frame-VAD model; emission delay moves to phase 4 |
| Call recordings (phase 4) need server peaks and tiles, and the 8 kHz view cap needs a bandwidth estimate | Both arrive with ingest in phase 4 |
| Deferred packs leave R26's non-NeMo members and a Hebrew CTC aligner without a runtime | One phase-4 **decide** line |
| TLS moved to the host's Caddy on the staging stand | Phase-1 exposure item rewritten; basic auth goes once sign-in works |
| S5 depends only on the phase-0 shell | May run any time before phase 3 |
