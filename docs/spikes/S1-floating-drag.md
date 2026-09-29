# S1 — Floating-window drag under our control

Status: done
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

**Passed.** Measured 2026-09-29 by `make spikes-measure` (`web/e2e/spikes.spec.ts`): Playwright 1.63, headless Chromium 153 (software compositing), Vite dev build (unminified React), control plane and Postgres 17 on the same shared host. Production builds are faster; the weekly performance job re-measures.

| Record | Value |
| --- | --- |
| Dockview | `dockview-core` / `dockview-react` 8.3.1 (MIT); `dockview-enterprise` not installed |
| Frame time while dragging, 10 floats | p50 16.7 ms, p95 16.7–16.8 ms in each of 3 passes; 0 % dropped frames (control pass without a drag: 0 %); 0 long animation frames |
| API that moves the group | The **public** `transformFloatingGroupDrag` option: Dockview calls it every pointer frame with the proposed box, the container size and the other floats' boxes; `computeSnap` returns the snapped top-left. No internal setter for moves |
| Internals still used | The float's overlay (`api.component.floatingGroups[].overlay`: `setBounds`, `toJSON`, `onDidChange`) for resize snapping, keyboard moves, align commands and focus yielding — covered by `dockview-adapter.browser.test.ts` |
| `tabbar` vs `titlebar` handle | `tabbar` works (the empty tab-bar space moves the float); `titlebar` not needed |
| Tab docking | Intact: a tab dragged out of a float onto the grid docks there (float count 10 → 9) |
| Bounds in `toJSON()` | Yes, all 10 floating groups with positions |

Surprises:
- Dockview 8.3 ships `smartGuides` and `keyboardNavigation` options, but both are served by `dockview-enterprise` modules; setting `keyboardNavigation` without it logs an error. We use neither: our `computeSnap` and our command registry.
- Overlay sizes read back include its 1 px border (300×200 reads 302×202).
- Dockview's drag threshold swallows the first pointer move; tests arm the drag before measuring.
- Playwright tracing (`trace: retain-on-failure`) snapshots the DOM on every mouse action and alone caused 5–12 % dropped frames; measurements run with tracing off.

Spec change (made in `10-ui-shell.md` "Dockview integration", decision log 2026-09-29): moves use the public hook; the
internal setter only for resize and keyboard.
