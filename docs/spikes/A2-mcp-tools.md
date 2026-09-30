# A2 — Cadence MCP server driven by both agents

Status: done
Box: 2 days

## Goal
Prove that tools generated from the OpenAPI contract are usable by both agents to create a mix and dry-run a training job.

## Setup
Control plane with an MCP server (Streamable HTTP) exposing ten generated tools with curated descriptions; a session-scoped token.

## Steps
1. Generate tools from api/openapi.yaml. 2. Start an opencode session with the server configured; ask it to create a mix and dry-run a run. 3. Same with Claude Code. 4. Check the audit log: actor, session id, causedBy tool call id.

## Acceptance
Both agents pick the right tools without hints; every mutation is attributed; dryRun estimates are returned.

## Record
Tool descriptions that needed rewording; token scoping issues; failures.

## Result

Run 2026-09-30 by hand on the development machine, through the real product path rather than a harness: the
control plane (`cadence serve`, Postgres 17 in Docker), a project from the wizard (`projects.new`, internal
repository, default agent profile and `guardrails-default` preset), a mix `he-smoke` over the fixture dataset
version, and the agent host (`src/index.ts`) claiming sessions created with `agentSessions.new`. The prompt named
the goal only, no tool names: "The mix he-smoke samples too evenly: make its sampling temperature 0.7. Then check
what a training run on that mix would cost, without starting it, and tell me the estimate in one sentence."

**Verdict: both agents picked the right tools without hints, every mutation is attributed to the session down to the
tool call, and the dry-run estimate came back.**

| | Claude Code (claude-agent-acp 0.84.0, `haiku`, owner's subscription) | opencode 1.18.33 (`opencode/big-pickle`, OpenCode Zen, free) |
| --- | --- | --- |
| Tools, in order | skill `cadence-train` → ToolSearch → `mixes.list` → `mixes.edit` {temperature 0.7, ifMatch "1"} → `runs.new` dryRun | `mixes.list` → `drafts.get`/`drafts.list` → `mixes.get` → `mixes.edit` dryRun then real (0.6) → `runs.new` dryRun → `projects.note` |
| Mix change | a draft (`drf_…`, before 1 → after 0.7), the mix stays at revision 1 (draft policy `mix: draft`) | its own draft (one open draft per author and entity) |
| Estimate | 0.833 GPU-h (0.417–1.25, basis table), 3000 s, one card, within the 8 GPU-h budget | the same estimate, stated with its range and defaults |
| Turn | 1 turn, 62 input + 2 232 output tokens (+176 k cached reads), API-equivalent $0.063 | 1 turn, 245 + 357 tokens (+33 k cached), $0 |
| Permission requests | 3 (the MCP calls), all answered by the preset in the host round trip, none reached a person | 1: a chained shell command (`cat NOTES.md …; git log`) → an agent-permission approval a person approved once |

Attribution: the audit row of `mixes.edit` has actor `{kind: agent, id: crd_…, sessionId: ses_…}`, preset
`guardrails-default`, rule `draft`, and `causedBy.toolCallId` = `toolu_014dow5RBTqxe4kN2uZ8v2Yn`, which is the ACP tool
call id of the transcript's tool-call entry — the badge-to-Chat link holds end to end. The draft's author is the
same actor.

The same run, repeated in the agent-host image (`docker run` as root with the per-session users, opencode/Zen, a
prompt that writes a file): the session ran as uid 20000 in a 0700 directory, opencode's `fs/write_text_file` landed
as that user, the turn was committed and pushed to `session/<id>`, and `agentSessions.cancel {end}` revoked the token,
fast-forwarded main (auto-merge when-clean) and removed the worktree.

Surprises:
1. **Claude names MCP tools with underscores.** A tool `mixes.get` is `mcp__cadence__mixes_get` in Claude's tool calls
   and permission rules; the rendered `.claude/settings.json` listed `mcp__cadence__mixes.get`, matched nothing, and
   every call asked. The host's answer then denied it (the operation `mixes_get` matched no preset rule), so the
   first run could not touch Cadence at all. Fixed: the renderer emits the sanitized names (`policy.ClaudeMCPTool`)
   and the host and server map `mixes_get` back to `mixes.get` (verbs never contain underscores). opencode does the
   same (`cadence_mixes_get`); its rules were already rendered that way.
2. **Claude still asks for allowed MCP tools.** Even with the corrected allow rules the adapter raised a permission
   request per Cadence call. The preset answers them at once (`tools.read`, `tools.draft`, `tools.spend`), so no
   person sees them, but each costs a round trip. Open: whether claude-agent-acp applies project `permissions.allow`
   to MCP tools. Answered 2026-09-30: Claude Code does not load a project file's allow rules at all (only its ask and
   deny rules); the host now pre-allows the preset's Cadence tools through `allowedTools` (docs/spec/05-agents.md,
   "Phase 1 as built").
3. **Claude loads the product skill first** (`cadence-train`) and defers MCP tools behind ToolSearch, as A1 saw.
4. **`runs.new` dry runs did not name the mix.** Claude sent `{init: base}`, opencode `{datasets: [ver_…]}`; the
   estimate is the table estimate either way. The runs.new description should say to pass the mix (phase 2 wires
   the mix into the estimate).
5. **opencode read presence and tried the other session's draft** (a mistyped id → 404 with its help article), then
   explained correctly that its edit makes its own draft. Two sessions editing one mix leave two drafts; the person
   picks one.
6. **Restoring an ACP session after the host restarted failed for Claude** ("Resource not found": the session lived
   in a per-session HOME that the development run had replaced); the host fell back to a new ACP session primed with
   the transcript summary, as designed.

Tool descriptions that needed rewording: none for this task. Token scoping issues: none; the agent token reached
only its project and read the registry.

What the spec should change: Claude's MCP rule names (above) in R7; `runs.new`'s tool description names the mix.
