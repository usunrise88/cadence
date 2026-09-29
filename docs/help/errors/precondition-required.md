---
title: Precondition required
summary: This write must say which revision it is based on — send If-Match with the ETag of your last read.
contexts: [error:precondition-required]
---

## What this is

A `428 Precondition Required` problem (RFC 6585 §3): the operation changes an existing entity, and Cadence refuses
blind overwrites. Send `If-Match` with the entity's current `ETag`.

`workspaces.set` is the one write where `If-Match` is optional: only when the workspace does not exist yet (the
first save creates it). Saving over an existing workspace without `If-Match` answers 428.

## Place in the loop

Concurrency control, before the command changes anything: nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/precondition-required` |
| `status` | `428` |
| `detail` | Which entity needs a revision |

## Commands

Read the entity (`projects.get`, `workspaces.get`) and use its `ETag` as `If-Match`.

## Playbooks

- Clients should keep the `ETag` of every entity they display and send it with each edit; the generated TypeScript
  client and MCP tools expose it as a parameter.

## Sources

- RFC 6585, Additional HTTP Status Codes, §3 428 Precondition Required.
- RFC 9110, HTTP Semantics, §13.1.1 If-Match.
