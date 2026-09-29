---
title: Method not allowed
summary: The path exists but not with this HTTP method — for example DELETE, which Cadence never uses (removal is archive).
contexts: [error:method-not-allowed]
---

## What this is

A `405 Method Not Allowed` problem (RFC 9110 §15.5.6): an operation exists at this path, but not for the method you
used. Cadence maps each verb to one HTTP shape: `GET` reads, `POST` on a collection creates (`new`), `PATCH`
edits, `PUT` sets a pointer or workspace, and every other verb is `POST /{collection}/{id}:{verb}`. There is no
`DELETE`: removal is the reversible `archive` verb.

## Place in the loop

Routing, before any command: nothing ran.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/method-not-allowed` |
| `status` | `405` |

## Commands

Use the operation id (`projects.archive`, `projects.edit`, …) from the palette or the MCP tool list; the generated
clients always pick the right method.

## Playbooks

- To remove a project, call `projects.archive` (`POST /projects/{slug}:archive`).

## Sources

- RFC 9110, HTTP Semantics, §15.5.6 405 Method Not Allowed.
- docs/spec/08-resolutions.md R1 — HTTP shape ⇔ verb.
