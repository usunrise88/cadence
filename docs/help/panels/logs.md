---
title: Logs
summary: The streaming log of the active job — follow, filter by level, search, copy; long logs render only the lines in view.
contexts: [panel:logs]
---

## What this is

A tool panel (bottom row of the Training, Data and Ops workspaces). It shows the log of the **active job**: the job
focused in Queue & GPU, the step opened in Pipeline run, or (with runs) the Run document's current step. The worker
writes each line as `{t, level, msg, fields}`; the panel shows the line number, the time, the level (debug muted, warn
amber, error red; the level is also written out, so colour is never the only sign), the message and its fields as
`key=value`.

The worker redacts before anything leaves it: each secret value it injected into the step and every credential-shaped
token (`cdk_…`, `cst_…`, `cwk_…`, `hf_…`, `Bearer …`) reads `[redacted]` in log lines and fields, progress messages,
output meta and the step's error message — agents read all of them.

It opens with the last 2 000 lines (`jobLogs.list` with `tail=true`) and then streams new lines on `job.{id}.log`
while the panel is visible. A long log renders only the lines in view, so a run's whole log stays fast (up to 20 000
lines in the panel; older lines stay in the job's log file).

**Pin** keeps the current job while the focus moves on; unpin to follow the active job again. The same view is
embedded in Pipeline run for each step.

## Place in the loop

Run → Review. Watch a step while it runs; read why it failed before retrying it.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Level | All levels | Minimum level: "warn and above" keeps warnings and errors (the server's `level` filter) |
| Search | empty | Text the message must contain, case-insensitive (the server's `text` filter, after a short pause while typing) |
| Follow | on | Keeps the newest line in view; scrolling up turns it off, scrolling back to the bottom turns it on |

## Commands

| Command | Notes |
| --- | --- |
| Follow | Toggle; also by scrolling |
| Copy | Copies the shown lines (after the filters) as text: time, level, message, fields |
| Pin | Keep this job |

The panel only reads (`jobLogs.list`, `jobs.get`); it changes nothing.

## Playbooks

- **Why did a step fail?** Open the step in Pipeline run, choose "error and above", and copy the lines into Chat with
  Ctrl/Cmd+I, or ask the agent: it reads the same lines with `jobLogs.list` (`level=error`, `tail=true`).
- **Find the OOM.** Search "out of memory"; the automatic retry at 0.75× batch shows as a new attempt in Pipeline
  run.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Logs) and "Search" (structured log lines).
- docs/review/2026-09-30-phase-2-plan.md "Worker protocol" (`workerLogs.new`: NDJSON lines appended to the job's log
  file, tailed through `job.{id}.log`).
