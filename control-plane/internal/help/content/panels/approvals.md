---
title: Approvals
summary: Gated requests from agents, automations and registry actions waiting for a person — approve once, approve for the session, or deny.
contexts: [panel:approvals]
---

## What this is

A tool panel (Ops workspace, right column; also floating from the **Approvals** badge in the status bar) listing
every request the policy engine stopped for a person, oldest first, with the decided history below. Each card
shows:

| Part | Meaning |
| --- | --- |
| Scope tag | **Registry** (mounts, golden-set freeze, model registration — decided by the admin) or the project's slug |
| Operation | The gated command, e.g. `aliases.set` or `projects.archive` — the same name as its MCP tool |
| Reason | Why the rule asked for a person |
| Requested by | A person, an automation key (`Automation · <key name>`) or an agent session |
| Estimate | GPU-hours the command would spend and what is left of today's budget, when it spends |
| Rule | The preset rule that decided (`baseline-alias`, `gpu-spend`, …) |
| Action | The stored request (method and path) that runs when approved; the body under "Request body" |
| Expires in | Countdown to `expiresAt`: nobody deciding within 24 hours denies it |
| Decision | For decided cards: approved once / for the session / denied / expired, by whom, the note, the replay's status |

The list stays live: requests and decisions arrive on the `approvals` topic and patch the panel, the status-bar
badge and the notification history at once. The **Scope** switch shows all, project or registry approvals.

## Place in the loop

Decide. The card is the confirm — there is no second dialog. Approving replays the stored request as its original
actor; its events carry `causedBy.approvalId`, so the audit log traces the change back to your decision.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Grant | once | **Approve for this session** also lets the same agent session repeat the same operation on the same path; only requests from an agent session offer it |
| Note | empty | Optional, up to 2000 characters, recorded with the decision |
| Expiry | 24 h | `timeouts.approval_expiry_hours` in defaults.yaml (R5) |

## Commands

| Command | Keys | API |
| --- | --- | --- |
| Approve request | Enter on a focused card | `approvals.approve` (If-Match on the approval's revision) |
| Deny request | Backspace on a focused card | `approvals.deny` |

Arrow Up / Down move between cards; after a decision the next pending card takes focus. From the palette,
"Approve request" and "Deny request" act on the card that last had focus. A card decided elsewhere meanwhile
answers `412` ([precondition-failed](../errors/precondition-failed.md)) and the list refreshes.

## Playbooks

- An agent asks to move `@baseline`: read the reason and the target version, approve once, or deny with a note the
  agent will read.
- A run over budget: compare the estimate with what is left today; approve for the session only if you want the
  agent to repeat the same operation without asking.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Approvals); docs/spec/05-agents.md "Guardrails"; docs/spec/08-resolutions.md R5, R7, R8.
- The policy engine and presets: [Approvals and the policy engine](../guides/approvals.md).
