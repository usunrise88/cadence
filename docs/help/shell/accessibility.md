---
title: Accessibility
summary: How the shell meets WCAG 2.2 AA — keyboard and single-pointer paths for every drag, focus, target size, contrast and status messages.
contexts: [shell:accessibility]
---

## What this is

The acceptance table for the desktop shell. WCAG 2.2 level AA (ISO/IEC 40500:2025) is a requirement, not a guideline;
each row names how the shell meets the criterion and the test that keeps it true.

## Place in the loop

Every step: the same shell carries data, training, evaluation, deployment and triage work.

## Fields and defaults

| Criterion | How the shell meets it | Checked by |
| --- | --- | --- |
| 2.5.7 Dragging Movements | Float, pop out, return, maximize and dock left/right/top/bottom are in the tab context menu, the `⋯` menu on every group and the palette | `e2e/shell.spec.ts` (float and dock from menus) |
| 2.1.1 Keyboard | F6 / Shift+F6 groups, Ctrl/Cmd+Alt+] / [ tabs, Alt+W close, arrows / Shift+arrows / Alt+arrows move a focused float, Esc cancels a drag; every command is in the palette | `e2e/shell.spec.ts` (keyboard) |
| 2.4.11 Focus Not Obscured | A floating window covering the focused element fades to 20 % and ignores the pointer until focus leaves | `dockview-adapter.ts` focus yielding |
| 2.4.7 Focus Visible | 2 px accent-9 ring (accent-10 in dark mode) on tabs, sashes and controls | `scripts/contrast.mjs` (3:1 against every background it sits on) |
| 2.5.8 Target Size | Hit areas ≥ 24×24 px, including tab close buttons with 16 px icons and group header buttons | Component sizes (`size-6`) |
| 1.4.3 Contrast | Every text pairing in the Theming table ≥ 4.5:1, light and dark | `scripts/contrast.mjs` in `make test` |
| 1.4.11 Non-text Contrast | Focus rings, active sashes, snap guides, active-tab line ≥ 3:1 | `scripts/contrast.mjs` |
| 4.1.3 Status Messages | Notices, window operations and (from phase 1) agent turns reach a polite live region in the status bar | Live region `role="status"` |

## Commands

- `view.shortcuts` — the keyboard sheet (Ctrl/Cmd+/)
- `view.toggleSnap` — snapping on/off (Ctrl/Cmd+Shift+;)
- `view.toggleTheme` — light / dark

## Playbooks

None.

## Sources

- W3C, Web Content Accessibility Guidelines (WCAG) 2.2, 2023; ISO/IEC 40500:2025.
- Cadence recommendation — docs/spec/10-ui-shell.md "Accessibility and input"; spike S3 for the contrast fixes.
