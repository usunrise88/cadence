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

## Current state (phase 0 done; phase 1 next)

Work follows `ROADMAP.md`: six phases (0 Shell → 1 Agent loop → 2 Training → 3 Evaluation → 4 Data → 5 Deploy and
flywheel), each closed by a gate. Pick work from the current phase; tick items there as they merge; don't start an
item whose **decide** line is still open. Proposed answers to every **decide**/**spec** line are in
`docs/spec/08-resolutions.md` (R1–R39), ported to the Doc; they win over older spec text until it is rewritten.

Phase 0 (shell) passed its gate on 2026-09-29: spikes S1–S4 are done with numbers in `docs/spikes/`. What exists:
- **Contract toolchain.** `api/openapi.yaml` follows R1; `api/vocabulary.yaml` is the machine copy of the verb table
  (a test keeps it equal to `docs/spec/10-ui-shell.md`). `control-plane/cmd/mcpgen` refuses naming violations and
  writes `internal/mcp/tools.json`, `internal/api/planned.gen.go` (501 for `x-cadence.planned` operations) and
  `web/src/api/operations.gen.ts`. The API lives under `/api`.
- **Control plane.** pgx + embedded migrations, fixed dev actor (`usr_admin`) until phase 1 auth, command pipeline
  (idempotency, If-Match/412/428, dryRun by rollback), outbox → SSE, projects, per-user workspaces, help bundle, slog
  / Prometheus / OTel file traces, SPA embedded (`make web`).
- **Web shell.** Dockview host + adapter, panel and entity registries, selection bus, command registry + palette,
  workspaces with migrations, SSE client, theme, Library / Inspector / Help / Project panels. Panels use only
  `@/shell/panel`, the entity primitives and `@/components/ui`; ESLint enforces it.
- **Worker / agent host.** Step-kind contract with `x-cadence` and input hash; the agent host is still the A1 stub.

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

## Commands

```
make gen               # contract → Go stubs, MCP manifest, TS client + operation table; docs/help → embedded bundle
make check-gen         # CI: gen, then fail if anything generated is uncommitted
make lint              # golangci-lint, tsc + eslint (architecture rules), agent-host tsc, ruff + mypy --strict
make test              # unit + contract: go test, vitest (jsdom + headless Chromium), contrast, pytest, agent host
make test-integration  # control plane against Postgres (testcontainers, needs Docker)
make ui-e2e            # Playwright on the shell against the real control plane (Postgres in Docker)
make spikes-measure    # S1/S3/S4 measurements → web/test-results/spikes/*.json
make up                # docker compose: postgres + control plane (SPA embedded) + agent host, one CADENCE_VERSION
make web               # build the SPA into the control plane's embed directory
make e2e               # smoke project on the staging card — phase 4
```

Per package (single test in brackets):

```
cd control-plane && go test ./...                 # go test ./internal/<pkg> -run TestName ; -tags integration
cd control-plane && go run ./cmd/cadence          # serve on 127.0.0.1:8080 (DATABASE_URL required)
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
