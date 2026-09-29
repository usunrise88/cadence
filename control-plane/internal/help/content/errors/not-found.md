---
title: Not found
summary: The project, workspace, help article or path you asked for does not exist, or not under that name.
contexts: [error:not-found]
---

## What this is

A `404 Not Found` problem (RFC 9110 §15.5.5): the target resource does not exist. For projects the address is the
slug (`/projects/{slug}`); for workspaces it is the name inside one user's project; for help it is
`<section>.<slug>`.

## Place in the loop

Reads answer 404 when the entity is missing; commands answer 404 when the entity they act on is missing, before
anything is written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/not-found` |
| `status` | `404` |
| `detail` | What was looked up and by which key |

## Commands

- `projects.list` (with `archived=true` to include archived ones) to find the right slug.
- `workspaces.list` to see the saved workspaces of a project.
- `help.search` to find an article by text or context.

## Playbooks

- A deep link to a workspace that was never saved answers 404; the shell then opens the default layout and the
  first save creates it (`workspaces.set` without `If-Match`).
- Archived projects still resolve by slug; they are only hidden from the default list.

## Sources

- RFC 9110, HTTP Semantics, §15.5.5 404 Not Found.
- RFC 9457, Problem Details for HTTP APIs.
