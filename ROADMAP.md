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
- [ ] Exposure (B7, R39) on the staging stand: TLS at the host's Caddy, control plane on 127.0.0.1, Postgres unpublished
      (in place since phase 0); once sign-in works, the owner drops basic auth from the Caddy site file (a root-owned
      file); a Caddy compose profile stays the option for hosts without a proxy of their own

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
  session, `aliases.set baseline` → approval. Live runs are still by hand: no model accounts on CI runners.
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
  login claude|opencode` stays as the CLI fallback. Still the owner's: dropping basic auth from the stand's Caddy site
  file (Exposure, above) — turn on two-factor sign-in first.

---

## Phase 2 · Training

**Goal.** A run is one reproducible optimisation stage — pinned base or checkpoint, frozen mix, committed recipe,
step budget — scheduled on a card under its memory cap, watched live and continued by resume or a new stage.

**Gate.** The agent (a session running the phase-2 playbook below) calibrates and runs a Nemotron 3.5 fine-tune on the staging card under the
24 GB cap from a dataset version, with a dry-run estimate shown first; metrics stream to the Run panel; checkpoints
are registered with validation WER. A3 acceptance met (incl. ONNX parity numbers recorded for phase 5). The seams
hold: the CPU toy pack passes the conformance suite in CI, and no control-plane or web code names the Nemotron family
(R40–R45).

Decide before starting:
- [ ] **decide** → *R18* How phase 2 gets training data before the Data phase — proposed: an import step that registers a
      pre-built Shar / NeMo manifest (FLEURS subset, the A3 Hebrew subset) as a frozen, fingerprinted dataset version
      through `pipelines.run` (`pipelines/import:run`), registering a Source with the licence the format carries (03)

Write before starting:
- [ ] **spec** → *R14* Worker ↔ control-plane protocol: how a Python worker on another host leases River jobs it cannot
      consume directly, reports progress, heartbeats, streams logs and metrics, and receives secrets; the worker's
      credential kind (missing from the credentials table)
- [ ] **spec** → *R15* Artifact and content store: how step outputs (manifests, Shar shards, checkpoints, eval reports) are
      written, addressed by hash, read by the next step and the UI; where it lives before mounts exist
- [ ] **spec** → *R15* Metric and log storage: schema and retention for `run.{id}.metrics` points and `job.{id}.log` lines
- [ ] **spec** → *R16* Playbook definition format: chain of pipelines, prefilled prompt, estimate, plan ticks; plus a
      "Train from an imported dataset" playbook (mix → calibrate → train → register checkpoints) — none of the four v1
      playbooks can run before phase 4, yet the phase-2 gate is a playbook session
- [ ] **spec** → *R17* Replay data: which corpus supplies the 15 % replay share across the base model's other 39 locales
      (e.g. FLEURS per locale), its sources and licences, and the replay-locale golden sets the phase-3 gate checks
- [ ] **spec** → *R19* Compute availability windows on the shared staging card (07 "Also unspecified")
- [ ] **spec** → *R40–R45* Extensibility seams, before step kinds and the worker harden: a runtime on every step kind
      and in the lease; the Nemotron model-family descriptor (roles, latency profiles, capabilities, defaults section);
      neutral artifact types (`hypotheses`, `analysis`, `deployable`); `init` and `gpus` in the run and resource
      schemas; the conformance suite. Everything beyond the seams is deferred (owner, 2026-09-29)

Environment
- [ ] Staging host ready: GPU card with the 24 GB cap, NeMo Speech 26.07 container pinned by digest, the base model
      at its pinned revision, other resident services left intact (A3 setup)
- [ ] GPU telemetry for the `gpu` topic and the status bar (card memory and compute per card)

Worker and jobs
- [ ] Worker per the protocol above: publishes the step registry at start, runs steps as subprocesses, heartbeat,
      reaping after 3 missed beats; secrets injected into jobs only
- [ ] River queue: one training slot per card, per-project priorities, pause/resume/cancel, `jobs.wait`
- [ ] Artifact store per the **spec**; OpenTelemetry trace carried into jobs
- [ ] Pipeline engine: YAML pipelines pinning `kind@version`, artifact types validated at `dryRun`, per-step status,
      retry one step, input-hash idempotence
- [ ] `defaults.yaml` + `x-cadence` on every step parameter; "departures from defaults" recorded on entities
- [ ] OOM → typed error → one retry at 0.75× batch
- [ ] Playbook engine and playbook sessions (plan from the chain, `dryRun` before each spending step, estimate first);
      the fifth v1 playbook "Fine-tune from a dataset version" (R16) — the gate runs it
- [ ] Minimal data entities for imports: Source (licence, eval-only until cleared, archive), Utterance, Transcript,
      per-utterance fingerprints — the full ingest path arrives in phase 4
- [ ] Replay corpus and replay golden sets imported per the **spec** above
- [ ] Runtimes (R40): the worker announces `runtime@version` and the NeMo runtime is registered from it;
      `runtimes.list|get`; card slots are owned per host and card, so two runtimes could share a card; `runtimes.new`
      (a second runtime, with approval) waits with the deferred packs
- [ ] Model families (R41, R43): published by the runtime; Nemotron 3.5 streaming first, with latency profiles
      `80ms`–`1120ms`; base models and checkpoints carry their family; no code branches on a family name
- [ ] CPU `toy` framework pack (a tiny CTC model) and the conformance suite in CI; the NeMo pack runs it nightly (R45)

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

---

## Phase 3 · Evaluation

**Goal.** A checkpoint is judged only in true streaming at deployment latency, on frozen golden sets it never
trained on, against the baseline, with confidence intervals, and a gate turns the matrix into a verdict.

**Gate.** An eval matrix of a phase-2 checkpoint on the imported golden sets (FLEURS he plus the replay-locale sets
from phase 2) runs on staging and `evals.gate` returns a verdict (target-locale WER vs baseline with bootstrap CI,
replay-locale regression ≤ 0.5, deletions/insertions check). The telephone golden set from own calls needs phase 4.

Decide before starting:
- [ ] **decide** → *R20* The latency set and primary cell (`[56,1]` vs `[56,0]`, offline vs `[56,13]`) (C2), named as
      latency profiles (R43)
- [ ] **decide** → *R22* What evaluations are keyed by before a model version exists: registry Eval records are keyed by
      model version, but phase 3 evaluates checkpoints and registration is phase 5 — key by checkpoint content hash,
      or move `models.register` into phase 3
- [ ] **decide** → *R23* Baseline before anything is in production: the base model at its pinned revision

Write before starting:
- [ ] **spec** → *R21* Normalizer identity: 02 makes it a registry entity (Eval records keyed by normalizer version), 03 makes
      it a file in a project language pack with "no registry version" — a cross-project Eval record cannot pin one
      project's file SHA; pick one
- [ ] **spec** → *R25* Audio serving: an endpoint that streams an utterance span (range requests) for the Audio panel, with
      a play-only mode for reviewers; missing from the 13-item backend contract
- [ ] **spec** → *R47–R50* Manual transcription tests and the live channel: message schemas in the contract, the `media` tag in
      the generator, interactive jobs beside training, capture defaults; *R51–R54* the audio view, spectrogram defaults,
      charts, streaming metric definitions

Spikes before the items they gate: A5 (live transcription) before live mode — it needs a phase-2 checkpoint and the
worker protocol; S5 (audio view) before the Audio panel — it needs only the phase-0 shell and can run any time before.

- [ ] Golden sets as registry assets from imports (FLEURS he), freeze with approval, fingerprint exclusion, runs
      cannot reference them; adoption re-runs the leakage check against fingerprints of imported versions
- [ ] Language packs `lang/<locale>/` (normalizer, ITN, translit, LID, boost lists, golden recipe); he-IL starter pack;
      added to existing projects through `projects.sync`; entities pin the pack SHA; a new normalizer version forces
      a new baseline; the search index starts using per-locale normalisation
- [ ] `streaming_eval` (cache-aware, per latency profile — R43), WER/CER/S/D/I, per-utterance rows, duration buckets,
      punctuation-insensitive companion score
- [ ] Scorer step kinds available now (R54 definitions): entity accuracy for number classes (names and addresses need
      annotated spans, phase 4); latency to final at p50/p95 with audio at real-time pace, utterance ends from a NeMo
      frame-VAD model until per-channel VAD lands in phase 4; partial stability as the unstable partial word ratio.
      Emission delay PR50/PR90 needs aligned references and end-of-utterance needs per-channel VAD (both phase 4); RTF
      and streams per card come from the benchmark step (phase 5)
- [ ] Eval records cache (per the **decide** above); only missing cells computed
- [ ] Baselines (approval), gates per project (`gates.edit`), 1 000-sample bootstrap CI on every delta, resampling whole
      calls or speakers (R54)
- [ ] Decoding config as an eval axis (R24): static RNNT context biasing in the eval decoder; `langpacks.get|edit`,
      `boost.edit`; boosted vs unboosted cells in `evals.new` (entity recall + general WER); the Language pack
      "test a phrase" box as a one-utterance eval
- [ ] `models.register` (R22): publish a checkpoint whose gate passed, with its eval report and model card; export
      stays in phase 5
- [ ] Robustness matrix: augmentation profile as another `evals.new` axis (golden set × profile × latency)
- [ ] Experiments and sweeps: grid/random over recipe params, GPU-hour cap, Compare N, register best
- [ ] Lineage both ways: `GET /registry/{kind}/{id}/lineage`, "used by" (before the Lineage panel)
- [ ] Audio view (R51, R52) as a shell primitive: waveform, spectrogram (FFT in a Web Worker, WebGL2), model input and
      emissions from the transcribe step, hypothesis words with confidence, reference/hypothesis alignment (S/D/I),
      streaming timeline; spans as selections and chat references (`#t=`); TextGrid, CTM and WebVTT export
