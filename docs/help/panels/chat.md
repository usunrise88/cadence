---
title: Chat
summary: One agent session's live transcript — replies, thinking, plan, tool calls, approvals, commits — with the session's state, budget, stop, pause and end, and the merge of its changes.
contexts: [panel:chat]
---

## What this is

A tool panel, one per agent session. Every default workspace has one in its right column; the workspace remembers
which session it is pinned to (pick another from the session menu in its header, or "New session…"). "Open Chat" in
Agent sessions, an approval card or an attribution badge opens the session's own Chat next to it when the
workspace's Chat already shows another session.

The transcript renders what the agent host reports on `agent.session.{id}`:

| Entry | Shown as |
| --- | --- |
| Agent reply | Streaming Markdown; references such as `@mix:mix_…` or `@recipe:project.yaml` are links that open the document |
| Thought | A collapsed "Thinking" block |
| Plan | A checklist that ticks as the agent works |
| Cadence tool call (MCP) | Card with the operation (`mixes.edit`), the entity as a link, the draft it left, a dry run's estimate (GPU-hours ±, duration, card, audio hours), the approval or job it returned, and the arguments and result |
| File edit | Card with an inline diff per file (added and removed lines are marked by `+`/`−` as well as colour) |
| Shell | Card with the command, the exit code (or status) and the output, collapsed |
| Permission or approval request | The approval card inline: **Allow once**, **Allow for this session**, **Deny** for the agent's own requests; Approve / Deny for gated Cadence commands. The same request is in Approvals |
| Commit | The commit on the session branch and its files, or why nothing was committed (a credential in the diff) |
| Turn end | Stop reason and tokens in / out |

The header shows the session (`claude-code · session 3`), its kind (interactive or read-only), its state (running,
idle, waiting approval, paused with the reason, done, failed), the model, and meters for turns and tokens against
the session budget.

When an interactive session ends (or is paused), **Session changes** shows the merge state of its branch
`session/<id>`: the changed files, **Diff in Recipe** (the branch's diff against main in the Recipe document), and
**Accept into main** or **Discard…**. A clean branch of a project whose auto-merge policy is `when-clean` merges by
itself at the end; otherwise it waits here.

## Place in the loop

The agent runs the same loop: the prompt and plan prepare, dry runs check, turns run, the transcript and diffs
review, accepting drafts and session changes decides, and the note it leaves records.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Driver, model | the agent profile | A message in an empty Chat starts an interactive session with the profile's driver and model; Agent sessions lets you pick |
| References | the selection | Chips above the message; each becomes `@<kind>:<id>[#part]` and the agent reads the entity's facts before your text |
| Budget | policies | `budgets.agent_turns_per_session`, `budgets.agent_tokens_per_session`; a session over budget pauses |

## Commands

| Command | Keys | API |
| --- | --- | --- |
| Ask agent about the selection | Ctrl/Cmd+I | client: focuses Chat and attaches the selection |
| Send | Enter (Shift+Enter: new line) | `agentMessages.new`, or `agentSessions.new` when the Chat has no live session |
| Stop the agent's turn | Ctrl/Cmd+. | `agentSessions.cancel` |
| Pause / Resume | — | `agentSessions.pause`, `agentSessions.resume` |
| End the session | — (inline confirm) | `agentSessions.cancel` with `end: true` |
| Accept / discard session changes | — | `agentSessions.accept`, `agentSessions.revert` (the session must be paused or ended) |
| Allow / deny a request | Enter / Backspace on the focused card | `approvals.approve`, `approvals.deny` |

Screen readers hear when a turn finishes (with the start of the reply), when a session pauses, fails or ends —
through the polite live region; streamed tokens are not announced.

## Playbooks

- **Ask about what you see**: select a mix row or open a document, press Ctrl/Cmd+I, type the question, Enter.
- **Follow a change back**: click the agent badge on a mix or draft ("claude-code · session 3") — Chat opens at the
  tool call that made it, highlighted.
- **Finish a session**: End…, then review Session changes and Accept into main (or Discard).

## Sources

- docs/spec/05-agents.md "What the Chat panel shows", "Context bridge", "Worktree, drafts and merge".
- docs/spec/11-ui-panels.md "Panel catalogue" (Chat), "Default workspaces"; docs/spec/10-ui-shell.md "Accessibility and input".
- [Agent sessions guide](../guides/agent-sessions.md).
