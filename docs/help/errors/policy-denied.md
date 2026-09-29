---
title: Denied by policy
summary: The policy engine refused this command for your credential — the detail names the preset and the rule. Nothing ran and no approval was created.
contexts: [error:policy-denied, guide:approvals]
---

## What this is

A `403` problem from the policy engine (docs/spec/08-resolutions.md R7). Every command asks the engine first: it
answers allow, approval (the command is stored and answered `202 {approvalId}`) or deny. Deny means the permission
preset of your credential has a `forbidden` rule for this operation, or no rule allows it at all. The `detail`
names the operation, the rule and the preset, e.g. `sources.archive is denied by rule "no-deletes" of the
guardrails-default preset: removing things is left to people`.

Agents and automation keys get the preset of their scope (`guardrails-default` unless the session says otherwise;
"Explain this" sessions get `read-only`); people are allowed everything except what is gated for everyone.

## Place in the loop

Guardrails (docs/spec/05-agents.md): agents do anything reversible on their own; what spends over budget, touches
production or changes shared definitions waits for a person; deletes, secrets and credentials are out of an
agent's reach. The attempt is recorded in the audit log (`audit.list`, outcome `denied`).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/policy-denied` |
| `status` | `403` |
| `detail` | The operation, the rule id, the preset and the rule's reason |

Rules that deny, in `guardrails-default`:

| Rule | Denies | Why |
| --- | --- | --- |
| `approvals-are-for-people` | `approvals.approve`, `approvals.deny` | An agent never decides approvals |
| `admin-only` | `secrets.*`, `credentials.*`, compute and policy changes | Secrets never enter an agent context |
| `no-deletes` | every `archive` and `revoke` not gated above | Removing things is left to people |
| `no-match` | anything no rule allows | Default deny for agents |
| `token-scope` | a project other than the credential's | Session tokens are bound to one project |

In `read-only`, `no-changes` denies every mutating command.

## Commands

- Read the rule in the detail; `help.get` on `guides.approvals` explains the presets.
- If a person should do it, say so in your reply with the exact operation and arguments; do not retry.

## Playbooks

- Agents: never retry a denied command with other arguments to get around the rule; report it and move on.
- People: to let agents do more, change the project's agent profile to another preset (Agent settings).

## Sources

- RFC 9110, HTTP Semantics, §15.5.4 403 Forbidden.
- Cadence recommendation: the presets and rules in `control-plane/templates/presets/`.
