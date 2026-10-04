# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Cadence — guide for coding agents

Cadence is a self-hosted workbench for the full life of Nemotron ASR fine-tunes (data, training, evaluation,
deployment, production flywheel), agent-native and API-first. You are implementing Cadence itself, not running it.

## Read first

- The spec lives in `docs/spec/` — start with `docs/spec/README.md`, then the file for the area you touch.
- Do not invent product decisions. If the spec is silent, ask; if you must proceed, record the assumption in
  `docs/spec/07-audit-risks-sources.md` under "Open questions" and mention it in the PR.
- Architecture changes need a row in the decision log in `docs/spec/00-overview.md` (date, decision, why).

## Repo map

```
api/            OpenAPI 3.1 contract — source of truth for the Go server, the TS client and the MCP tools
control-plane/  Go: REST API, MCP server, outbox → SSE, River jobs, Postgres, content store, templates/
web/            Vite + React SPA: Dockview shell, shadcn on Base UI, Radix Slate + Indigo, Iconoir
worker/         Python step-kind registry; runs inside the NeMo Speech NGC container (GPU)
agent-host/     TypeScript ACP client hosting Claude Code (claude-agent-acp) and opencode (opencode acp)
recipes/        Example project repository after bootstrap (projects/hebrew: pipelines/, project.yaml, data.lock)
docs/spec/      The specification (read before changing behaviour); docs/spikes/ the eight gated experiments
.claude/rules/  Path-scoped rules that load when you touch the matching directory
.claude/skills/ Dev skills for building Cadence (cadence-spikes). Product skills (cadence-data, -train, …) are in
                control-plane/templates/skills/ and copied into project repos at bootstrap
```

## Current state (phases 0–3 done; phase 4 built, gate pending)

