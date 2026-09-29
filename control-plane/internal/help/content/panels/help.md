---
title: Help
summary: The article for the focused panel, field or error; follows focus unless pinned.
contexts: [panel:help]
---

## What this is

A tool panel that shows the help article for whatever you focus: a panel, a field, an error. Every article has the
same shape — what this is, place in the loop, fields and defaults, commands, playbooks, sources. The same markdown
is what Cadence's agents read, so a person and an agent get one answer.

## Place in the loop

Any step: press `?` with a panel focused, or search with `?` in the palette (Ctrl/Cmd+K).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Search | Full-text search over all articles |
| Pin | Keep the current article instead of following focus |

## Commands

- `view.helpForThis` — Help for this panel (`?`)
- `view.searchHelp` — Search help (palette with `?`)
- `view.shortcuts` — Keyboard shortcuts (Ctrl/Cmd+/)

## Playbooks

"Explain this" (a read-only agent session with the article attached) arrives in phase 1.

## Sources

Cadence recommendation — docs/spec/11-ui-panels.md "Help".
