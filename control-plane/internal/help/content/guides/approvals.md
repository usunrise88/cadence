---
title: Approvals and the policy engine
summary: How Cadence decides what an agent may do on its own, what waits for a person as an approval, and how an approved request runs.
contexts: [guide:approvals, panel:approvals, error:policy-denied]
---

## What this is

Every command — from the UI, an API key or an agent's MCP tool call — goes through the policy engine before it
runs. The engine reads the permission preset of the caller's credential and answers one of three things:

| Answer | What you get | What happens |
| --- | --- | --- |
| allow | The command's normal answer | It runs |
| approval | `202 {"approvalId": "apr_…"}` and a `Cadence-Approval-Id` header | The request is stored for a person; `approval.requested` goes out on the `approvals` topic |
| deny | `403` [policy-denied](../errors/policy-denied.md) | Nothing runs |

A dry run (`?dryRun=true`) of a command that would need approval runs as a dry run and says so in the
`Cadence-Policy: approval; rule=<id>` header, so an agent sees the estimate and that a person will decide.

## Place in the loop

Guardrails (docs/spec/05-agents.md): agents do anything reversible on their own; GPU spend over budget, golden
sets, gates, the baseline alias, deployments and registry changes wait for a person; deletes, secrets and
credentials are out of reach. People are allowed everything except what is gated for everyone (the `baseline`
alias, R8).

## Fields and defaults

Presets live in `control-plane/templates/presets/<name>.yaml` and are rendered into the agents' own permission files
(`.claude/settings.json`, opencode's `permission` block) at bootstrap; those files only save prompts, the server
decides. Claude Code ignores the allow rules of a repository's `.claude/settings.json`, so the agent host passes the
Cadence tools the preset allows to each Claude session at start (`allowedTools`): they run without a permission
request, and the Chat does not list a permission the preset allowed on its own for a call it shows.

| Preset | Used by |
| --- | --- |
| `guardrails-default` | Agent sessions and automation keys unless their profile names another |
| `read-only` | "Explain this" sessions: read verbs only |

Rule classes: `read` and `draft` allow; `spend` allows while the dry-run estimate of GPU-hours fits what is left of
today's budget, otherwise asks for approval; `gated` asks for approval; `forbidden` denies. Rules apply in order,
first match wins; an agent command no rule matches is denied.

An approval carries the stored request (method, path, query, headers without credentials or cookies, body), who
sent it, the rule and reason, the estimate when there is one, and `expiresAt`: nobody deciding within **24 hours**
denies it (R5; `decision.expired: true`).

## Commands

- `approvals.list` — pending first; filter `state` (pending, approved, denied, decided) and `project`.
- `approvals.get` — one approval with its stored request and, once approved, `result` (the replay's status and body).
- `approvals.approve` — `If-Match` on the approval's revision; body `{"grant": "once" | "session", "note": …}`. The
  stored request runs right away as its original actor; its events carry `causedBy.approvalId`. With
  `grant: "session"` the same agent session may repeat the same operation on the same path without asking again.
- `approvals.deny` — `If-Match`; optional `note`. The request never runs.
- `audit.list` — every command with its actor, outcome, rule and cause (command, tool call, approval).
- `jobs.wait` — agents wait for long work (a `202 {jobId}`) instead of polling.

Repeating the original request with the same `Idempotency-Key` answers the stored `202` while the approval is
pending and the real result once it was approved.

## Playbooks

- Agent got `202 {approvalId}`: tell the user what is waiting and why (the approval's `reason`), then wait for
  `approval.decided` on the `approvals` topic (or read `approvals.get`); do not resend the command.
- Agent got `policy-denied`: report it; never try another route to the same effect.
- Person: decide from the Approvals panel, the inline card in Chat or Telegram — all record the same audit entry.

## Sources

- docs/spec/05-agents.md (Guardrails, Security); docs/spec/08-resolutions.md R5, R7, R8.
- Cadence recommendation: the rules in `guardrails-default`.
