---
title: Library
summary: Browse the Cadence-wide registry — sources, dataset versions, golden sets, models — with a this-project / all filter.
contexts: [panel:library]
---

## What this is

A tool panel (left column) listing immutable registry versions of every kind with the shared list columns: name,
version, status, actor, updated, tags. The filter bar takes free text and qualifiers (`kind:`, `tag:`, `locale:`);
the scope switch shows what this project adopted or everything in the registry.

## Place in the loop

Prepare: pick what a mix, a run or an eval will use. Registry kinds register from phase 1; until then the list is empty.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Scope | This project (adopted versions) or All (default) |
| Kind chips | One per registry kind that exists, with its count |
| Version | `YYYY-MM-DD.<sha>` — immutable |

## Commands

Keys in the list: arrows move, Enter opens, Space previews in Inspector. Adopt, compare and set alias arrive with the
registry (phase 1).

## Playbooks

None yet.

## Sources

Cadence recommendation — docs/spec/11-ui-panels.md "Panel catalogue"; docs/spec/02-domain-projects-registry.md.
