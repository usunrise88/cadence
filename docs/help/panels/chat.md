---
title: Chat
summary: One agent session's live transcript — replies, thinking, plan, tool calls, approvals, commits — with the session's state, budget, stop, pause and end, and the merge of its changes.
contexts: [panel:chat]
---

## What this is

A tool panel, one per agent session. Every default workspace has one in its right column; the workspace remembers
which session it is pinned to (pick another from the session menu in its header, or "New session…"). "Open Chat" in
Agent sessions, an approval card or an attribution badge opens the session's own Chat next to it when the
workspace's Chat already shows another session. Its tab names the session the short way — `CC · S4` is Claude Code
session 4, `OC` is opencode — and shows a dot when a turn finished or the session wants you (an approval, a pause, a
failure, its end) while the Chat was hidden; showing the Chat clears it. The chat icon is filled with the session's
status: blue working, green waiting for your message, amber waiting for a decision, grey paused, red failed.

The transcript renders what the agent host reports on `agent.session.{id}`:

| Entry | Shown as |
| --- | --- |
| Agent reply | Streaming Markdown; references such as `@mix:mix_…` or `@recipe:project.yaml` are links that open the document |
| Thought | A collapsed "Thinking" block |
| Plan | A checklist that ticks as the agent works |
| Cadence tool call (MCP) | One line with the operation; click it for the card with the operation (`mixes.edit`), the entity as a link, the draft it left, a dry run's estimate (GPU-hours ±, duration, card, audio hours), the approval or job it returned, and the arguments and result |
| File edit | One line with the +/− counts; click it for an inline diff per file (added and removed lines are marked by `+`/`−` as well as colour) |
| Shell | One line with the command and a failed exit code; click it for the output |
| Permission or approval request | The approval card inline: **Allow once**, **Allow for this session**, **Deny** for the agent's own requests; Approve / Deny for gated Cadence commands. The same request is in Approvals |
| Commit | The commit on the session branch and its files, or why nothing was committed (a credential in the diff) |
| Turn end | Stop reason and tokens in / out |

Tool calls stay one quiet line each until you open them; the attribution badge opens the one it jumps to. Typing
anywhere in the Chat outside a field goes to the message box.

The header is one line: the session (`claude-code · session 3`), its state (running, idle, waiting approval, paused,
done, failed; hover it for the reason), the kind when it is not interactive, the model, and the session's controls as
icons in the corner — stop the turn, pause or resume, end. Meters for turns and tokens against the session budget sit
below it.

When the agent host restarts, the header reads **reconnecting** with the line "The agent host is restarting —
reconnecting…" until the next host takes the session, usually within seconds; a host that stopped answering for
90 s shows "The agent host stopped answering" until another host takes over. A turn the restart interrupted is not
run again: the transcript says so, and your next message continues the session (the agent is told its last turn was
interrupted). Messages the old host had not started go to the next host by themselves. Pause and End pressed
meanwhile wait for the next host; a Stop is dropped, since the turn it meant has already ended.

A permission request the agent was waiting on when its turn ended — you pressed Stop, paused or ended the session, a
budget or clock paused it, or the host restarted — is cancelled, not declined: the transcript notes why, and the agent
is told the same before its next prompt, so it does not report that you declined.

While a turn runs, **Session changes** lists the files the agent has changed in its worktree but not committed yet
(state *editing*): status, lines added and removed, or the size of a new file. The agent host watches the worktree
and reports within about a third of a second; the list empties when the turn's commit takes the files (a file the
credential scan refused stays listed). Only paths and sizes travel — the diff itself appears once the turn commits.

When an interactive session ends (or is paused), **Session changes** shows the merge state of its branch
`session/<id>`: the changed files, **Diff in Recipe** (the branch's diff against main in the Recipe document), and
**Accept into main** or **Discard…**. A clean branch of a project whose auto-merge policy is `when-clean` merges by
itself at the end; otherwise it waits here.

When `main` moved on the same lines, the state is *conflict* and each conflicting file has a **Three-way** button: the
merge base, `main` and the session's version of the file, with every conflict marked (amber bar). At the Chat's usual
width the three versions are stacked per conflict (base, main, session); in a wider Chat they sit side by side.
**Unified** shows one column instead: unchanged lines from `main`, what each side changed, and each conflict as its
main, base and session parts. **Next conflict** / **Previous conflict** (or `n` / `p` inside the view) move between
conflicts. Files that merge cleanly keep the two-way diff in the Recipe document. A binary file, or one over 128 KiB,
has no three-way view. Accepting stays disabled until the conflict is gone: resolve it on the branch (a new session
turn) or discard the changes.

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
| Stop the agent's turn | Ctrl/Cmd+., or the square icon in the header | `agentSessions.cancel` |
| Pause / Resume | The pause / play icon in the header | `agentSessions.pause`, `agentSessions.resume` |
| End the session | The eject icon in the header (inline confirm) | `agentSessions.cancel` with `end: true` |
| Accept / discard session changes | — | `agentSessions.accept`, `agentSessions.revert` (the session must be paused or ended) |
| Three-way view of a conflicting file | Next / Previous conflict, `n` / `p` in the view | `branches.compare` |
| Allow / deny a request | Enter / Backspace on the focused card | `approvals.approve`, `approvals.deny` |

Screen readers hear when a turn finishes (with the start of the reply), when a session pauses, fails or ends —
through the polite live region; streamed tokens are not announced.

## Playbooks

- **Ask about what you see**: select a mix row or open a document, press Ctrl/Cmd+I, type the question, Enter.
- **Follow a change back**: click the agent badge on a mix or draft ("claude-code · session 3") — Chat opens at the
  tool call that made it, highlighted. For opencode sessions the badge learns the tool call a moment after the change
  (when the agent host reports the call); until then it opens the session's Chat.
- **Finish a session**: the eject icon, End the session, then review Session changes and Accept into main (or Discard).
- **A conflict at the end**: open Three-way on the file, compare main with the session's version, then either ask
  the agent in a new session to redo the change on top of main, or Discard.

## Sources

- docs/spec/05-agents.md "What the Chat panel shows", "Context bridge", "Worktree, drafts and merge".
- docs/spec/11-ui-panels.md "Panel catalogue" (Chat), "Default workspaces"; docs/spec/10-ui-shell.md "Accessibility and input".
- [Agent sessions guide](../guides/agent-sessions.md).
