---
title: Not implemented
summary: The operation is part of the contract but arrives in a later roadmap phase; detail names the phase.
contexts: [error:not-implemented]
---

## What this is

A `501 Not Implemented` problem (RFC 9110 §15.6.2): Cadence names every planned operation in `api/openapi.yaml`
now, so its name, MCP tool and UI command are stable, and answers 501 until the roadmap phase that implements it.
The `detail` says which phase (see `ROADMAP.md`).

## Place in the loop

Nothing ran. The operation exists in the contract, so clients and agents can discover it, but not use it yet.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/not-implemented` |
| `status` | `501` |
| `detail` | `<operation> is planned for roadmap phase <n>` |

## Commands

None. The UI greys planned commands out with the phase as the reason (Cadence recommendation).

## Playbooks

- Agents: do not retry; report that the capability is not available in this release.

## Sources

- RFC 9110, HTTP Semantics, §15.6.2 501 Not Implemented.
- `ROADMAP.md` — which phase delivers which operation.
