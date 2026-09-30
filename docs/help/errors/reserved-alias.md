---
title: Reserved alias
summary: The alias is reserved — @production moves only with a promotion (deployments.promote), never with aliases.set.
contexts: [error:reserved-alias, field:alias]
---

## What this is

A `409 Conflict` problem of type `reserved-alias`: `aliases.set` was asked to move an alias that Cadence reserves
(docs/spec/08-resolutions.md R8). Two alias names are reserved in every project:

| Alias | How it moves |
| --- | --- |
| `@production` | Only with a promotion: `deployments.promote` records who moved which model version to production and why, behind its approval. `aliases.set` always answers `reserved-alias` |
| `@baseline` | With `aliases.set`, but the command is gated: it answers `202` with an approval id and moves once a person approves |

Every other alias (`@train-current`, or any name you choose) moves freely with `aliases.set`.

## Place in the loop

Aliases point a project at registry versions; pipelines reference aliases and runs record the resolved version.
`@production` is what serves real calls, so it changes only through the Deploy block's promotion, which is signed
and reversible by rollback. Nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/reserved-alias` |
| `status` | `409` |
| `detail` | Which alias and which command moves it |

`aliases.get` and `aliases.list` show each alias's `reserved` field: `free`, `gated` (baseline) or `promotion`
(production).

## Commands

- `deployments.promote` — moves `@production` (Deploy block, roadmap phase 5).
- `aliases.set` on `@baseline` — gated; approve it in the Approvals panel.
- `aliases.list` — the project's aliases and how each may move.

## Playbooks

- Agents: do not retry and do not try another route to `@production`; propose a promotion to a person instead.
- To compare against a new model without promoting it, set `@baseline` (gated) or a free alias of your own.

## Sources

- docs/spec/08-resolutions.md R8 — reserved aliases.
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
