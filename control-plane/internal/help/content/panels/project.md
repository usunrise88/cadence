---
title: Project
summary: The project home — the five blocks as a checklist, last decisions, open approvals and notes.
contexts: [panel:project]
---

## What this is

The Project document is the home of a project: a unit of work with its own repository, budgets and gates. Its
header shows the project's name, state (active or archived), revision and dates; the Overview lists the five blocks
of the Cadence cycle — data, training, evaluation, deployment, flywheel — with how many entities each holds.

## Place in the loop

Every loop starts here: the next-step bar names what to prepare next. In phase 0 the blocks are empty; each fills as
its phase lands (data in phase 4, training in 2, evaluation in 3, deployment and flywheel in 5).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Slug | The project's id in paths and links (`/p/<slug>/w/<workspace>`); lowercase letters, digits, dashes |
| Revision | Edit counter used for `If-Match`; every rename or archive increments it |
| Created, Updated | Timestamps of the first and the latest command |
| State | `active` or `archived`; archived projects are read-only |

## Commands

- `projects.edit` — Rename project (primary action)
- `projects.archive` — Archive project (inline confirm, reversible)
- `projects.new` — New project (menu bar → project switcher)

## Playbooks

Playbooks ("Adapt a new language", "Fine-tune from a dataset version") appear on the Project home from phase 1.

## Sources

Cadence recommendation — docs/spec/10-ui-shell.md "Lists, compare, drafts, empty states" and "Document anatomy".
