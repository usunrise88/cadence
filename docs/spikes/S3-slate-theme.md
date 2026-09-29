# S3 — Slate + Indigo theme for Dockview

Status: done
Box: 1 day

## Goal
Map Radix Slate and Indigo tokens onto every Dockview surface, light and dark, including drop overlays and sashes.

## Setup
S1 app; @radix-ui/colors CSS; a custom Dockview theme object.

## Steps
1. Build theme.css mapping --dv-* variables to Slate steps per the Theming table. 2. Drag panels to see drop overlays; hover sashes; float a group. 3. Switch dark mode. 4. Run the contrast script on the pairs from the Theming table.

## Acceptance
No unthemed surface in either mode; contrast script passes 4.5:1 text and 3:1 non-text.

## Record
Full list of --dv-* variables the Dockview version exposes; pairs that failed and their fix.

## Result

**Passed after three token changes.** Measured 2026-09-29 by `make spikes-measure` (`web/e2e/spikes.spec.ts`): Playwright 1.63, headless Chromium 153 (software compositing), Vite dev build (unminified React), control plane and Postgres 17 on the same shared host. Production builds are faster; the weekly performance job re-measures.

Coverage: `e2e/spikes.spec.ts` resolves every Radix and Cadence token to a colour, then walks all 311 elements under
the Dockview root — docked groups, a floating window, tabs, sashes and a live drop overlay mid-drag — in light and
dark. Unthemed backgrounds, borders or text: **none** in either mode (translucent values mid-transition, e.g. the
fading scrollbar, match their token by RGB).

`--dv-*` variables Dockview 8.3.1 exposes and we map (`web/src/styles/theme.css`): group view, tabs-and-actions
container (background, height, font size), tab colours for the 4 active/inactive × visible/hidden states, tab divider,
separator border, paneview header border and active outline, sash and active sash (+ transition), drag-over background,
border colour and border, edge dock indicator, dnd compass (4), icon hover, floating box shadow, border, group border,
title bar (background, border, height), dragging opacity, context menu (background, colour), smart guides (2),
scrollbar (2), overlay z-index, transition duration, tab font size, margins, radii (4), close icon size, spacing.

Contrast (`web/scripts/contrast.mjs`, 25 pairings × 2 modes, now in CI) — the Theming table's own steps failed:

| Pair | Table step | Ratio | Fix |
| --- | --- | --- | --- |
| Focus ring on slate-1 / slate-2 (light) | accent-8 | 2.36 / 2.30 (need 3) | accent-9 light, accent-10 dark (dark accent-9 on slate-4 is 2.77) |
| Active sash on slate-1 (light) | slate-8 | 1.86 (need 3) | slate-9 |
| Warning text on slate-1 (light) | amber-11 | 4.498 (need 4.5) | amber-12 |

Also found: shadcn's `dark:bg-input/30` classes produce colours outside the token set; removed from inputs and
buttons. Status dots (step 9) are always paired with a text label, so they are not the sole carrier of meaning (SC
1.4.11 does not require 3:1 for them); the label text is checked.

Spec change (made in `10-ui-shell.md` Theming and Accessibility tables, decision log 2026-09-29).
