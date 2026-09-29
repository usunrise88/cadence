# S2 — Base UI inside a Dockview popout window

Status: todo
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
_(fill in: what worked, numbers, surprises, what the spec should change)_
