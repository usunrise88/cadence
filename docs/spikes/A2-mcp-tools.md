# A2 — Cadence MCP server driven by both agents

Status: todo
Box: 2 days

## Goal
Prove that tools generated from the OpenAPI contract are usable by both agents to create a mix and dry-run a training job.

## Setup
Control plane with an MCP server (Streamable HTTP) exposing ten generated tools with curated descriptions; a session-scoped token.

## Steps
1. Generate tools from api/openapi.yaml. 2. Start an opencode session with the server configured; ask it to create a mix and dry-run a run. 3. Same with Claude Code. 4. Check the audit log: actor, session id, causedBy tool call id.

## Acceptance
Both agents pick the right tools without hints; every mutation is attributed; dryRun estimates are returned.

## Record
Tool descriptions that needed rewording; token scoping issues; failures.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
