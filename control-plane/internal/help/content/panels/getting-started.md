---
title: Getting started
summary: The setup checklist — admin, recordings, first project, first dataset, first run, first gate — each step with its command.
contexts: [panel:getting-started]
---

## What this is

A tool panel (Training workspace, right column) shown until the first gate passes. Each step ticks itself from data
Cadence already has; nothing is recorded separately:

| Step | Done when | Command |
| --- | --- | --- |
| Set up the admin account | You are signed in | first-start screen (`auth.setup`) |
| Attach the call recordings | A mount exists (phase 4) | `mounts.new` |
| Create a project | A project exists | `projects.new` |
| Freeze the first dataset version | A dataset version frozen by a person or an agent (bundled fixtures do not count) | `datasets.freeze` |
| Finish the first training run | A run is done (phase 2) | `runs.new` |
| Pass the first gate | An eval passes the project's gate (phase 3) | `evals.gate` |

Steps whose block has not shipped yet show the phase they arrive in. The command name is the same string as the
palette entry and the agent's MCP tool.

## Place in the loop

Prepare: the path from an empty install to the first gate.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Dismiss | Closes the panel and keeps it out of "Reset to default" in this browser; View → Open Getting started brings it back |

## Commands

Available steps offer their command as a button (for example **New project…**). Everything else runs from the
palette (Ctrl/Cmd+K) or an agent session once its phase lands.

## Playbooks

After the project exists, the Project home offers the "Adapt a new language" playbook with its estimate.

## Sources

- docs/spec/11-ui-panels.md "First run and progressive disclosure" and "Panel catalogue" (Getting started).
