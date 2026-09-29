# S4 — Workspace restore under 300 ms

Status: done
Box: 1 day

## Goal
Measure fromJSON restore time for 20 panels with visibility-scoped rendering.

## Setup
S1 app with 20 registered stub panels, each subscribing to a fake stream only while visible.

## Steps
1. Save a 20-panel layout. 2. Reload and time from first paint to layout ready. 3. Repeat with renderer onlyWhenVisible vs always. 4. Count active subscriptions after restore.

## Acceptance
Restore under 300 ms; hidden panels hold zero subscriptions.

## Record
Timings per renderer mode; memory after restore.

## Result

**Passed.** Measured 2026-09-29 by `make spikes-measure` (`web/e2e/spikes.spec.ts`): Playwright 1.63, headless Chromium 153 (software compositing), Vite dev build (unminified React), control plane and Postgres 17 on the same shared host. Production builds are faster; the weekly performance job re-measures.

Setup: 20 stub tool panels in 4 groups × 5 tabs, each subscribing to `spike.<id>.*` through `useTopic` (only while
visible). The layout is saved through `workspaces.set`, then the page is reloaded 5 times per renderer mode; the time is
`performance.measure("cadence:restore")` from before `fromJSON` to the next animation frame (the API fetch is excluded
and shown separately in the network log).

| Renderer | Restore median | Max | Panels rendered | Subscriptions after restore | JS heap |
| --- | --- | --- | --- | --- | --- |
| `onlyWhenVisible` | 171 ms | 179 ms | 4 | 4 (one per visible panel) | 86 MB |
| `always` | 175 ms | 180 ms | 20 | 4 | 86 MB |

Hidden panels hold **zero** subscriptions in both modes: `useTopic` follows the panel API's visibility, not mounting.
The stream reconnects with the union of live topics and resumes from the last seq, so a topic change loses nothing.

Surprise: with `content-box` overlays, Dockview 8.3.1 grows each floating window by its border width on every
`fromJSON(toJSON())` round-trip (300 → 302 → 304). The shell pins `.dv-resize-container { box-sizing: border-box }`
and `workspaces.browser.test.ts` checks repeated round-trips.
