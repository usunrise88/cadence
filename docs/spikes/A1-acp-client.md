# A1 — One ACP client for opencode and Claude Code

Status: done
Box: 2 days

## Goal
Prove the agent host design: initialize, session/new with an MCP server, session/prompt, streamed updates, permission round trip, cancel, resume — against both agents.

## Setup
agent-host/ with @agentclientprotocol/sdk; opencode installed (opencode acp); claude-agent-acp adapter; a throwaway MCP server with one tool.

## Steps
1. Spawn opencode acp; run the full lifecycle; log every session/update kind seen. 2. Same with claude-agent-acp. 3. Make the agent edit a file and capture the tool-call diff content. 4. Trigger a permission request and answer it from the host. 5. Cancel mid-turn. 6. Resume a session where supported.

## Acceptance
Both agents complete the lifecycle; diffs and permissions arrive as structured updates; the host code has no agent-specific branches outside the driver.

## Record
Update kinds per agent; features missing in the Claude adapter vs the native SDK; latency to first token.

## Result

Run 2026-09-29, about half a day of the 2-day box. Harness: `cd agent-host && npm run spike:a1 -- --record`
(`src/spikes/a1.ts`, lifecycle in `src/spikes/lifecycle.ts`). Setup:
- Versions: `@agentclientprotocol/sdk` 1.5.1 and `@agentclientprotocol/claude-agent-acp` 0.84.0, both the latest
  releases, so no pin changed. opencode is `opencode-ai` 1.18.33, pinned as an agent-host devDependency.
- Models: Claude `haiku`, through the machine's Claude Max login. opencode `opencode/big-pickle`, a free OpenCode
  Zen model that needs no credentials.
- MCP: a throwaway streamable-HTTP server (`src/spikes/mcp-echo.ts`) with one tool, `echo`. It refuses any request
  without `Authorization: Bearer cst_…`.
- Transcripts: redacted and stored in `agent-host/test/fixtures/` (`<driver>-main.jsonl`, `-restore.jsonl`,
  `-summary.json`).

**Verdict: both agents complete the whole lifecycle through one ACP client.** Nothing needed the native Claude SDK,
and nothing agent-specific lives outside `src/drivers/`. The code in `src/acp` and `src/drivers/agent.ts` is plain
ACP. Each driver supplies only four things:
- how to launch the agent;
- `_meta` for `session/new`;
- how to read its tool names: MCP, shell, and opencode's todo tool;
- how to find a shell call's output and exit code.

| Step | Claude Code (claude-agent-acp) | opencode (`opencode acp`) |
| --- | --- | --- |
| initialize / session/new | 386–457 ms / 753–834 ms | 1977–2277 ms / 1093–1338 ms |
| MCP turn, header `Authorization` | tool called, 0 of 11 requests unauthorised | tool called, 0 of 10 requests unauthorised |
| Permission round trip (host answered `allow_once`) | MCP tool, Edit, Bash: 3 requests, options allow_once / allow_always / reject_once | same 3, same options (MCP asks only with `"cadence_*": "ask"`) |
| File edit diff | `tool_call_update` content `diff` **before** the permission request (the card can show it) | `diff` in the completed update; the permission request carries a unified diff in `rawInput.diff` |
| File writes | the agent writes itself; the client `fs/*` is never called | through the client `fs/write_text_file`, so the host's workspace guard applies |
| Cancel mid-turn | `cancelled`, 11 ms from `session/cancel` to the stop | `cancelled`, 60 ms |
| Resume in a new process | `session/resume`, 805–826 ms, 0 replayed updates, context kept (answered the nonce) | `session/resume`, 945–969 ms, same |
| Also advertised | `loadSession`, session list / fork / close / delete, additional directories, `providers/*` | `loadSession`, session list / fork / close |
| First thought (reasoning) | 0.5–1.4 s | 1.0–2.1 s |
| First message token | 2.2 s (shell turn), 2.8 s (after resume), 5.2 s (MCP turn incl. ToolSearch + permission), 11.9 s (plan + read + edit) | 2.6 s (shell), 3.8 s (after resume), 4.0 s (MCP), 11.4 s (edit; 53 s in another run) |

The first-token numbers are from prompt to the first `agent_message_chunk` in the recorded run. The ranges cover
4 Claude runs and 2 opencode runs; opencode then passed the gated live test once more.

Update kinds, recorded run (5 turns):
- **Claude:** `agent_thought_chunk` 283, `usage_update` 34, `tool_call_update` 27, `agent_message_chunk` 9,
  `plan` 8, `tool_call` 7, `available_commands_update` 4, `session_info_update` 1. Also the extension notification
  `_auth/status_update`, which carries the account's plan, e-mail and organisation.
- **opencode:** `agent_thought_chunk` 29, `tool_call_update` 14, `tool_call` 6, `agent_message_chunk` 5,
  `usage_update` 5, `available_commands_update` 2. No `plan`: the driver maps its `todowrite` tool to one, which
  has the same shape.
- **Not seen from either:** `user_message_chunk` (only `session/load` replays history), `current_mode_update`,
  `config_option_update`, `notice`, compaction.
