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
| Attach the call recordings | A mount exists (add one in the [Storage](storage.md) panel; the admin approves it) | `mounts.new` |
| Create a project | A project exists | `projects.new` |
| Freeze the first dataset version | A dataset version frozen by a person or an agent (bundled fixtures do not count); the playbooks [Try Cadence or Adapt a new language](../guides/playbooks.md) get you there | `datasets.freeze` |
| Finish the first training run | A run of the open project is done (`runs.list`) | `playbooks.run` |
| Pass the first gate | An eval of the open project passed its gate (`evals.list`) | `evals.gate` |

Steps whose block has not shipped yet show the phase they arrive in. The command name is the same string as the
palette entry and the agent's MCP tool. The gate step's **Run the gate** gates the open project's newest finished
eval; until an eval has finished it says to evaluate a checkpoint first (Checkpoints → Evaluate).

When the first gate passes the checklist retires: it says so, and it no longer opens with the default workspaces
(View → Open Getting started still shows it).

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

After the project exists, the Project home offers the playbooks this phase runs, each with its estimate
(`playbooks.run`; [the guide](../guides/playbooks.md)):

- **Try Cadence** — the first thing to run on a new instance: two hours of FLEURS through the whole loop (import,
  mix, calibrate, a short run, eval, gate) in about one GPU-hour. It ticks "Freeze the first dataset version", "Finish
  the first training run" and usually reaches the gate step.
- **Adapt a new language** — a corpus on a mount (the recordings step) ingested, pseudo-labelled where it has no
  transcripts, frozen and trained on; the admin approves the mount, the source's clearing and the auxiliary models
  on the way.
- **Fine-tune from a dataset version** — when a frozen version already exists.

## Sources

- docs/spec/11-ui-panels.md "First run and progressive disclosure" and "Panel catalogue" (Getting started).
