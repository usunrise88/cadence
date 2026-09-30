<p align="center">
  <img src="web/public/favicon.svg" width="72" height="72" alt="Cadence logo">
</p>

<h1 align="center">Cadence</h1>

<p align="center">
  A self-hosted workbench for fine-tuning streaming ASR models, built so that people and AI agents work in it together.
</p>

<p align="center">
  <img alt="Status: work in progress" src="https://img.shields.io/badge/status-work%20in%20progress-f5a524">
  <img alt="Phase 2 of 6" src="https://img.shields.io/badge/roadmap-phase%202%20of%206-3e63dd">
  <img alt="Go" src="https://img.shields.io/badge/Go-control%20plane-00add8">
  <img alt="React" src="https://img.shields.io/badge/React-web%20shell-61dafb">
</p>

> [!WARNING]
> **Work in progress.** The shell and the agent loop work today. Training, evaluation, data and deployment are next,
> in the order of the [roadmap](ROADMAP.md). Expect breaking changes; there is no release yet.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/workbench-dark.png">
  <img alt="A Claude Code session drafts a change to a mix and asks for approval to move the baseline" src="docs/images/workbench-light.png">
</picture>

## What it is

Cadence covers the whole life of a Nemotron ASR fine-tune: data → training → evaluation → export → deployment → the
production flywheel. It has two ideas:

- **Agent-native.** Claude Code and opencode work through the same API as the UI. Their edits arrive as drafts
  attributed to the session, gated actions wait for a person's approval, and every panel updates live.
- **API-first.** One OpenAPI contract generates the Go server, the web client, the MCP tools and the CLI. An action has
  one name everywhere: `mixes.edit` is the operation, the tool and the UI command.

Everything is versioned: recipes live in git, registry assets (datasets, models, golden sets) are immutable versions,
and every mutation has an actor, an idempotency key and an audit row.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/palette-dark.png">
  <img alt="The command palette searches registry collections, dataset versions and help at once" src="docs/images/palette-light.png">
</picture>

## Status

| Phase | Delivers | |
| --- | --- | --- |
| 0 · Shell | Contract toolchain, control plane, dockable web shell | ✅ |
| 1 · Agent loop | Auth, MCP, agent host, sessions, drafts, approvals, projects, registry core | ✅ |
| 2 · Training | GPU worker protocol, artifact store, queue, runs, playbooks | ⏳ next |
| 3 · Evaluation | Golden sets, streaming eval, gates, manual transcription tests | |
| 4 · Data | Mounts, ingest, freeze, annotation | |
| 5 · Deploy and flywheel | Export, shadow and canary, triage, schedules | |

## Architecture

```
web (React, Dockview) ──┐
agents (Claude Code,    ├── REST · SSE · MCP ──▶ control plane (Go) ──▶ Postgres (state, River jobs, outbox)
  opencode via ACP)  ───┤                              ▲
CLI ────────────────────┘                              └── GPU worker (Python, NeMo) leases jobs over HTTP
```

| Path | What |
| --- | --- |
| `api/` | OpenAPI 3.1 contract: the source of truth |
| `control-plane/` | Go: REST API, MCP server, outbox → SSE, jobs, policy engine, internal git |
| `web/` | Vite + React SPA: Dockview shell, shadcn on Base UI, Radix colours, Iconoir |
| `agent-host/` | TypeScript ACP client hosting Claude Code and opencode sessions; agent evals |
| `worker/` | Python step registry, runs inside the NeMo Speech container (phase 2) |
| `docs/spec/` | The specification; `docs/help/` the in-app help |

## Run it

```sh
echo "POSTGRES_PASSWORD=$(openssl rand -hex 16)" > .env
make up            # postgres, control plane (SPA embedded), agent host, egress proxy
```

Open http://127.0.0.1:8080; the first visit creates the admin account. Agent model accounts are connected in
Settings → Agents.

## Develop

```sh
make gen                 # contract → Go stubs, TS client, MCP tools, CLI
make lint test           # Go, TypeScript and Python checks, unit and contract tests
make test-integration    # control plane against Postgres
make ui-e2e              # Playwright against the real control plane
make evals               # agent evals, scripted by default; CADENCE_LIVE_AGENTS=1 runs real agents
```

Read [`CLAUDE.md`](CLAUDE.md) and [`docs/spec/README.md`](docs/spec/README.md) before changing behaviour; work is
picked from [`ROADMAP.md`](ROADMAP.md).
