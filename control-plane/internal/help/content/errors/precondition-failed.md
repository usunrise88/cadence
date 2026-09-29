---
title: Precondition failed
summary: Your If-Match revision is stale — someone (a person or an agent) changed the entity since you read it. currentRev tells you what to rebase on.
contexts: [error:precondition-failed]
---

## What this is

A `412 Precondition Failed` problem (RFC 9110 §13.1.1, §15.5.13): every entity carries a revision (`rev`), returned
as `ETag: "<rev>"`; writes send it back in `If-Match`. When the entity has moved on, the write is refused instead of
silently overwriting the other change. `currentRev` is the revision it is at now.

For `workspaces.set`, sending `If-Match` for a workspace that does not exist yet also answers 412 with
`currentRev: 0`; omit `If-Match` to create it.

## Place in the loop

Concurrency control for every command (docs/spec/06-platform.md, "Concurrency"): a losing writer, person or agent,
gets the current revision and must resolve, never a silent overwrite. Nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/precondition-failed` |
| `status` | `412` |
| `currentRev` | The entity's revision now; re-read it and rebase your change |
| `detail` | Which entity and which revisions |

## Commands

Re-read the entity (`projects.get`, `workspaces.get`), reapply your change to the fresh copy, and send it with the
new `ETag` in `If-Match` and a new `Idempotency-Key`.

## Playbooks

- UI: the shell shows the other change (its actor badge comes from the event) and offers to reapply yours.
- Agents: re-read, re-plan the edit on the current revision, retry once; do not loop on the old revision.
- Workspace saves: the shell's debounced save re-reads and retries once, then asks (Cadence recommendation).

## Sources

- RFC 9110, HTTP Semantics, §8.8.3 ETag, §13.1.1 If-Match, §15.5.13 412 Precondition Failed.
- RFC 9457, Problem Details for HTTP APIs, §3.2 extension members (`currentRev`).