- **Usage:** the `session/prompt` response carries input, output and cached tokens for both agents; opencode also
  reports thought tokens. `usage_update` carries the context window used and its size, plus a USD cost: Claude
  reports an API-equivalent cost even on the subscription, opencode reports 0.
- **Normalised stream:** the host maps all of this to `message`, `thought`, `plan`, `tool_call` (a snapshot folded
  from the call and its updates, with class mcp / edit / shell / read / …, the diffs and shell details),
  `permission`, `permission_decided`, `usage`, `state` (turn started/ended), and `info` (everything else, passed
  through).

What the Claude adapter lacks compared with the native Claude Agent SDK. None of this blocks phase 1:
- **Hooks and in-process MCP tools:** not reachable over ACP. The HTTP MCP server is enough.
- **`rewindFiles()`:** the adapter enables file checkpointing only for AIR (JetBrains) clients, and ACP has no
  rewind method. Git per-turn commits cover this for us.
- **Subagent transcripts:** gated behind AIR capabilities. Otherwise subagents appear as ordinary tool calls.
- **Shell exit code:** Claude reports none. `_meta.claudeCode.toolResponse` has stdout, stderr and `interrupted`,
  and a failure shows as status `failed`.
- **Already mapped:** `canUseTool` becomes `session/request_permission`, and `interrupt()` becomes `session/cancel`.
- **SDK options:** the adapter takes them through `_meta.claudeCode.options` on `session/new` (used here for
  thinking).

Surprises:
1. **Claude thoughts are empty unless asked for.** Recent models default thinking display to "omitted". The Claude
   driver sends `thinking: {type: "adaptive", display: "summarized"}` when `thoughts` is on. Without it, one run
   thought silently for 20 s with no update at all, and would have tripped R5's stuck-turn clock at a longer
   stretch.
2. **Thought chunks are token-sized:** 283 per Claude run. The host must coalesce them before persisting; one event
   per `session/update` would flood `agent.session.{id}`.
3. **Claude leaks its surroundings into the session.** It loads user settings (`settingSources`
   user/project/local), and `available_commands_update` lists the user's installed skills and plugins. The
   `_auth/status_update` notification carries the account e-mail. This supports R3 (a per-session
   `CLAUDE_CONFIG_DIR`, never the user's `~/.claude`), and `_auth/*` must never reach transcripts.
4. **Claude defers MCP tools.** It runs a `ToolSearch` tool call before the first MCP call, which adds about 1.5 s
   and one extra tool call. It also sends the MCP method `server/discover` before `initialize`. Our server answers
   method-not-found and Claude falls back, so stream E's server must tolerate it the same way.
5. **opencode does not ask before MCP calls by default.** The rendered `permission` block needs
   `"cadence_*": "ask"` (or finer patterns). Claude's default mode asks for every MCP tool, so a preset should allow
   read-class tools explicitly to avoid approval noise.
6. **Diffs differ in scope.** Claude's diff is the edited fragment (`old_string` → `new_string`). opencode sends
   the whole file in the permission request, then a fragment. Treat ACP diffs as display only: the per-turn git diff
   stays the source of truth for the Recipe document.
7. **Ids and titles differ.** opencode names sessions `ses_…` and titles an edit's permission with the file path.
   Claude uses UUID sessions and titles like "Edit notes.txt". Only the drivers' readers see this.
8. **A free model is enough for tests.** `opencode/big-pickle` (OpenCode Zen free tier) works with no secrets. It
   is fine for spikes and the gated live test, but prompts go to a third-party free endpoint, and latency varies
   (the edit turn took 11 s in one run and 53 s in another).

What the spec should change:
- **R2 is confirmed for both agents.** The MCP token travels only in `session/new` → `mcpServers[].headers`, so the
  `{env:CADENCE_MCP_TOKEN}` fallback can be dropped.
- **Resume in 05-agents "Drivers":** for Claude it becomes "native `session/resume` (adapter ≥ 0.84), `session/load`
  as an alternative". Summary injection stays only as the fallback for a failed restore. The host picks resume or
  load from the `initialize` capabilities, not per driver.
- **"Every `session/update` is persisted as an event"** should become "coalesced": consecutive message and thought
  chunks merge into one event per block, and a tool call becomes one event per status change.
- **The Claude driver asks for summarized thinking.** Add this to the Drivers table so the "thinking" block in Chat
  is not empty.
- **The preset renderer (R7)** must emit MCP patterns for opencode (`cadence_*`) and `mcp__cadence__*`
  allow / ask rules for Claude.
- **The Chat shell card** shows the exit code only when the agent reports one; Claude never does.
- **No native Claude SDK driver is needed** for phase 1 or its gate. Keep the escape hatch named for rewind and
  hooks only.
- **The agent-host image must install opencode.** Here it is a devDependency, so `npm ci --omit=dev` in the
  Dockerfile skips it. `npm ci` in CI now also downloads the opencode binary (about 100 MB).

Tests that keep this honest without agents or network:
- `test/drivers.contract.test.ts` replays each recorded transcript through `test/replay-agent.ts`, a fake agent
  process speaking the recorded ACP over stdio, and checks every property above for both drivers.
- `test/live.test.ts` runs the real agents. It is off unless `CADENCE_LIVE_AGENTS=claude,opencode` is set.
