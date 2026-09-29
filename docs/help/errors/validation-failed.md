---
title: Validation failed
summary: The request was readable but a value breaks the operation's schema; errors[] lists each field and what is wrong with it.
contexts: [error:validation-failed]
---

## What this is

A `422 Unprocessable Content` problem (RFC 9110 §15.5.21): the request parsed, but one or more values do not satisfy
the operation's schema in `api/openapi.yaml` — a slug with capitals, a name longer than 120 characters, a missing
required field, an unknown field where the schema forbids extra properties, an `Idempotency-Key` shorter than 8
characters.

The `errors` array names every offending field, so a form can mark all of them at once.

## Place in the loop

Validation runs before the command: nothing was written and no event was emitted. Fix the values and resend; the
same `Idempotency-Key` is fine because nothing was stored under it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/validation-failed` |
| `status` | `422` |
| `errors[].path` | Where the problem is: a JSON pointer into the body (`/slug`) or `query.<name>` / `header.<name>` |
| `errors[].message` | What the schema expected |

## Commands

Every mutation accepts `?dryRun=true`: it validates and reports what would happen without writing. Use it to check a
request before sending it for real.

## Playbooks

- Slugs: 3–40 characters, lowercase letters, digits and dashes, starting with a letter.
- Workspace names: letters, digits, spaces, `_` and `-`, up to 63 characters.
- Agents: read `errors[]`, correct only the listed fields, and resend once; repeated identical failures pause the
  session (Cadence recommendation, see docs/spec/05-agents.md).

## Sources

- RFC 9110, HTTP Semantics, §15.5.21 422 Unprocessable Content.
- RFC 9457, Problem Details for HTTP APIs, §3.2 extension members (`errors`).
- `api/openapi.yaml` — the schemas.
