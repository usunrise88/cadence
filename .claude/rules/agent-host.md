---
paths:
  - "agent-host/**"
---
# Agent host rules (ACP)

Read docs/spec/05-agents.md before changing session behaviour.

- One ACP client (`@agentclientprotocol/sdk`); drivers for Claude Code (`claude-agent-acp`) and opencode (`opencode acp`) implement the same ACP-shaped interface. No agent-specific branches outside `src/drivers/`.
- A session = one worktree on `session/<id>` + one scoped token + one budget. The host commits after every turn with the turn id and never writes to `main`.
- Every `session/update` is persisted as an event on `agent.session.{id}` through the control plane API; the host keeps no state the API does not have.
- Permission requests and gated commands become approvals via the API; the host never decides them itself except through the policy engine's answers.
- Budgets and runaway checks (three identical tool calls, token limit, inactivity) pause the session with a reason event.
- The host never sees Cadence secrets; the MCP token is the only credential it forwards.
- Tests: driver contract tests against recorded ACP transcripts; a live test per driver behind an env flag.
