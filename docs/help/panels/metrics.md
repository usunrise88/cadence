---
title: Metrics
summary: Loss, validation WER, LR, gradient norm, throughput and GPU memory of the active run by step, epoch, wall time or GPU-hours, with checkpoint marks, smoothing, log scale, table view and pinned runs overlaid.
contexts: [panel:metrics]
---

## What this is

A tool panel (bottom row of the Training workspace). It follows the active Run document — after you switch to
another document it keeps the last run, and before any it shows the project's newest run. One chart per metric the
train step reports, in this order: training loss, validation loss, validation WER, learning rate (log scale), gradient
norm, throughput, GPU memory, then any other metric by name.

The series come from `metrics.get`: a series longer than 1 000 points arrives binned into equal buckets (mean with min
and max) — the same numbers an agent reads. The browser only smooths, zooms and switches scales. New points arrive on
`run.{id}.metrics` and are appended (`afterStep`), at most one read per second; the charts redraw at most four times a
second. Checkpoints are marked on every chart (▼ checkpoint, ◆ best by validation WER).

Every chart has **Table** (the raw numbers) and **Copy CSV**, a keyboard cursor (arrows, Home/End) read out in a live
region, and a legend whose buttons show and hide a series. Colour is never the only channel: each series also has its
dash.

## Place in the loop

Training · Run → Review. Is the loss falling, does validation WER improve, is the learning rate schedule what you
meant, is the card busy?

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| x | Step | Step, epoch, wall time, or GPU-hours (the run's lease time on GPU cards) |
| Smoothing | 0.6 | EMA weight; the raw line stays faint underneath |
| Log scale | off | For every chart (the learning rate is always log); values ≤ 0 become gaps |
| Pinned runs | none | Runs kept on the charts beside the active one, each in its own colour and dash |

## Commands

| Command | Notes |
| --- | --- |
| Pin | Keeps the active run on the charts; open another run to compare them |
| Unpin | The × on a pinned run |
| Open the run | The run's name in the toolbar |

The panel only reads (`metrics.get`, `runs.get`).

## Playbooks

- **Compare two stages.** Open the first run, Pin, open the second: both curves on every chart.
- **Is the card the bottleneck?** Switch x to GPU-hours and read throughput; a flat line under a falling loss is
  healthy, a sawtooth means waiting for data.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Metrics); docs/spec/08-resolutions.md R53 (uPlot for time series,
  binned data from the API, at most four redraws a second, table view, colour never the only channel).
