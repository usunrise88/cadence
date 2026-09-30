---
title: Queue & GPU
summary: The step queue per card — waiting, paused, running and stopping jobs, who holds the training slot — with each card's memory, utilisation and availability windows; reorder, pause, resume and cancel.
contexts: [panel:queue-gpu]
---

## What this is

A tool panel (left column of the Ops workspace). Every pipeline step that runs on a worker is a **step job** in one
queue shared by all projects. The panel shows it per card:

| Part | Meaning |
| --- | --- |
| Card header | Card name, host and card class, the host's health (from the worker's reports), and **Training slot**: the job holding the card's one training slot, or "free" |
| Memory bar | The card's memory: **resident services** (processes outside Cadence, such as the vLLM service on the staging card) and **Cadence**, with the line where Cadence's memory cap starts |
| Charts | Memory (used by every process, and Cadence's share) and utilisation over the last 30 minutes, live; each has a table view with CSV copy |
| Availability | The card's availability windows per job kind ("training: Mon–Fri 20:00–08:00 (next day) Europe/Berlin"; a window without a time zone follows the instance time zone), or "any time" |
| Jobs on the card | Leased jobs: running, or stopping (cancelled, paused, or its window closed) |
| Waiting for a card | Queued jobs in the order the scheduler starts them: priority (higher first), then first in, first out; paused jobs last |

Each job row shows its state, the step kind (`kind@version`), the job kind (training, eval, export, data), the
project, its priority, the attempt, and the progress (running) or the estimate (waiting). Selecting the step kind
focuses the job: **Logs** shows its log and **Pipeline run** its pipeline run.

The worker reports one memory number per card for every process. Cadence's share is therefore an estimate: the
resident share is the last reading taken while no Cadence job held the card; before such a reading, a running job is
assumed to use its whole cap. The chart and the bar say "estimated" when it is one.

The list follows the current project; **All projects** shows every project's jobs (for Ops). The training slot and
the memory split always count every project's jobs, since a card is shared.

Live topics: `queue` (`queue.changed` refreshes the list), `gpu` (`gpu.telemetry`, charts redraw at most four times a
second) and `compute.{id}` (`compute.health`).

## Place in the loop

Run. Training runs, evaluations and data pipelines queue here; this is where a person sees what holds the card and
changes the order.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Priority | 0 (a pipeline run's `priority`, −100 to 100; `jobs.edit` accepts −1000 to 1000) | Higher starts first |
| Memory cap | `compute.hosts[].cards[].memory_cap_gb` in defaults.yaml (24 GB on the staging card) | What one Cadence job may use; edited in Settings → Compute |
| Availability windows | none (any time) | Per job kind: days, opening and closing time, time zone; edited in Settings → Compute |
| Scope | This project | All projects for Ops |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Move up / Move down | `jobs.edit` | Sets the priority one above (or below) the neighbour's; keyboard buttons, no drag needed |
| Pause | `jobs.pause` | A waiting job is held back; a running one saves its training state and returns to the queue |
| Resume | `jobs.resume` | Back to the queue; a training job resumes from its saved state |
| Cancel | `jobs.cancel` | Two clicks (Cancel, then Confirm cancel); a waiting job stops at once, a running one when its step notices |
| Logs / Pipeline run | — | Focus the job and open the panel |
| Edit in Settings | — | Opens Settings → Compute, where the windows and caps are edited (`compute.edit`, admin) |

A queue entry carries no revision: the command reads the job first (`jobs.get`) and sends its revision as If-Match.
A change made in between answers `412 precondition-failed`; try again.

## Playbooks

- **A training job waits behind an evaluation.** Move it up, or pause the evaluation; the training job takes the
  card's slot when it frees.
- **Keep the card free during the day.** In Settings → Compute add a training window "Mon–Fri 20:00–08:00". A job
  starts only when its estimate fits before the window closes; a running training job is paused at the close and
  resumes when the next window opens.
- **Ask the agent what holds the card.** The agent reads the same queue with `queueEntries.list` and can pause or
  reorder with the same commands (reversible, so the default preset allows them).

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Queue & GPU); docs/spec/02-domain-projects-registry.md "Projects"
  (project filter, all projects for Ops).
- docs/spec/08-resolutions.md R19 (availability windows), R53 (charts: uPlot for GPU telemetry, at most four redraws a
  second, table view).
- docs/review/2026-09-30-phase-2-plan.md "Card slots" (one training slot per card; eval and data jobs share the
  remaining memory when they fit).
