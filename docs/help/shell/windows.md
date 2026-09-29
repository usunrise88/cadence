---
title: Windows — dock, float, pop out
summary: Every window operation has a pointer path, a keyboard path and a palette command.
contexts: [shell:windows]
---

## What this is

A group of tabs is always in one of three places: the grid (docked), a floating window over the grid, or a popout
browser window for a second monitor. Only docked groups can be maximized. Closing a popout returns its group to
the grid.

## Place in the loop

Arrange the work surface for the step you are on; the layout is saved with the workspace.

## Fields and defaults

| Parameter | Default | Note |
| --- | --- | --- |
| Snap distance | 8 px | A floating window's edge or centre within 8 px of a target line snaps to it |
| Release distance | 12 px | A snap holds until you move 12 px away |
| Gutter | 8 px | Lines 8 px inside each container edge |
| Bypass | Ctrl/Cmd | Hold while dragging to place freely |

## Commands

- Float, Pop out, Return to grid, Maximize, Dock left/right/top/bottom — tab context menu, the `⋯` button on every
  group, or the palette
- Arrows move a focused floating window 1 px, Shift+arrows 10 px, Alt+arrows to the next snap line
- Align floating window left/right/top/bottom — palette
- F6 / Shift+F6 — next / previous group; Ctrl/Cmd+Alt+] / [ — next / previous tab; Alt+W — close panel

## Playbooks

None.

## Sources

- Bier & Stone, "Snap-dragging", SIGGRAPH 1986 — snapping to geometric targets.
- WCAG 2.2 SC 2.5.7 Dragging Movements — every drag has a single-pointer path.
