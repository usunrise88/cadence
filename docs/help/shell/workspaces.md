---
title: Workspaces
summary: Named layouts per user per project, saved automatically, with Reset to default.
contexts: [shell:workspaces]
---

## What this is

A workspace is a saved arrangement of panels for a task. Five ship by default — Training, Eval, Data, Triage, Ops —
and are built from code, so "Reset to default" always matches the panels that exist. Your changes are saved per
user per project, two seconds after the last change (at once when you switch workspace or leave the page); a
layout that ends where it started is not saved again. Layout saves are preferences, so they do not appear in the
audit log.

**Ops** is the deployment workspace (block 4): Queue & GPU on the left; Chat, Approvals, Storage and Help on the
right; [Shadow](../panels/shadow.md) and Logs at the bottom; the Model document opens in the centre from Library or a
link, and the Shadow panel follows the deployment or Model document you open.

## Place in the loop

Pick the workspace for the block you are working on.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Revision | Saves carry `If-Match`; if another tab saved meanwhile, you are asked: load theirs or keep mine |
| Deep link | `/p/<project>/w/<workspace>?doc=<kind>:<id>&sel=<item>` opens the workspace and the document |
| Missing panels | A panel that no longer exists restores as a placeholder you can remove |

## Commands

- `workspaces.set` — Save workspace (explicit save; saving is automatic otherwise)
- `view.resetWorkspace` — Reset workspace to default (also **Window → Reset layout**)
- Ctrl/Cmd+Shift+1…5 — switch workspace

## Playbooks

None.

## Sources

Cadence recommendation — docs/spec/10-ui-shell.md "Persistence".
