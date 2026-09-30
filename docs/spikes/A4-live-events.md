# A4 — Outbox → SSE → cache patching with an agent editing

Status: done
Box: 1 day

## Goal
Prove that a change made by an agent through MCP appears in an open panel within one round trip, attributed.

## Setup
Control plane stub with an outbox table and the /events endpoint; a Mix panel bound to TanStack Query; an agent session from A2.

## Steps
1. Open the Mix panel. 2. Have the agent change the mix through MCP. 3. Measure time from commit to panel update. 4. Kill the SSE connection; reconnect with Last-Event-ID; verify no gap. 5. Edit the same mix from the UI while the agent edits: confirm the 412 conflict path.

## Acceptance
Update visible under 300 ms; resume without loss; conflict surfaces instead of overwrite; badge shows the session.

## Record
Latency p50/p95; events per second the panel can absorb before coalescing is needed.

## Result

**Acceptance met, with a scripted agent and with real Claude Code and opencode sessions** (the phase-1 gate, below).
Scripted agent, measured 2026-09-30 by `make spikes-measure` (`web/e2e/a4-live-events.spec.ts`, results in
`web/test-results/spikes/A4.json`): Playwright 1.63, headless Chromium, Vite dev server (unminified React, `/api`
proxied), control plane and Postgres 17 in Docker on the same shared host (AMD EPYC 7351P, 32 threads), tracing off.
Three runs.

Setup: a project with a mix over `dataset/fleurs-he-smoke`, opened as a document from the Library. The "agent" is an
MCP client that holds a real agent session token (`cst_`, preset `guardrails-default`, minted on the e2e database by
`control-plane/internal/e2etools/mintagent`, which `web/e2e/stack.sh` builds) and calls `mixes.edit` on `/mcp` with
Claude Code's `_meta.claudecode/toolUseId` — the same path a driver takes, without a model. Each edit lands as a draft
(the default draft policy); the panel shows it inside the dashed outline with the badge `agent · session 4`.

| Measure (30 edits after 3 warm-up) | Run 1 p50 / p95 | Run 2 | Run 3 |
| --- | --- | --- | --- |
| Commit → draft visible (draft `updatedAt`, the commit transaction's `now()`, to the DOM mutation) | 58 / 107 ms | 46 / 71 ms | 41 / 50 ms |
| Agent sends `tools/call` → draft visible | 62 / 111 ms | 50 / 78 ms | 45 / 55 ms |
| Agent receives the result → draft visible | 24 / 36 ms | 24 / 38 ms | 23 / 33 ms |

- **Under 300 ms:** p95 at most 107 ms, max 127 ms. The chain is commit → `NOTIFY` → dispatcher → hub → SSE → Vite
  proxy → one animation-frame batch → cache patch (`entity.mix.{id}`, `draft.*`, `presence.changed`) → React.
- **Resume without loss:** the event stream was dropped in the page while the agent made 5 more edits; the shell
  reconnected from the last seq it had seen (`?after=88`) and showed the last draft 29–47 ms after the reconnect
  (1.5–2.0 s after the drop: the shell waits 2 s before reconnecting a closed stream). All 76 events of the mix's topic
  arrived exactly once (0 missing, 0 duplicates, checked against `events.list`).
- **Conflict, not overwrite:** the person edits rev 2 in the table while another tab saves rev 3; Save answers `412
  precondition-failed`, the panel shows the conflict notice (Reload / Reapply my changes) and the stored mix keeps
  the other change. The same holds when the other change is an agent's accepted draft (`web/e2e/mix.spec.ts`).
- **Badge shows the session:** `agent · session 4` on the draft and, after acceptance, on the mix header (the draft's
  author), each carrying the tool-call id for the Chat click-through.
- **Events per second before coalescing is needed:** 60 edits back to back committed at 49–69 edits/s (97–138
  events/s: `draft.updated` + `presence.changed` each); the panel absorbed them in 43–48 animation-frame batches and
  showed the last draft 19–58 ms after its result reached the agent. The per-frame coalescing already in the stream
  is enough at the rate one agent can write; no further coalescing is needed.

Surprises:
- Chromium's offline emulation (`context.setOffline`) leaves an open event stream connected, so it cannot simulate a
  drop; the harness closes the shell's `EventSource` and fails it (readyState CLOSED), which exercises the shell's
  own reconnect with `?after=`. EventSource's native retry with `Last-Event-ID` (server closes the stream) is
  covered by the control plane's `TestEventStreamFilterAndResume` only.
- The draft's revision, not the mix's, is what the panel waits for: an agent's successive edits leave the mix at rev 1
  and bump the draft (`data-draft-rev`), so the harness measures each edit without a person accepting in between.

### Real drivers (the phase-1 gate, 2026-09-30)

Setup: the real system on the development machine, not compose — Postgres 17 in Docker (127.0.0.1:55901),
`cadence serve` built from `feat/phase-1-agent-loop` (127.0.0.1:18901, `CADENCE_HOST_TOKEN_FILE`), the SPA on the Vite
dev server (:5901, unminified), and the agent host run with tsx (`src/index.ts`, `CADENCE_SESSION_UIDS=off`, no
`CADENCE_AGENT_CREDENTIALS`: its unprivileged development mode — sessions run as the developer's user in 0700
directories and Claude uses the machine's own login). Admin created on the first-start screen, a project through
`projects.new` (bootstrap job, internal repository, default profile: draft policy `mix: draft`, preset
`guardrails-default`), one mix per driver over `dataset/fleurs-he-smoke`, opened from the Library in headless Chromium
(Playwright 1.63, tracing off). A session per driver was started with `agentSessions.new` and one prompt that names the
goal, not the tools: "Set the sampling temperature of mix gate-claude to 0.5, then 0.6, then 0.8, then 0.9, and finally
0.7 — one separate edit per value, in that order. Then give me a training-run estimate for that mix without starting
it, in one sentence." Drivers: Claude Code through claude-agent-acp 0.84.0 on `sonnet` (the profile default, the
owner's Claude subscription); opencode 1.18.33 on `opencode/big-pickle`, the free OpenCode Zen model, because the
owner's MiniMax Token Plan key is not installed yet (R6's model stays to be checked). The harness was a Playwright
script outside the repository (the steps above, then the measurement below); four runs per driver, each on a fresh
project.

Measure: a `MutationObserver` in the page stamps the first time each draft revision (`data-draft-rev`) shows in the
Mix panel; the draft's `updatedAt` (the commit transaction's `now()`, same host clock) is the start.

