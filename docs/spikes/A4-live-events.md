# A4 — Outbox → SSE → cache patching with an agent editing

Status: partial
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

**Acceptance met with a scripted agent; the re-run with Claude Code and opencode sessions is the phase-1 gate's.**
Measured 2026-09-30 by `make spikes-measure` (`web/e2e/a4-live-events.spec.ts`, results in
`web/test-results/spikes/A4.json`): Playwright 1.63, headless Chromium, Vite dev server (unminified React, `/api`
proxied), control plane and Postgres 17 in Docker on the same shared host (AMD EPYC 7351P, 32 threads), tracing off.
Three runs.

Setup: a project with a mix over `dataset/fleurs-he-smoke`, opened as a document from the Library. The "agent" is an
MCP client that holds a real agent session token (`cst_`, preset `guardrails-default`, minted on the e2e database by
`control-plane/internal/e2etools/mintagent`, which `web/e2e/stack.sh` builds) and calls `mixes.edit` on `/mcp` with
Claude Code's `_meta.claudecode/toolUseId` — the same path a driver takes, without a model. Each edit lands as a draft
(the default draft policy); the panel shows it inside the dashed outline with the badge `agent · session 4`.

| Measure (30 edits after 3 warm-up) | Run 1 p50 / p95 | Run 2 | Run 3 |
| --- | --- | --- | --- |
| Commit → draft visible (draft `updatedAt`, the commit transaction's `now()`, to the DOM mutation) | 58 / 107 ms | 46 / 71 ms | 41 / 50 ms |
| Agent sends `tools/call` → draft visible | 62 / 111 ms | 50 / 78 ms | 45 / 55 ms |
| Agent receives the result → draft visible | 24 / 36 ms | 24 / 38 ms | 23 / 33 ms |

- **Under 300 ms:** p95 at most 107 ms, max 127 ms. The chain is commit → `NOTIFY` → dispatcher → hub → SSE → Vite
  proxy → one animation-frame batch → cache patch (`entity.mix.{id}`, `draft.*`, `presence.changed`) → React.
- **Resume without loss:** the event stream was dropped in the page while the agent made 5 more edits; the shell
  reconnected from the last seq it had seen (`?after=88`) and showed the last draft 29–47 ms after the reconnect
  (1.5–2.0 s after the drop: the shell waits 2 s before reconnecting a closed stream). All 76 events of the mix's topic
  arrived exactly once (0 missing, 0 duplicates, checked against `events.list`).
- **Conflict, not overwrite:** the person edits rev 2 in the table while another tab saves rev 3; Save answers `412
  precondition-failed`, the panel shows the conflict notice (Reload / Reapply my changes) and the stored mix keeps
  the other change. The same holds when the other change is an agent's accepted draft (`web/e2e/mix.spec.ts`).
- **Badge shows the session:** `agent · session 4` on the draft and, after acceptance, on the mix header (the draft's
  author), each carrying the tool-call id for the Chat click-through.
- **Events per second before coalescing is needed:** 60 edits back to back committed at 49–69 edits/s (97–138
  events/s: `draft.updated` + `presence.changed` each); the panel absorbed them in 43–48 animation-frame batches and
  showed the last draft 19–58 ms after its result reached the agent. The per-frame coalescing already in the stream is enough at the
  rate one agent can write; no further coalescing is needed.

Surprises:
- Chromium's offline emulation (`context.setOffline`) leaves an open event stream connected, so it cannot simulate a
  drop; the harness closes the shell's `EventSource` and fails it (readyState CLOSED), which exercises the shell's
  own reconnect with `?after=`. EventSource's native retry with `Last-Event-ID` (server closes the stream) is
  covered by the control plane's `TestEventStreamFilterAndResume` only.
- The draft's revision, not the mix's, is what the panel waits for: an agent's successive edits leave the mix at rev 1
  and bump the draft (`data-draft-rev`), so the harness measures each edit without a person accepting in between.

Not done here: the edit made by a real Claude Code and opencode session (needs the agent-session stream, wave 2) —
the phase-1 gate re-runs this spec with both drivers; a production build (minified React, SPA served by the control
plane) should only be faster.

What the spec should change: 06 "Real-time model" now describes drafts (own revision, one per author, accept writes
the next revision with `causedBy.draftId`, `412 draft-stale`) and presence (open drafts plus a direct-edit window,
`presence.changed`); record in the decision log that the 300 ms budget is met with the existing per-frame coalescing
and that the shell's 2 s reconnect delay after a closed stream dominates recovery time (lower it if resumes must be
faster than the budget).
