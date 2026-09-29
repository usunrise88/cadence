# S4 — Workspace restore under 300 ms

Status: todo
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
_(fill in: what worked, numbers, surprises, what the spec should change)_
