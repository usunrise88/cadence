---
title: Library
summary: Browse the Cadence-wide registry — sources, dataset versions, golden sets, models — with a this-project / all filter, list any search in the query language, and keep saved views.
contexts: [panel:library]
---

## What this is

A tool panel (left column) listing immutable registry versions of every kind with the shared list columns: name,
version, status, actor, updated, tags. With an empty filter it browses the registry: the scope switch shows what this
project adopted or everything, and the kind chips narrow to one kind. Type in the filter bar and it becomes a search
in the [query language](../guides/search.md) — free text plus `kind:`, `status:`, `tag:`, `lang:`, `updated:>…`,
`wer<10` and the rest — listing hits of every kind (projects, registry versions and collections, jobs, approvals, help)
with the kind in the second column. "Open as list" in the palette opens a search here.

## Place in the loop

Prepare: pick what a mix, a run or an eval will use. Any step: keep the lists you come back to as saved views.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Filter | Empty: browse the registry. Text: a search (`projects.search`) over this project and the registry |
| Scope | Browsing: This project (adopted versions) or All (default). Searching: This project (with the registry and help) or All (`scope:all`) |
| Kind chips | Browsing: one per registry kind that exists, with its count. Searching: the parsed qualifiers |
| Views | Your saved searches in this project; the highlighted one is shown. Choosing it again clears the filter |
| Version | `YYYY-MM-DD.<sha>` — immutable (the kind, when searching) |

## Commands

Keys in the list: arrows move, Enter opens, Space previews in Inspector. **Save view** stores the current filter
under a name (`views.set`, per user per project); saving under an existing name replaces its query. Adopt, compare and
set alias arrive with the registry documents.

## Playbooks

- Keep "Frozen Hebrew data" as `kind:dataset_version state:frozen lang:he` and open it from the palette.
- An unknown qualifier shows the list of valid ones under the filter bar ([invalid-query](../errors/invalid-query.md)).

## Sources

Cadence recommendation — docs/spec/11-ui-panels.md "Panel catalogue" and "Search"; docs/spec/02-domain-projects-registry.md.