- [ ] Transcription tool (R47–R50): a file, the microphone or an utterance span through one WebSocket session; up to
      three targets, blind compare; telephony simulation; a typed reference gives WER on the page; nothing is stored;
      "test a phrase" becomes a two-target transcription
- [ ] Eval charts (R53): matrix heatmap, forest plot of deltas with intervals, S/D/I, buckets, latency CDFs, WER
      against latency; the utterance table opens rows in Diff and Audio
- [ ] Generator: the `media` tag — exempt from the verb rule and from MCP — for R25's audio endpoint, `…/peaks`,
      `transcriptions.new` and its socket (R48)
- [ ] Queue: job kind `interactive` beside training under the card's cap, never beside a benchmark, 1 GPU-hour per
      project per day (R49)

Panels: Eval report, Diff, Audio, Golden set, Lineage (over what the registry holds so far), Language pack,
Experiment, Transcription; Eval workspace.

---

## Phase 4 · Data

**Goal.** Raw corpora on mounts become frozen, fingerprinted, licence-checked dataset versions in the base model's
text style, indexed in place and materialised only when a job needs them; the registry pattern is complete; the
telephone golden set is built from own calls.

**Gate.** A dataset version ingested from a mount and frozen by the `data-ingest` pipeline (leakage check passed,
dataset card generated) is trained on through the phase-2 path — ideally as the "Adapt a new language" playbook.

