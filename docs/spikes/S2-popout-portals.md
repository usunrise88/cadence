# S2 — Base UI inside a Dockview popout window

Status: done
Box: 1 day

## Goal
Confirm popovers, tooltips, menus and the command palette render inside a popout browser window.

## Setup
Same app as S1; one group popped out; shadcn on Base UI components inside it.

## Steps
1. Open a popover from a panel in the popout. 2. Pass the panel's ownerDocument as the portal container. 3. Check theme class and stylesheet injection into the popout document. 4. Open the command palette while the popout has focus.

## Acceptance
All four components render in the popout with correct theme; keyboard shortcuts work in both windows.

## Record
What had to be injected; any focus-trap issues; Dockview popout API used.

## Result

**Passed.** Measured 2026-09-29 by `make spikes-measure` (`web/e2e/spikes.spec.ts`): Playwright 1.63, headless Chromium 153 (software compositing), Vite dev build (unminified React), control plane and Postgres 17 on the same shared host. Production builds are faster; the weekly performance job re-measures.

| Record | Value |
| --- | --- |
| Dockview popout API | `api.addPopoutGroup(group, { popoutUrl: "/popout.html", position })`; `onDidAddPopoutGroup` hands over the window; closing the window or "Return to grid" (`group.api.moveTo`) brings the group back |
| Injected | Stylesheets: Dockview copies the opener's `document.styleSheets` itself. Theme: the shell applies the root `light`/`dark` class to each popout and keeps it in sync. Keys: the command registry's keydown listener and focus tracking are installed in each popout document |
| Portals | Base UI portals take `container` from `PortalContainerContext`: each panel frame, tab and group header provides its own document's `body`, re-read when the group moves |
| Verified in the popout (`e2e/shell.spec.ts`) | Tooltip, dropdown menu (window actions), the command palette dialog — all render in the popout document with the theme; Ctrl/Cmd+K pressed in the popout opens the palette there, not in the main window |
| Focus traps | None observed in these tests |

Popovers use the same portal path as tooltips and menus (shadcn `Popover` wired to `usePortalContainer`); they are not
separately exercised in a popout yet. R37's interceptability check across browsers stays open for Firefox and Safari
(Chromium only here).
