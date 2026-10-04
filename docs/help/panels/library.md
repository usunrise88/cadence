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
under a name (`views.set`, per user per project); saving under an existing name replaces its query.

With a registry row highlighted the toolbar offers what the open project can do with it:

- **Adopt…** on a golden set or dataset version opens its document's adopt card, which dry-runs the checks first —
  the leakage check for a golden set, the licence ([licence-forbids-adoption](../errors/licence-forbids-adoption.md))
  and the locale ([locale-mismatch](../errors/locale-mismatch.md); **Check as replay** adopts another language kept to
  measure forgetting). **Adopt** on a model, base model, normalizer or noise bank checks and adopts at once and says
  the outcome beside the button. Only frozen versions are adopted; `data.lock` on main lists every adoption.
- **Set as baseline** on a model or base model (`aliases.set`; moving `@baseline` always waits for an approval, and a
  notice gives its id).

Sources (scope All, no kind chip) list beside the versions with their kind and whether they are cleared for training;
they open as the Source document, and dataset versions as the Dataset version document. Archived versions
(`versions.archive`) are hidden unless the filter asks `state:archived`. Compare arrives later.

The AI menu (the sparks icon next to New mix) asks the agent about the highlighted row, asks it to find what the
filter describes in plain words, or explains the Library in a read-only session.

## Playbooks

- Keep "Frozen Hebrew data" as `kind:dataset_version state:frozen lang:he` and open it from the palette.
- An unknown qualifier shows the list of valid ones under the filter bar ([invalid-query](../errors/invalid-query.md)).

## Sources

Cadence recommendation — docs/spec/11-ui-panels.md "Panel catalogue" and "Search"; docs/spec/02-domain-projects-registry.md.
