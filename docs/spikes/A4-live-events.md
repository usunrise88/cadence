# A4 — Outbox → SSE → cache patching with an agent editing

Status: todo
Box: 1 day

## Goal
Prove that a change made by an agent through MCP appears in an open panel within one round trip, attributed.

## Setup
Control plane stub with an outbox table and the /events endpoint; a Mix panel bound to TanStack Query; an agent session from A2.

## Steps
1. Open the Mix panel. 2. Have the agent change the mix through MCP. 3. Measure time from commit to panel update. 4. Kill the SSE connection; reconnect with Last-Event-ID; verify no gap. 5. Edit the same mix from the UI while the agent edits: confirm the 412 conflict path.

## Acceptance
Update visible under 300 ms; resume without loss; conflict surfaces instead of overwrite; badge shows the session.

## Record
Latency p50/p95; events per second the panel can absorb before coalescing is needed.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
