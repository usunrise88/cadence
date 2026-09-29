---
title: Bad request
summary: The server could not read the request — malformed JSON, a parameter of the wrong type, a header it cannot parse, or no API operation at that path.
contexts: [error:bad-request]
---

## What this is

A `400 Bad Request` problem (RFC 9110 §15.5.1): the request never reached the operation because Cadence could not
parse it. Typical causes: a body that is not valid JSON, a query parameter that is not the declared type (for
example `limit=ten`), an `If-Match` that is not a revision, a `Last-Event-ID` that is not a number, a topic pattern
with `*` in the middle, or a body larger than the 4 MiB command limit.

Nothing ran and nothing was written, so the request is safe to fix and resend with the same `Idempotency-Key`.

## Place in the loop

This is a transport-level failure, before any command runs. It differs from `validation-failed` (422), where the
request was readable but its values break the operation's schema.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/bad-request` |
| `status` | `400` |
| `detail` | Which part could not be read and why |
| `instance` | The request path |

## Commands

None. Correct the request and resend it. An agent should compare its call with the operation's schema
(`api/openapi.yaml`, or the MCP tool's input schema) rather than retrying unchanged.

## Playbooks

- Malformed JSON: serialise the body with a JSON encoder, never by string concatenation.
- `If-Match`: send back the `ETag` of your last read verbatim (`"3"`); `3` and `W/"3"` are also accepted.
- Unknown path: check the operation id in the command palette or the MCP tool list; paths live under `/api`.

## Sources

- RFC 9110, HTTP Semantics, §15.5.1 400 Bad Request.
- RFC 9457, Problem Details for HTTP APIs.
- `api/openapi.yaml` — the contract every request is checked against.
