---
title: Conflict
summary: The change clashes with the current state — a slug that is already taken, or an entity already in the state you asked for.
contexts: [error:conflict]
---

## What this is

A `409 Conflict` problem (RFC 9110 §15.5.10): the request is valid but cannot be applied to the current state of the
target. Examples: `projects.new` with a slug another project uses; `projects.archive` on a project that is already
archived.

It is not a revision race: a stale `If-Match` answers `precondition-failed` (412) instead.

## Place in the loop

The command ran inside its transaction and was rolled back: nothing was written and no event was emitted.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/conflict` |
| `status` | `409` |
| `detail` | Which value or state conflicts |

## Commands

- `projects.get` to see the current state; `projects.list?archived=true` to see every slug in use.
- `?dryRun=true` on `projects.new` reports a taken slug without creating anything.

## Playbooks

- Pick another slug; slugs are permanent addresses (links, topics, repository branches), so Cadence does not reuse
  them (Cadence recommendation).

## Sources

- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict.
- RFC 9457, Problem Details for HTTP APIs.
