---
title: Agent sessions
summary: How Claude Code and opencode run as Cadence sessions — lifecycle, budgets, pauses, permissions, merging the session branch — and the one-time setup of their logins.
contexts: [guide:agent-sessions]
---

## What this is

An agent session is Claude Code or opencode working in a project on its own branch `session/<id>`, reached only
through the Cadence MCP server with a token that dies with the session. You start one with `agentSessions.new` (the
Chat panel, Ask agent), talk to it with `agentMessages.new`, and watch it on the `agent.session.{id}` topic: every
message, thought, plan, tool call, permission request, turn and commit is a transcript entry
(`agentMessages.list`). The agent host runs it; the control plane keeps everything the host reports.

## Place in the loop

The session is how an agent does the loop's work under the Guardrails: entity changes go through MCP tools (as
drafts where the project's draft policy says so), file changes are committed on the session branch after every
turn, gated commands and the agent's own permission requests wait for a person in Approvals.

## Fields and defaults

| Field | Default | Where it comes from |
| --- | --- | --- |
| kind | interactive | `read-only` answers one message under the read-only preset, with no branch |
| driver, model | the agent profile | Agent settings; `defaults.yaml` `wizard.driver`, `wizard.claude_code_model`, `wizard.opencode_model` |
| preset | the agent profile's | `read-only` for read-only sessions |
| budget.turns | 200 | policies (`budgets.agent_turns_per_session`) |
| budget.tokens | 10 000 000 | `budgets.agent_tokens_per_session` (input + output, cached reads not counted) |
| budget.tokensPerTurn | 1 000 000 | `budgets.agent_tokens_per_turn` |
| stuck turn | 5 min | `timeouts.stuck_turn_minutes`: no update during a turn cancels it and pauses |
| idle | 30 min | `timeouts.idle_session_minutes`: no message pauses an interactive session ("asleep"); the next message wakes it |
| auto-merge | when-clean | the agent profile: at the end the branch merges if it applies cleanly — never when it touches `gates.yaml`, `lang/`, `project.yaml`, `.claude/` or `opencode.json` (the gate, the language packs and the settings wait for a person to accept the branch) |

States: `created` → `running` ⇄ `waiting_approval` (a permission request or a gated command is pending; waiting never
pauses) → `paused` (a person, idleness, a stuck turn, three identical tool calls in a row, a budget) → `done`,
`failed` or `cancelled`. Every pause carries its reason and writes a note to NOTES.md for the next session.

## Commands

- `agentSessions.cancel` stops the current turn; with `{"end": true}` it ends the session: the host commits what is
  left, the token is revoked, and the branch merges per auto-merge (or stays as Session changes).
- `agentSessions.pause` / `agentSessions.resume` stop and restart the agent process; resume restores the agent's own
  session (ACP) or starts a new one primed with a summary of the transcript. A session paused by its budget resumes
  only with a larger budget (`{"budget": {"turns": …, "tokens": …}}`).
- `agentSessions.accept` / `agentSessions.revert` merge or discard the session branch as a whole, once the session is
  paused or ended; a conflict answers [merge-conflict](../errors/merge-conflict.md) and changes nothing.
- `agentMessages.new` sends a message. A session asleep (paused for idleness) resumes first and then gets it; a
  session paused for any other reason answers [conflict](../errors/conflict.md) with the reason until it is resumed.
  References such as `@mix:mix_…` or `@run:123` become a context block for the agent and its `selection://current`.

Agents cannot start, message, accept or revert sessions (the `sessions-are-for-people` rule).

## Playbooks

**One-time setup (the owner).** The agents' own logins live in the `agent-credentials` volume, never in Cadence's
secrets and never your own `~/.claude`:

```
docker compose run --rm -it agent-host login claude     # claude setup-token: sign in with the Claude account, paste the token
docker compose run --rm -it agent-host login opencode   # opencode auth login: choose MiniMax, paste the Token Plan key
```

The Claude token (`claude/oauth-token`) and opencode's `opencode/auth.json` are copied into each session's private
HOME. The agent host authenticates to the control plane with its own token, which the control plane writes to the
`host-credential` volume at start (`CADENCE_HOST_TOKEN_FILE`); for a host on another machine,
`cadence admin host-token` prints one.

**The sandbox.** Each session runs as its own Unix user in a 0700 directory; the agent host's network is internal and
its only way out is the egress proxy, which lets through the model APIs, Hugging Face, PyPI and NGC and nothing else.
Add a host to `CADENCE_EGRESS_ALLOW` in `docker-compose.yml` if an agent needs it.

**A commit was refused.** The host scans every turn's staged diff for tokens and keys (`cst_`, `cdk_`, `sk-ant-`,
`hf_`, …); a hit leaves the files uncommitted, shows an error in the transcript and tells the agent to remove it.

## Sources

docs/spec/05-agents.md "Agent integration"; docs/spec/08-resolutions.md R2–R6; docs/spikes/A1-acp-client.md,
docs/spikes/A2-mcp-tools.md.
