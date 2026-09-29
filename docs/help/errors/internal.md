---
title: Internal error
summary: Something failed inside Cadence. The response carries no internals; the server log and trace have the details.
contexts: [error:internal]
---

## What this is

A `500 Internal Server Error` problem (RFC 9110 §15.6.1): an unexpected failure — the database was unreachable, a
bug, a resource exhausted. Cadence never puts internal messages, SQL or stack traces in a response; the details go to
the structured log and the trace, correlated by `trace_id`.

## Place in the loop

A command that fails this way is rolled back entirely: nothing is written, no event is emitted, and nothing is stored
under its `Idempotency-Key`, so retrying with the same key is safe.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/internal` |
| `status` | `500` |
| `instance` | The request path |

## Commands

None. Check `/healthz` (database status and version) and the log under `$CADENCE_LOG_DIR`.

## Playbooks

- Retry once after a short pause with the same `Idempotency-Key`; if it fails again, stop and report.
- Operators: find the request in `cadence.log` by path and time, then its `trace_id` in `traces.jsonl`.

## Sources

- RFC 9110, HTTP Semantics, §15.6.1 500 Internal Server Error.
- RFC 9457, Problem Details for HTTP APIs, §5 security considerations (do not leak implementation details).
