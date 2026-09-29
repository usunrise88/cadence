# S1 — Floating-window drag under our control

Status: todo
Box: 2 days

## Goal
Confirm the snapping design: intercept the floating-group drag, write bounds every frame through Dockview, keep tab drag-and-drop for docking.

## Setup
Vite app with pinned dockview-react (MIT), three floating groups, one docked grid. floatingGroupDragHandle: 'tabbar'.

## Steps
1. Capture pointer-down on the empty header space; stopPropagation before Dockview's own drag. 2. Move the group each animation frame through the overlay bounds setter; measure jitter. 3. Verify a tab can still be dragged out of the float into the grid. 4. Try floatingGroupBounds minimum-visible mode; clamp top edge to 0. 5. Repeat with the 'titlebar' handle as fallback.

## Acceptance
60 fps drag with 10 floats; tab docking intact; bounds appear in toJSON(); no Enterprise package installed.

## Record
Dockview version; frame time p95; which internal API moved the group; whether capture on 'tabbar' works or 'titlebar' is required.

## Result
_(fill in: what worked, numbers, surprises, what the spec should change)_
