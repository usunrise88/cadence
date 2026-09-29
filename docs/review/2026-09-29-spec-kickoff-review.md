# Review — spec v0.2 and kickoff skeleton (2026-09-29)

Full read of `docs/spec/*`, `docs/spikes/*`, `.claude/rules/*` and every kickoff file. Findings are ordered by
what blocks work first. Each has a proposed resolution; none is applied to the spec — the spec is an export of the
Claude Doc, so decisions go there first (then the decision log in `00-overview.md`).

Proposed resolutions for every open item: `docs/spec/08-resolutions.md`.

Status legend: **open** — needs a decision · **fix** — mechanical, can be done when the area is touched ·
**verify** — settle in the named spike.

## A. Blocks `make gen` (naming contract)

The generator is supposed to refuse any operation whose verb is not in the vocabulary (`01-principles.md` #11).
As written, the spec's own tool list would not pass it.

| # | Finding | Where | Proposal | Status |
| --- | --- | --- | --- | --- |
| A1 | Tool names use verbs missing from the vocabulary: `search.query`, `samples.query` (query), `triage.next` (next), `boost.evaluate`, `augment.evaluate` (evaluate), `agent-sessions:merge` (merge), `credentials:revoke` (revoke), `:bootstrap`, `sendAgentMessage` (send), `saveWorkspace` (save), `login`/`logout` | 05 tool table; 11 commands and backend contract | Either add `query, next, evaluate, merge, revoke, bootstrap, send, save` to the vocabulary (10-ui-shell) or rename (`search.query`→`search.run`?). Decide auth endpoints are exempt | open |
| A2 | Same action, two names: `runs.new` (05) vs `runs.create` (04 matrix); `evals.new` vs `evals.create`; `gates.edit` vs `gates.update` (04 gap #3); `projects.sync` vs `POST /projects/{p}:syncTemplates` | 04, 05, 11 | Keep the vocabulary form (`new`, `edit`, `sync`); fix 04 and the path | fix |
| A3 | Entity segment has no casing rule: `goldenSets` (tool), `golden-sets` (path), `golden_set` (EntityKind, topics); `agent-sessions` path vs `agent.session` topic | 04, 05, 06, 10 | One rule, e.g. tool/opId entity = camelCase plural (`goldenSets`), path = kebab plural, EntityKind/topic = snake singular; generator derives, never hand-types | open |
| A4 | No rule mapping HTTP shape → verb. `PATCH /triage/{id}` carries three verbs (accept/correct/reject); `PATCH /projects/{p}/baseline` is `baselines.set`; `PATCH` elsewhere is `edit` | 11 commands | Every non-`edit` verb is an action `POST /{res}/{id}:{verb}`; `PATCH` ⇔ `edit` only; `POST` collection ⇔ `new` | open |
| A5 | Current `api/openapi.yaml` uses camelCase opIds, has no shared `Idempotency-Key`/`If-Match`/`dryRun`/problem components, only `approveRequest` (no deny), `POST /agent-sessions` not under a project | api/openapi.yaml | Rewrite once A1–A4 are settled; it is a stub, not a contract yet | fix |
| A6 | Which item paths are global? Spec mixes `/runs/{id}`, `/evals/{id}:gate`, `/jobs/{id}`, `/drafts/{id}`, `/batches/{id}`, `/experiments/{id}`, `/models/{id}:export` with the rule "project-scoped resources live under `/projects/{p}/…`, registry under `/registry/…`" | 11 vs `.claude/rules/api-contract.md` | Rule: collections are scoped (`/projects/{p}/runs`), items addressed by globally unique id (`/runs/{id}`), authz checks the item's project. Model versions are registry → `/registry/models/{id}:export` | open |

## B. Security and data-handling gaps

| # | Finding | Where | Proposal | Status |
| --- | --- | --- | --- | --- |
| B1 | **Session token would be committed.** The worktree holds agent config "rendered … with the MCP endpoint and the token" (05 §lifecycle 2); `opencode.json.tmpl` inlines `Bearer {{ .Cadence.SessionToken }}`; the host commits the worktree after every turn and merges to `main`, which may be a GitHub repo | 05, templates/agent-config | Token only via env: opencode supports `{env:CADENCE_MCP_TOKEN}`; for Claude pass the MCP server in ACP `session/new` rather than a file. Add the rendered config paths to the worktree's `.git/info/exclude` | open |
| B2 | Agent host mounts `~/.claude` (credentials, history) into its container, and agents have a shell there → the agent can read the user's Claude credentials; contradicts "the MCP token is the only credential an agent holds" (05 Security). Also `:ro` likely breaks Claude Code, which writes session state | docker-compose.yml | Resolve in A1: separate `CLAUDE_CONFIG_DIR` per session, credentials outside the worktree sandbox's readable paths | verify (A1) |
| B3 | `aliases.set` can move `@baseline` / `@production`, bypassing the approval that `baselines.set` and promotions require | 02 registry rules, 04, 05 guardrails | Reserved aliases (`baseline`, `production`) are not settable through `aliases.set`; only through their gated commands | open |
| B4 | "Shell network limited to HF, PyPI, NGC" (05 guardrails) is unenforceable for opencode — its permission system has no network sandbox — and 05 Security says Cadence builds no sandbox of its own. Claude also needs its own API egress | 05 | Either run the agent host in a container with an egress proxy (Cadence-level sandbox after all) or downgrade the guardrail to "Claude driver only" | open |
| B5 | Agent host mounts all of `./recipes` read-write; agents must not see other projects' work (05 "What the agent sees") | docker-compose.yml | Mount per-session worktrees only | fix |
| B6 | Retention deletes production audio "with its derived rows" after 90 days (04 Block 5 gates), but redacted samples become registry Sources → dataset versions, which are immutable and undeletable while referenced (02). Deferral of "data governance" to phase 4 does not cover this v1 rule | 02, 04, 07 | Decide before the flywheel: tombstone members + re-freeze, or exclude production audio from frozen versions after N days | open |
| B7 | Postgres published on `5432` with default password `change-me`; control plane on `0.0.0.0:8080` although 06 says "listens on localhost only" behind Caddy — and localhost-only inside a container is unreachable from the agent host/worker | docker-compose.yml, 06 | Don't publish 5432; bind to the compose network; add Caddy per 06. Reword 06 to "not exposed outside the compose network" | fix |
| B8 | `CadenceEvent.projectId` is `required` in the OpenAPI schema, but registry events carry none (06); the stub emits `"-"` | api/openapi.yaml, main.go | Make it optional | fix |

## C. Spec-internal inconsistencies (smaller)

| # | Finding | Proposal | Status |
| --- | --- | --- | --- |
| C1 | Defaults are duplicated: `train-stage.yaml` hard-codes peak LR, steps, memory cap, replay; `data-ingest.yaml` hard-codes filters; `project.yaml` hard-codes budget and replay — against "defaults live in `defaults.yaml`, nowhere else". `defaults.yaml` does not exist and its location is unspecified | Pipelines omit params that equal defaults; place `defaults.yaml` (e.g. `control-plane/defaults.yaml`, embedded) | open |
| C2 | Eval latency set differs: 03 `[56,1]` primary + `[56,0]`, `[56,13]`; 11 Eval report shows `[56,0]`, `[56,1]`, **offline**; A3 evaluates only `[56,0]` | Pick the set once in 03 | open |
| C3 | Inactivity timeout 3 min (03) + "approval never answered → paused at inactivity timeout" (05) → almost every Telegram approval would pause the session | Separate timeouts: idle-in-turn vs waiting-approval | open |
| C4 | `agent_spend_per_day: 0 # 0 = subscription` — 0 reads as "no budget" | Explicit `unit: turns` + a number | fix |
| C5 | Mix "saved as a version" (11, openapi) — but "version" is reserved for immutable registry snapshots (glossary); Mix is project work with revisions | Say "save mix revision" or make mixes freezable | open |
| C6 | Pipeline version = commit SHA (03) vs "version identifiers for … pipelines: `YYYY-MM-DD.<sha>`" (00) | Pick one | fix |
| C7 | Skills list: 01 #8 names five, 05 names six (+`cadence-spikes`). `cadence-spikes` is a dev skill for building Cadence and must not be copied into projects | Done in repo: product skills moved to `control-plane/templates/skills/`; update 01/05 wording in the Doc | fix |
| C8 | Retention 90 days, subscription terms for ACP/headless/scheduled Claude sessions — both marked "awaiting confirmation" in 00, only the first is in 07 open questions | Add the subscription check to 07 | fix |
| C9 | Browser shortcut conflicts beyond the reserved list: Ctrl+Shift+P opens a private window in Firefox (switch project); Ctrl/Cmd+I is page info in Firefox | Verify interceptability in S2 or remap | verify (S2) |
| C10 | pseudo-label models in `data-ingest.yaml` (`nemotron-3.5-base`, `omnilingual-3b`) are not registry references | Reference base-model registry entries | fix |

## D. Kickoff skeleton vs its own rules

These are expected for a stub but will bite whoever trusts the docs.

- `make gen/lint/test/e2e` only `echo`; `make lint test` "passes". Makefile refers to `control-plane/oapi.yaml` and `control-plane/cmd/mcpgen` that don't exist. (Noted in CLAUDE.md "Current state".)
- ESLint rules the spec calls enforced (no `asChild`, panel isolation, Dockview adapter boundary) don't exist yet.
- `web/package.json`: `@base-ui-components/react` was renamed to `@base-ui/react` at v1.0; no TS/Vite config, React types, Vitest, Playwright, `@hey-api/openapi-ts`. All versions `latest`.
- `agent-host`: the image installs neither `opencode` nor Claude Code, yet README says opencode is "installed on the host" — the container can't reach it. No `src/drivers/`, no test script.
- `worker`: `echo.py` — "the template for every real one" — has no `x-cadence` metadata and no input hash, so it demonstrates neither rule; bare `dict` annotations fail `mypy --strict`; no `tests/`, so `pytest` exits 5.
- `control-plane`: module path `github.com/anton/cadence/control-plane` ≠ origin `usunrise88/cadence`; `main.go` uses a package-level counter and unchecked `w.Write` (errcheck).
- Templates: `{{ .Agent.PermissionPresetClaudeJSON }}`, `{{ .Agent.PermissionPresetOpencodeJSON }}`, `{{ .Cadence.MCPURL }}`, `{{ .Cadence.SessionToken }}` are not documented in `templates/README.md` (rule in `recipes-and-templates.md`).
- `recipes/projects/hebrew/.claude/settings.json` is hand-written (`deny: Bash(rm -rf *)`), not rendered from a preset; no `NOTES.md`, `lang/he-IL/`, `augment/`. Pipelines moved into `projects/hebrew/pipelines/` (they were a byte copy of the templates at `recipes/` root).
- Whether opencode reads `.opencode/opencode.json` (wizard, hebrew example) or root `opencode.json` (template) — settle in A1.

## E. What is solid

The layering is coherent: one contract → three clients; outbox → SSE with `Last-Event-ID`; registry/project split
with a lockfile; immutable versions vs revisions; guardrails expressed as approvals instead of UI-only checks;
spikes that target the real unknowns (Dockview internals, ACP adapter completeness, Nemotron under a 24 GB cap).
Counts claimed in the spec check out (44 entities, 32 panels = 13 + 19, 13 backend needs).

## Suggested order

1. Decide A1–A4 and A6 (a half-page naming rule) — nothing generated can be written before it.
2. Decide B1, B3, B4 before spike A1/A2 — they change the agent-host design.
3. B6 and C3 before the flywheel and scheduled sessions; the rest when the area is touched.
