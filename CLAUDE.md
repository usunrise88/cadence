# Cadence — guide for coding agents

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
recipes/        Example project repository after bootstrap (pipelines, project.yaml, data.lock)
docs/spec/      The specification (read before changing behaviour); docs/spikes/ the eight gated experiments
.claude/rules/  Path-scoped rules that load when you touch the matching directory
```

## Commands

```
make up          # postgres + control plane + agent host via docker compose
make gen         # regenerate Go stubs, TS client and MCP tool manifest from api/openapi.yaml — run after any api/ change
make lint        # golangci-lint, eslint (with the panel and Base UI rules), ruff, mypy
make test        # unit + contract + integration (Postgres in Docker)
make e2e         # smoke project on the staging card — only from the staging host
make spikes      # status of docs/spikes/*.md
```

Per package: `cd control-plane && go test ./...`, `cd web && npm test`, `cd worker && pytest`, `cd agent-host && npm test`.

## Non-negotiables

IMPORTANT — these are enforced by lint or review; do not work around them:

1. Spec-first API. Change `api/openapi.yaml`, run `make gen`, then implement. Never hand-write client types or tool schemas.
2. One name in three places: an action is `<entity>.<verb>` — the same string is the OpenAPI operationId, the MCP tool name and the UI command id. Verbs come only from the vocabulary in `docs/spec/10-ui-shell.md` ("Verb vocabulary"); add a verb there before using it.
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
- Don't put credentials, tokens or `.env` contents anywhere in the repo, logs or test fixtures.
- Don't touch `recipes/` semantics without reading `docs/spec/02-domain-projects-registry.md` — it is the shape of every project repository.
- Don't paste large spec sections into chat or commits; link the file and section.
