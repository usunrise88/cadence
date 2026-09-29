# Cadence

Self-hosted workbench for the full life of Nemotron ASR fine-tunes: data, training,
evaluation, export and deployment, production flywheel. Agent-native and API-first:
Claude Code or opencode operate the same API the UI uses, and the UI shows their work live.

Spec: the "Cadence — spec v0.2" document (system tab + UI shell tab).

## Layout

```
api/            OpenAPI 3.1 contract — the source of truth for the Go server, the TS client and the MCP tools
control-plane/  Go: REST API, MCP server, event outbox → SSE, River jobs, Postgres, content store
web/            Vite + React SPA: Dockview shell, shadcn on Base UI, Radix Slate + Indigo, Iconoir
worker/         Python: step registry, NeMo Speech jobs (runs inside the NeMo Speech NGC container)
agent-host/     TypeScript: ACP client hosting Claude Code (claude-agent-acp) and opencode (opencode acp)
recipes/        Example project repository after bootstrap: pipelines, mixes, project.yaml, data.lock (data itself lives in the registry)
docs/spikes/    The eight spikes that gate phase 0 and phase 1
.claude/skills/ Dev skills for building Cadence (cadence-spikes); product skills live in control-plane/templates/skills/
.claude/rules/  Path-scoped rules for coding agents working on Cadence itself
docs/spec/      The specification, split by area (export of the Claude Doc)
```

## Start here

1. Open `CLAUDE.md` (agents load it automatically; opencode through `opencode.json`), then `docs/spec/README.md`.
2. Read `docs/spikes/README.md` and run S1 and A1 first — they carry the two biggest unknowns.
3. `make up` brings up Postgres, the control plane stub and the agent host stub with `docker compose`.
4. Every spike ends with a filled-in "Result" section in its brief; the spec's decision log is updated from it.

## Conventions

- Spec-first API: edit `api/openapi.yaml`, then regenerate (`make gen`).
- Recipes and pipelines are files; database entities point at commit SHAs.
- Versions are `YYYY-MM-DD.<short-sha>`.
- No secrets in the repo: `.env` (git-ignored) holds HF/NGC tokens for the worker only.
