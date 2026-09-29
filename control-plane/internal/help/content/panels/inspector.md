---
title: Inspector
summary: Properties of the current selection; follows the active document unless pinned.
contexts: [panel:inspector]
---

## What this is

A tool panel showing every field of the selected entity — config values, manifest rows, metadata — with a copy
button per value. It follows the active document; the pin button keeps it on one document so you can compare two.

## Place in the loop

Review: check exactly what a document holds before you decide.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Pin | Off by default; when on, the Inspector stays on the pinned document |
| Selection | The item selected inside the document (a checkpoint, an utterance), shown after the name |

## Commands

- Pin / unpin (button in the panel header)
- Copy a value (hover a row)

## Playbooks

None.

## Sources

Cadence recommendation — docs/spec/10-ui-shell.md "Shell concepts" (selection bus).