| Commit → draft visible, 20 edits per driver | p50 | p95 | max | per-run p50 / p95 |
| --- | --- | --- | --- | --- |
| Claude Code (`sonnet`) | 37 ms | 56 ms | 56 ms | 43/56, 45/52, 37/45, 33/41 |
| opencode (`opencode/big-pickle`) | 47 ms | 68 ms | 68 ms | 47/63, 51/55, 44/57, 46/68 |

- **Draft and badge:** every edit landed on the agent's one open draft (rev 1 → 5, the mix stays at rev 1) and showed
  in the dashed outline with the badge `claude-code · session 1` / `opencode · session 2`, carrying the session id.
- **Conflict, not overwrite:** before each prompt the person typed weight 4 into the table (unsaved, rev 1); the table
  is locked while an agent's draft is open; the person accepted the draft in the panel (the mix became rev 2 with
  temperature 0.7), then pressed Save: `412`, the conflict notice ("the mix moved to rev 2 while you edited rev 1;
  nothing was overwritten") with the session's badge, and the stored mix kept weight 1 and temperature 0.7 — 8 of 8.
- **Attribution:** the audit has the five `mixes.edit` of each session with actor `{kind: agent, sessionId}`, preset
  `guardrails-default`, rule `draft`, `causedBy.toolCallId`.
- **Turns:** Claude 21–24 s, 1.6 k output tokens + 217 k cached reads, $0.15 API-equivalent on the subscription;
  opencode 33–56 s, 0.2–0.4 k output + 33–39 k cached, $0. No permission request reached a person in any run (the
  preset answered every MCP permission in the host round trip).
- **Dry-run estimate:** each run ended with `runs.new?dryRun=true` → 0.833 GPU-h (0.417–1.25, basis table), 3000 s,
  one staging card, within the 8 GPU-h budget, stated in the reply.

Surprises (real drivers):
1. **The runaway rule paused Claude on its third edit** (fixed in `agent-host`, commit "count a tool call for the
   runaway rule once it leaves pending"): Claude streams a tool call's arguments into a pending call that starts as
   `{}`, and the host counted each call at its first update, so three `mixes.edit` with different values looked
   identical. Calls now count once they leave `pending`.
2. **Ended Claude sessions left their directory behind** (fixed, "end the agent's whole process group"): Claude's CLI
   outlives the adapter by a moment and wrote its MCP logs into the session's HOME after the host removed it. The agent
   now runs in its own process group, which the host ends and waits for.
3. **`haiku` is not reliable enough for the gate prompt.** On `haiku` Claude once delegated the edits and the estimate
   to subagents (Task); a subagent then called `ToolSearch select:mcp__cadence__runs_new` three times in a row and the
   runaway rule paused it — correctly. `sonnet`, the profile default, did the whole prompt in 4 of 4 runs.
4. **opencode's tool calls cannot be linked from the badge.** opencode sends no tool-use id in MCP `_meta`, so
   `causedBy.toolCallId` is the server's fallback `mcp:<mcp session>/<rpc id>`, while the transcript names the call
   `call_function_…`. The badge opens the session's Chat but cannot land on the tool call. Open: correlate in the host
   (it sees both the ACP call and its arguments) or have the server accept the transcript id another way.
5. **The free model is sloppy with ids and notes.** Once it mistyped the version id in `runs.new` (404), retried with
   the collection name and explained the 404 as "not adopted"; twice it wrote a `projects.note` from a misreading (that
   a `mixes.edit` dry run "registers a draft", because the dry-run answer shows the draft and presence that would
   exist). Those notes land in NOTES.md. The MiniMax model should be checked the same way once its key is installed.
6. **Claude still raised a permission request for every Cadence call** (A2 surprise 2); the preset answered each one
   at once.
7. **In the development mode Claude reached the account's claude.ai connectors** (its MCP logs named one): with the
   host's own login the session loads what the account has connected. The compose path copies only
   `claude/oauth-token` from the agent-credentials volume; whether a `setup-token` session loads connectors is open —
   check it before real projects, and disable them for sessions if it does.

A production build (minified React, SPA served by the control plane) should only be faster.

What the spec should change (real drivers): 05 "Drivers" — the runaway rule counts a call once its arguments are final;
the host ends the agent's process group; opencode's tool-call ids need a correlation for the attribution badge; the
`mixes.edit` dry-run `next` text should say the draft and presence it shows are what would exist. Earlier, from the
scripted run: 06 "Real-time model" now describes drafts (own revision, one per author, accept writes
the next revision with `causedBy.draftId`, `412 draft-stale`) and presence (open drafts plus a direct-edit window,
`presence.changed`); record in the decision log that the 300 ms budget is met with the existing per-frame coalescing
and that the shell's 2 s reconnect delay after a closed stream dominates recovery time (lower it if resumes must be
faster than the budget).