Work follows `ROADMAP.md`: six phases (0 Shell → 1 Agent loop → 2 Training → 3 Evaluation → 4 Data → 5 Deploy and
flywheel), each closed by a gate. Pick work from the current phase; tick items there as they merge; don't start an
item whose **decide** line is still open. Proposed answers to every **decide**/**spec** line are in
`docs/spec/08-resolutions.md` (R1–R39), ported to the Doc; they win over older spec text until it is rewritten.
R40–R54 in the same file (framework seams, manual transcription tests, the audio view and charts) were added the same
day: build the seams in phase 2; training from scratch and packs beyond NeMo are deferred; transcriptions store nothing.

Phase 0 (shell) passed its gate on 2026-09-29: spikes S1–S4 are done with numbers in `docs/spikes/`. What exists:
- **Contract toolchain.** `api/openapi.yaml` follows R1; `api/vocabulary.yaml` is the machine copy of the verb table
  (a test keeps it equal to `docs/spec/10-ui-shell.md`). `control-plane/cmd/mcpgen` refuses naming violations and
  writes `internal/mcp/tools.json`, `internal/api/planned.gen.go` (501 for `x-cadence.planned` operations),
  `internal/cli/operations.gen.go` (the `cadence <entity> <verb>` CLI, R34) and `web/src/api/operations.gen.ts`.
  The API lives under `/api`.
- **Control plane.** pgx + embedded migrations, fixed dev actor (`usr_admin`) until phase 1 auth, command pipeline
  (idempotency, If-Match/412/428, dryRun by rollback), outbox → SSE, projects, per-user workspaces, help bundle, slog
  / Prometheus / OTel file traces, SPA embedded (`make web`).
- **Web shell.** Dockview host + adapter, panel and entity registries, selection bus, command registry + palette,
  workspaces with migrations, SSE client, theme, Library / Inspector / Help / Project panels. Panels use only
  `@/shell/panel`, the entity primitives and `@/components/ui`; ESLint enforces it.
- **Worker.** Step-kind contract with `x-cadence` and input hash.

Phase 1 (agent loop) passed its gate on 2026-09-30: A1, A2 and A4 are done with numbers in `docs/spikes/`; Claude Code
and opencode sessions edit a mix through MCP and the open Mix panel shows the draft with the session badge in
p95 56–68 ms. What exists now (details in `ROADMAP.md` "Phase 1 notes" and `docs/spec/05-agents.md` "Phase 1 as built"):
- **Identity.** First-start admin, Argon2id + TOTP, session cookie + `Cadence-Client: web` CSRF header, credentials
  (`cdk_` API keys, `cst_` agent session tokens, `cah_` agent-host token, `cep_` egress-proxy token),
  `cadence admin reset-password|host-token`.
- **Registry and policy.** Collections/versions (`YYYY-MM-DD.<sha>`), adoption, aliases (`baseline` gated,
  `production` reserved), compute, secrets (secretbox files, master key), `defaults.yaml` + `defaults://`, policies,
  presets + policy engine (`internal/policy`), approvals, audit, River jobs, `runs.new?dryRun=true` estimate (table).
- **Projects.** Wizard + `projects:bootstrap` (internal bare repo served over smart HTTP, or GitHub), agent profile,
  notes, sync, archive, mixes with revisions and drafts/presence, search index, saved searches.
- **MCP.** Streamable HTTP on `/mcp`, tools generated from the contract, data-marked results, four resources.
- **Agent host.** One ACP client, drivers for claude-agent-acp and `opencode acp`, session manager (worktree on
  `session/<id>`, commit + push per turn, budgets, runaway and stuck-turn clocks, permissions → preset → approvals),
  per-session Unix users when root. Agent model accounts are connected in Settings → Agents (`agentCredentials.*`,
  admin only, never MCP tools): values pass through the secret store's transit area to the host
  (`hostCredentials.claim|report`), which writes them into the agent-credentials volume and verifies them as a
  sandboxed user; `login claude|opencode` stays as the CLI fallback. The egress proxy adds the configured providers'
  hosts to its static list by polling `egressHosts.list` (internal/agentcreds, internal/egress).
- **Web.** Chat, Agent sessions, Approvals, Agent settings, Settings, Getting started, Project, Mix, Recipe panels;
  context bridge (Ctrl/Cmd+I, `@kind:id` chips, attribution badge → tool call in Chat).
Phase 1 has nothing open (live evals on the stand: 6/6, 2026-09-30). Built after the gate: the worktree watcher (`recipe.working` events), the three-way Session changes
(`branches.compare`), `hostSessions.release` on host shutdown, tool-call ids for opencode, the agent evals harness
(`make evals`); claude.ai connectors are off in every Claude session.

Phase 2 (training) passed its gate on 2026-10-01 (an agent's playbook session fine-tuned Nemotron on the stand; ROADMAP
"Phase 2" gate paragraph); the replay corpus and the 34 replay golden sets are imported on the stand. Plan and interfaces: `docs/review/2026-09-30-phase-2-plan.md`; what differs: ROADMAP "Phase 2 notes"; the
real-card rehearsal: `docs/review/2026-10-01-phase-2-rehearsal.md`. What exists:
- **Worker protocol** (tag `worker`, `cwk_`): workers register runtime, step kinds and model families as registry
  versions, long-poll leases on River `step` jobs (`internal/workers`, `internal/queue`: card slots, priorities,
  availability windows), heartbeat, stream NDJSON logs and metrics, publish outputs mid-lease and release.
- **Content store and pipelines**: `internal/cas` (BLAKE3 `b3:` hashes, directory manifests), `internal/artifacts`,
  `internal/pipelines` (YAML pinning `kind@version`, dryRun validation, input-hash reuse, OOM retry at 0.75×, output
  hooks).
- **Domains**: data (sources, utterances, transcripts, `dataset` hook, `noise_bank`), runs (facades over `train-stage`,
  calibration → measured estimates with lease overhead, checkpoints top-k, `metrics.get`, GPU budgets → approval),
  playbooks (`templates/playbooks`, server-ticked plans, dryRun before spending), notifications (Telegram) and backups.
- **Worker packs** (`worker/`): the harness, core kinds (`echo`, `dataset_import`), the CPU `toy` pack and the NeMo pack
  (`worker/packs/nemo`, family `nemo.fastconformer-rnnt.cache-aware`); `make conformance` runs the suite.
- **Web**: `@/shell/charts` (uPlot, ECharts), Queue & GPU, Logs, Pipeline run, Run, Metrics, Checkpoints, full Mix,
  augmentation form, GPU badge.
- The staging card is 48 GB with vLLM resident: training cap 22 GB (spike A3). Go and web code never name a framework
  or family (`internal/contract/seams_test.go`).
- **Hardening after the gate** (PRs #8, #10 and the follow-ups; ROADMAP "Phase 2 notes"): GPU budgets fail closed
  (an unknown GPU cost waits for approval) and subtract queued work, `pipelineRuns.retry` and `jobs.resume` are in
  `gpu-spend`; the reaper grants a grace after the control plane starts; plans check `pattern` ranges and defaults;
  `runs.new` refuses a language the base model's `locale:` tags lack and passes `x-cadence.shared` params (e.g.
  `target_lang`) to every step of the stage; periodic training states are published mid-lease and a lost lease resumes
  from the newest; step kinds may declare `optionalOutputs` (a finished fine-tune writes no training state); training
  states skip the backup mirror and evict without it (permanent); Settings → Content store and the
  `storage.low_space` warning; the Recipe document edits any text file and pipelines are planned before they commit;
  the agent host scans and pushes an agent's own commits; the Telegram bot answers `/status`, `/approvals`, `/help`;
  `dataset_import@3` transliterates (`sr-Cyrl-Latn`) and streams a capped `hf-dataset`; the NeMo pack passes the
  conformance suite on the card (`nightly.yml` needs a runner, below).

Phase 3 (evaluation) passed its gate on 2026-10-02: `evals.gate` returned a verdict on the stand (the Serbian
checkpoint beat the base model on its target and failed on 32 of 34 replay sets — forgetting; ROADMAP "Phase 3" gate
paragraph). Plan: `docs/review/2026-10-02-phase-3-plan.md`; what differs: ROADMAP "Phase 3 notes"; spikes S5 and A5
have numbers. What exists:
- **Registry**: golden sets (`goldenSets.freeze`, admin approval), scoring normalizers, model versions
  (`models.register`: passed gate + approval), lineage both ways (`registry.lineage`); leakage checks count the
  splits training reads.
- **Evals** (`internal/evals`): `evals.new|get|list|gate`, eval records cached across projects by model × golden set ×
  normalizer × decoding hash, the eval pipeline generated per eval (materialize → transcribe → score), paired
  blockwise bootstrap, `gates.yaml` (`gates.get|edit`), CER for languages without spaces, robustness axis,
  `languages` for a model trained under a neighbour's prompt.
- **Worker**: `wer_score`, `entity_score`, `latency_score`, `augment_dataset`, `spectrogram_tiles`; NeMo
  `nemotron_transcribe@3` and `nemotron_live@1` share one pipeline decoder (`cadence_nemo/pipeline.py`, five shims
  over NeMo 3.0 bugs), phrase boosting, `frame_vad`, `checkpoint_from_base` (role `materialize`).
- **Media and live** (tag `media`, never MCP; agents and API keys are refused): utterance audio with ranges and
  signed links, peaks, spectrogram tiles, `transcriptions.new` + the WebSocket relay to a worker live job (job kind
  `interactive`, daily allowance).
- **Web**: Eval report, Diff, Audio (`@/shell/audio`), Transcription, Golden set, Model, Lineage, Language pack,
  Experiment; language packs (he-IL, sr) in `templates/lang/`; experiments and sweeps.
- **The book**: `docs/tutorial/` (method in GUIDELINES.md, skill `cadence-tutorial`).

Phase 4 (data) is built on `feat/phase-4-data` (streams M, D, X, R, I, A, L, B; folded into the spec by stream S on
2026-10-04); its gate (a mounted Serbian corpus ingested, pseudo-labelled, frozen and trained on by the "Adapt a new
language" playbook) is still to run on the stand. Plan: `docs/review/2026-10-03-phase-4-plan.md`; what differs and
what is open: ROADMAP "Phase 4 notes". What exists:
- **Mounts and storage** (`internal/mounts`, `internal/storage`, `internal/eviction`): `mounts.list|get|new|scan|verify`
  (local, NFS/SMB path, S3, HF; adding one is an approval), `mount://<mount>/<path>#t=…&ch=…` URIs on utterances,
  `storage.get`, the content store as the cache (pins, LRU 85 → 70 %, project quotas), `datasets.materialize|evict`,
  backups to a mount (`backups.mirror_mount`).
- **Ingest and freeze** (`internal/data`): `sources.new` with clearing and ingest history ("no licence, no ingest"),
  draft dataset versions from `pipelines/data-ingest.yaml`, `datasets.preview|freeze` (leakage, quality, card,
  `cadence.dataset/1` cut with Lhotse shards), `utterances.search`; core kinds `sdp_ingest`, `text_normalise`,
  `manifest_filter`, `speaker_disjoint_split`, `dataset_freeze`, `segments_cut` (artifact `segments`).
- **Auxiliary models and pseudo-labels** (`internal/auxiliary`, `internal/triage`): registry kind `auxiliary`
  (adoption gated, licence-checked), `whisper_transcribe@1` and `lid_classify@1` (NeMo pack), `oasis_transcribe@1`
  (pack and runtime `services`), `pseudolabel_ensemble@1`, `pipelines/pseudo-label.yaml`, the triage queue.
- **Registry in full**: adoption with licence and locale checks, `data.lock` resolution, `versions.archive`, step-kind
  deprecation from the pack.
- **Interoperability** (`internal/exports`): `dataset_import@4` (Lhotse, NeMo, bundle), `datasets.export` +
  `exports.list|get` (`shar_export`, `dataset_export`, `hf_push` behind `hub-export`), `noise_mine@1`.
- **Annotation** (`internal/annotation`): `batches.new|get|list|freeze` (freeze gated → golden set or dataset),
  `batchItems.*`, `annotations.new`, `triage.accept|correct|reject`, reviewer invitations (`invitations.new|list`,
  `auth.accept`, role `reviewer`), `tracks.get` (energy/VAD/bandwidth tracks, media tag).
- **Alignment**: `align_reference@1` in runtime `omni` (omniASR CTC + torchaudio), golden-set reference alignments,
  emission delay in `latency_score@3`.
- **Playbooks and corpora**: "Adapt a new language" and "Try Cadence" (`person:`, `optional:`, `when:`),
  `cadence smoke`, `scripts/corpora/` (FLEURS sr, synthetic G.711 calls `calls-synth-sr`).
- **Web**: Storage, Source, Dataset version (`@/shell/data` charts, utterance search), Triage (a tool panel with
  Annotate mode), Annotation batch; Library gains Adopt and the this-project / all filter.

Known spec conflicts and gaps: `docs/review/2026-09-29-spec-kickoff-review.md` (statuses updated); assumptions made
while building are in `docs/spec/07-audit-risks-sources.md` "Open questions".

Gotchas:
- Generated files are committed; `make check-gen` fails CI when they are stale. After `docs/help` edits run
  `make help-sync` (the binary embeds `control-plane/internal/help/content`).
- `make web` overwrites the placeholder `control-plane/internal/webui/dist/index.html`; don't commit the built SPA.
- Dockview internals are allowed only in `web/src/shell/floating-snap/dockview-adapter.ts`; its browser contract test
  (`*.browser.test.ts`, headless Chromium) must pass on every Dockview upgrade. Floats must stay `border-box`.
- Dockview's `keyboardNavigation` and Smart Guides are enterprise modules: never enable them.
- Client-only commands are `view.<name>`; everything else is an API operationId.
- Toolchain: Go 1.27, Node 22, TypeScript 6.0 (typescript-eslint does not support 7), Python 3.12 via `uv`.
- Performance measurements run with Playwright tracing off; tracing alone drops frames.
- e2e stacks use private ports (`E2E_PG_PORT`, `E2E_API_PORT`, `E2E_WEB_PORT`; defaults 55433/18081/5174); pick
  your own when another worktree may be running one. Leftover `cadence-e2e-pg-*` containers are per port.
- Agents sanitise MCP tool names: `mixes.get` is `mcp__cadence__mixes_get` (Claude) and `cadence_mixes_get`
  (opencode); permission rules and the server's operation lookup use those forms (verbs never contain `_`).
- The agent host authenticates with the `cah_` token the control plane writes to `CADENCE_HOST_TOKEN_FILE` at start
  (a restart issues a new one and revokes the old). Run it locally with `CADENCE_URL`, `CADENCE_HOST_TOKEN_FILE`,
  `CADENCE_HOST_DATA` and `CADENCE_SESSION_UIDS=off` (no root; Claude then uses your own login — development only).
- `make gen` also regenerates the agent host's contract types (`agent-host/src/api/gen`, `npm run gen`).
- Claude streams a tool call's arguments into a pending call that starts as `{}`: read arguments only once the call
  has left `pending`. The agent runs in its own process group; the host ends the group before removing a session.
- Claude Code ignores the `allow` rules of a repository's `.claude/settings.json` (it applies ask and deny): the
  control plane sends the preset's allowed Cadence tools as `HostStart.allowedTools` and the driver passes them to the
  agent. A new Cadence tool an agent should use without asking belongs in the preset, not only in the rendered file.
- A stopping agent host calls `hostSessions.release`; compose gives it `stop_grace_period: 30s`. Restarting the stand's
  agent host is therefore safe mid-session, but a running turn is interrupted (the agent is told, not re-run).
- In vitest, `beforeEach(() => fn.mockReset())` returns the mock and vitest runs it as teardown: use a block body.
- A new parameter on a step kind changes its schema: bump the kind's version (`worker/step-kinds.lock.json` enforces
  it) and the bundled pipelines' pins; `optionalOutputs` and `x-cadence.shared` do not count.
- CI lints with golangci-lint v2.14.0 (`.github/workflows/ci.yml`); locally `docker run … golangci/golangci-lint:v2.14.0
  golangci-lint run ./...` in `control-plane` when the binary is not installed.
- A golden set's locale the model has no prompt for (Serbian) needs `evals.new.languages` (`{"sr-RS": "hr-HR"}`);
  the base model has no usable Thai. Evals transcribe at batch 8; batch 1 matches a live session word for word.
- The `corpora` mount is registered with root `/mnt/corpora`, the path inside the containers (compose binds the host's
  `CADENCE_CORPORA_DIR`, on the stand `/cadence/corpora`, there read-only); `exports` is `/mnt/exports`. A mount root
  is a container path, never the host's.
- A bundled pipeline that pins a new or bumped step kind needs the fixture
  `control-plane/internal/pipelines/testdata/bundled-kinds.json` regenerated: in `worker`,
  `CADENCE_UPDATE_BUNDLED_KINDS=1 uv run pytest tests/test_bundled_pins.py` (the integration test plans every
  bundled pipeline against it).
- OASIS returns lower case without punctuation, and Whisper writes Serbian in Cyrillic (the pseudo-label pipeline
  transliterates `sr-Cyrl-Latn`); OASIS is the owner's service, never started by Cadence (`scripts/serve.sh` on the
  host).
- The `omni` runtime image (`worker/Dockerfile.omni`, torch 2.8 + fairseq2) is ≈ 11.6 GB and runs only under compose
  profile `omni`; `services` (`worker/Dockerfile.services`) under profile `services`.
- For live agent runs keep prompts tiny; Claude sessions use `sonnet` (`haiku` delegates to subagents and loops),
  opencode `minimax/MiniMax-M3` on the stand (the free `opencode/big-pickle` elsewhere).

## Commands

```
make gen               # contract → Go stubs, MCP manifest, TS client + operation table; docs/help → embedded bundle
make check-gen         # CI: gen, then fail if anything generated is uncommitted
make lint              # golangci-lint, tsc + eslint (architecture rules), agent-host tsc, ruff + mypy --strict
make test              # unit + contract: go test, vitest (jsdom + headless Chromium), contrast, pytest, agent host
make test-integration  # control plane against Postgres (testcontainers, needs Docker)
make ui-e2e            # Playwright on the shell against the real control plane (Postgres in Docker)
make evals             # agent evals (agent-host/evals): fixture projects × both drivers; scripted agent, CADENCE_LIVE_AGENTS=1 live
make spikes-measure    # S1/S3/S4 measurements → web/test-results/spikes/*.json
make up                # docker compose: postgres + control plane (SPA embedded) + agent host, one CADENCE_VERSION
make web               # build the SPA into the control plane's embed directory
make e2e               # smoke project: the "Try Cadence" playbook in SMOKE_PROJECT=<slug> (cadence smoke)
```

Per package (single test in brackets):

```
cd control-plane && go test ./...                 # go test ./internal/<pkg> -run TestName ; -tags integration
cd control-plane && go run ./cmd/cadence          # serve on 127.0.0.1:8080 (DATABASE_URL required)
cd control-plane && go run ./cmd/cadence help     # the generated CLI: cadence <entity> <verb> (CADENCE_URL, CADENCE_TOKEN)
cd web && npm run dev                             # Vite on :5173, proxies /api to $CADENCE_API (default :8080)
cd web && npx vitest run --project unit           # npx vitest run src/shell/floating-snap -t "hysteresis"
cd web && npx vitest run --project browser        # Dockview contract tests in headless Chromium
cd web && npx playwright test e2e/shell.spec.ts   # -g popout
cd worker && uv run pytest                        # uv run pytest tests/test_steps.py::test_echo_copies_with_prefix ; -m gpu
cd worker && uv run python -m cadence_worker      # print the step-kind registry
cd agent-host && npm test
```

## Non-negotiables

IMPORTANT — these are enforced by lint or review; do not work around them:

1. Spec-first API. Change `api/openapi.yaml`, run `make gen`, then implement. Never hand-write client types or tool schemas.
2. One name in three places: an action is `<entity>.<verb>` — the same string is the OpenAPI operationId, the MCP tool name and the UI command id. Verbs come only from the vocabulary in `docs/spec/10-ui-shell.md` ("Verb vocabulary"); add a verb there before using it. Client-only UI commands (float, theme, palette) are `view.<name>` and never call the API.
3. Commands, not writes: every mutation has an actor, an `Idempotency-Key`, `If-Match` on the revision and a `dryRun`; it emits a domain event through the transactional outbox. Long work returns `202` with a job id.
4. Registry vs project: reusable assets (sources, dataset versions, golden sets, models, mounts, templates) are global, immutable versions named `YYYY-MM-DD.<sha>`; work (mixes, runs, evals, deployments, sessions) carries a `projectId`. Never put data in a project repository.
5. Every parameter has a default, description, source and safe range (`x-cadence` in the step schema); defaults live in `defaults.yaml`, nowhere else.
6. UI: panels register through a manifest, never import each other, never call Dockview; documents render header, actions and next-step from the entity manifest primitives. Base UI uses the `render` prop — `asChild` is rejected by lint.
7. Dockview: MIT packages only. Never install or read `dockview-enterprise`.
8. Agents: secrets never enter an agent context; production hosts are never reachable from Cadence; anything gated returns an approval id instead of acting.
9. Browser-reserved shortcuts (Ctrl/Cmd+W, T, N, Ctrl+Tab) are never assigned.
10. Errors are `application/problem+json` with a `type` URI that resolves to a help article in `docs/help/`; a new error type needs its page in the same PR.

## Workflow

- Branch `feat/<area>-<short>` or `fix/…`; Conventional Commits (`feat(control-plane): …`); one concern per PR, under ~400 changed lines when possible.
- Before you say you are done: `make lint test`, the affected spec file updated, help pages for new panels/steps/errors, `make gen` output committed.
- Tests follow the pyramid in `docs/spec/06-platform.md`: unit for logic, contract for anything generated, integration for outbox/SSE/approvals, Playwright for shell behaviour, fixtures from tiny public subsets only.
- Spikes (`docs/spikes/`) are time-boxed; fill the Result section, set `Status:`, and propose the spec change — do not turn a spike into product code without that step.
- Migrations are forward-only, expand-and-contract, embedded in the Go binary.
- Keep changes reversible: no destructive data operations in code paths without an approval command.

## Style (details in .claude/rules/)

- Go: standard layout, `internal/` packages by concern, errors wrapped with context, no global state, table-driven tests.
- TypeScript: strict; functional components; TanStack Query for data, Zustand for shell state; no `any`; no default exports except panels' manifests.
- Python: 3.12, typed (`mypy --strict` in `worker/`), pydantic v2 schemas, one module per step kind, no direct database access from steps.
- Names in code use the spec's glossary (`docs/spec/10-ui-shell.md`): version vs revision, freeze/register/promote, adopt, alias, draft, note.
- English for code, comments, commits and docs.

## Don't

- Don't add a UI-only capability, a hand-written API client, or a tool name outside the vocabulary.
- Don't write to the database from the worker, the agent host or a step kind — everything goes through the API.
- Don't touch `recipes/` semantics without reading `docs/spec/02-domain-projects-registry.md` — it is the shape of every project repository.
- Don't paste large spec sections into chat or commits; link the file and section.
