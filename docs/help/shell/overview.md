---
title: The Cadence desktop
summary: Panels, documents and tools, workspaces, commands and the palette — how the shell is organised.
contexts: [shell:overview]
---

## What this is

Cadence is a desktop in the browser: documents (a project, a run, an eval report) open as tabs in the centre; tool
panels (Library, Inspector, Help) sit at the edges and follow the active document. Any group can float over the
layout, pop out to another monitor, or be maximized. A named layout is a workspace — Training, Eval, Data, Triage,
Ops — saved per user per project.

## Place in the loop

Every entity is worked through the same loop — prepare, check, run, review, decide, record — shown in each document
header; the next-step bar offers the next step.

## Fields and defaults

| Setting | Default |
| --- | --- |
| Theme | Follows the system; View → Toggle dark mode |
| Snapping of floating windows | On; hold Ctrl/Cmd while dragging to bypass |
| Workspace save | Automatic, 2 s after the last layout change, and on workspace switch or page hide |

## Commands

- Ctrl/Cmd+K — the palette: type to search, `>` commands, `?` help, `@` projects
- Ctrl/Cmd+Shift+1…5 — switch workspace
- Ctrl/Cmd+/ — keyboard shortcuts
- See [Windows](windows.md) and [Keyboard](keyboard.md)

## Playbooks

Playbooks arrive with the agent loop (phase 1).

## Sources

Cadence recommendation — docs/spec/10-ui-shell.md.
