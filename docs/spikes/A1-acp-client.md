# A1 — One ACP client for opencode and Claude Code

Status: todo
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
_(fill in: what worked, numbers, surprises, what the spec should change)_