Decide before starting:
- [ ] **decide** → *R45* The models phase 4 needs that are not NeMo models, while packs beyond NeMo are deferred: R26's
      pseudo-label members (the ivrit.ai Whisper fine-tune, the omnilingual model) and a CTC model to align Hebrew
      references for NeMo Forced Aligner (R51) — run them in the NeMo runtime where its libraries serve them;
      otherwise the ensemble starts with NeMo models only and references stay unaligned

Write before starting:
- [ ] **spec** → *R26 · confirm* The language-ID model and the pseudo-label ensemble members as registry references (the starter
      pipeline names `nemotron-3.5-base` and `omnilingual-3b`, neither is a registry entry) (C10)
- [ ] **spec** → *R27* Where annotation guidelines live: 04 calls them "a help article version", but help articles ship
      inside the binary (11) — project-authored guidelines need a versioned home

- [ ] Mounts: local, NFS/SMB path, S3-compatible, HF Hub; health checks; adding a mount needs approval
- [ ] Content-hash index (`mount://…` URIs), NVMe cache with pinning, LRU eviction, per-project quotas, `materialize`/`evict`
- [ ] Sources in full: licence clearing for training, ingest history, "no licence, no ingest"; `utterances.search`
- [ ] Step kinds: `sdp_ingest` (stereo split per party, bot track self-labelled, resample, VAD, segment),
      `pseudolabel_ensemble` + LID + agreement, `text_normalise`, `manifest_filter`, `speaker_disjoint_split`,
      `dataset_freeze` (fingerprint, quality checks, dataset card), `shar_export`
- [ ] Registry in full: all kinds, adoptions with licence/locale checks, `data.lock`, soft-delete rules, step-kind deprecation; Library gains Adopt and the this-project / all filter
- [ ] Noise bank mined from own calls (per-channel VAD) + licensed public sets
- [ ] Interoperability: import NeMo manifests, Lhotse cuts/Shar, HF datasets, folder + CSV; export Shar/manifest/Hub
      (approval), Cadence bundle
- [ ] Backups and exports can target a mount
- [ ] Annotation workflow (needs call recordings, stereo split and VAD from this phase): sampling policy, reviewer
      invitations per batch (play, no download), Annotation batch, double annotation 10 %, adjudication,
      inter-annotator WER ≤ 5 %, freeze as the telephone golden set; entity spans for names and addresses;
      end-of-utterance metric from per-channel VAD
- [ ] For long audio (call recordings): waveform peaks at ingest and freeze, the spectrogram tile pyramid on demand,
      an estimated bandwidth per utterance so 8 kHz-origin audio is shown to 4 kHz (R51, R52); energy/VAD and channel
      tracks
- [ ] A reference alignment step (NeMo Forced Aligner, per the **decide** above) so golden sets carry word timings for
      the reference track; emission delay PR50/PR90 joins the scorers (R54)
- [ ] Dataset version statistics charts (R53); the numbers come from `datasets.get`, as the agent sees them
- [ ] Playbooks: "Adapt a new language"; smoke project ("Try Cadence": 2 h FLEURS, full loop ≈ 1 GPU-hour —
      its export and parity steps complete once phase 5 lands)

Panels: Source, Dataset version (leakage result, shard locations), Recipe (full editing), Storage, Pipeline run,
Annotation batch, Triage (Annotate mode); Data workspace.

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
