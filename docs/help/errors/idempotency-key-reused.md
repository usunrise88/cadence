---
title: Idempotency key reused
summary: This Idempotency-Key was already used for a different request. Repeat the exact request to get the original result, or use a new key.
contexts: [error:idempotency-key-reused]
---

## What this is

A `422` problem: every command carries an `Idempotency-Key` chosen by the client. The first successful run stores
its response under (actor, key); repeating the same request with the same key replays that response exactly
(same status, `ETag`, body and `Cadence-Command-Id`, plus `Idempotent-Replayed: true`) without running it again.
Sending a *different* request — another operation, path, body or `If-Match` — with a key already used is refused.

Dry runs (`?dryRun=true`) neither look up nor store keys. Failed commands store nothing, so retrying a failed
request with its key runs it again.

## Place in the loop

Checked first in the command's transaction, before any work. Nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/idempotency-key-reused` |
| `status` | `422` |
| `detail` | The operation the key was first used for |
| `Idempotency-Key` | 8–200 characters; a UUID per user intent is the usual choice |

## Commands

None — resend with a fresh key.

## Playbooks

- Generate one key per user intent (per click, per agent tool call), not per HTTP attempt: retries of the same
  intent reuse it, so a network retry never creates two projects.
- Agents: use the tool call id as the key; a replayed answer means the earlier call already succeeded.

## Sources

- IETF draft-ietf-httpapi-idempotency-key-header, The Idempotency-Key HTTP Header Field (422 on key reuse with a
  different payload).
- RFC 9457, Problem Details for HTTP APIs.
