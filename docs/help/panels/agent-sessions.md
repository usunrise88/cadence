---
title: Agent sessions
summary: Every agent session of the project by state — driver, model, budget use, pending approvals, merge state — and New session.
contexts: [panel:agent-sessions]
---

## What this is

A tool panel. The **Agents** badge in the status bar (it counts live sessions and turns amber while one waits for an
approval) opens a small popup like Notifications: the live sessions, each opening its Chat, and a button that opens
this panel as a floating window. It also opens from the palette ("Open Agent sessions", "New agent session…") or
from a Chat. It lists the project's sessions, those needing you first:

| Part | Meaning |
| --- | --- |
| Label | `claude-code · session 3` — the driver and the session's number in the project; agent badges use the same |
| Kind | interactive (its own branch) or read-only (one turn, "Explain this") |
| State | running (· working while a turn is in progress), waiting approval, paused (with the reason), asleep (paused for idleness; a message in its Chat wakes it), done, failed, cancelled |
| Pending approvals | Requests of this session waiting in Approvals |
| Model | The driver's model id |
| Budget use | Turns and tokens used of the session budget |
| Merge | The state of the session branch: none, pending (Session changes to accept or discard), conflict, merged, discarded |

The list stays live on the `agent.sessions` topic. **All / Live / Ended** filters it.

## Place in the loop

Prepare (start a session) and decide (accept or discard what a session changed).

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Agent | the agent profile's driver | Claude Code or opencode |
| Model | the profile's model for its driver, else the driver's catalogue default | opencode accepts any `provider/model` |
| First message | empty | Optional; without one the session waits for your message in Chat |
| Kind | interactive | Read-only sessions start from "Explain this" on a document header or a help article |

## Commands

| Command | Keys | API |
| --- | --- | --- |
| New session | — | `agentSessions.new` |
| New session → From a playbook | — | `playbooks.run` (Show the estimate runs it with `dryRun`; Start playbook runs it for real) |
| Open Chat | — | client: shows the session in Chat |
| Pause / Resume | — | `agentSessions.pause`, `agentSessions.resume` |
| Accept / Discard changes | — | `agentSessions.accept`, `agentSessions.revert` (paused or ended sessions) |

## Playbooks

- **Start a playbook**: New session → From a playbook, pick it (playbooks of later phases are listed, greyed), fill
  the inputs the project cannot give (the dataset version for "Fine-tune from a dataset version"; the rest start from
  `defaults.yaml`), Show the estimate — the plan and the GPU-hours appear — then Start playbook. Its Chat opens.
- A session paused on its budget: open its Chat, read the reason, Resume (the budget is raised in Agent settings or
  with `agentSessions.resume` and a larger budget).
- Several sessions ended with changes: accept the clean ones here; open the Chat of one with a conflict to see its diff.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Agent sessions), "Default workspaces" (status-bar badges).
- docs/spec/05-agents.md "Session kinds", "Session lifecycle"; [Agent sessions guide](../guides/agent-sessions.md).
