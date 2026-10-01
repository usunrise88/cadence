---
title: Charts — smoothing, axes, table view
summary: How Cadence charts smooth noisy curves, bin dense series, switch axes and scales, and give every chart a table, a CSV copy, a keyboard cursor and a text summary.
contexts: [shell:charts]
---

## What this is

Every chart in Cadence is one of two kinds. Time series (loss, validation WER, learning rate, throughput, GPU
memory, latency) are line charts that update live. Analytics charts (histograms, bars, heatmaps, scatter plots and
forest plots) show numbers the server has already binned. The chart shows the same numbers an agent gets from the
same API call. Your browser only zooms, smooths and switches scales.

## Place in the loop

Review: read a run's curves while it trains, compare runs, and check a dataset or eval report before you decide.

## Fields and defaults

| Control | Default | Note |
| --- | --- | --- |
| Smoothing | Set by the panel (0 = raw) | An exponential moving average with bias correction, as in TensorBoard. The raw line stays faint underneath, so smoothing never hides a spike. Values from 0 to 0.99; higher is smoother |
| Envelope | Automatic | With more than two points per pixel, each series is binned. A faint band shows the minimum and maximum in each bin; the line shows the mean (of the smoothed values when smoothing is on) |
| X axis | Step | Step, epoch, wall time or GPU-hours, where the data has them. Checkpoint marks (▼) and the best checkpoint (◆) follow the axis |
| Y scale | Linear | Log shows ratios. Values at or below zero cannot be drawn on a log scale; the summary says how many were left out |
| Zoom | Full range | Drag across the plot to zoom into an x range. Double-click or **Reset zoom** returns to the full range. Charts in one panel share the cursor and the zoom |
| Redraw | ≤ 4 per second | Live charts batch new points so the page stays responsive |

Colours: series take eight colours in a fixed order. A series keeps its colour when others are hidden. Each series
also has its own dash pattern, scatter symbol or fill pattern, so colour is never the only way to tell series
apart. Heatmaps use magma or viridis, and deltas use blue ↔ grey ↔ orange, never red–green.

## Commands

- **Table** — shows the chart's numbers as a table: the raw values (never binned), with smoothed values in their own
  columns. Up to 500 rows are shown.
- **Copy CSV** — copies every row as CSV, for a spreadsheet or a note.
- Legend entries — click, or press Enter or Space, to hide or show a series.
- Keyboard, with the chart focused (Tab): ←/→ move the cursor one point (Shift: ten), Page Up/Page Down a tenth of
  the range, Home/End the ends; in heatmaps ↑/↓ move a row; Escape hides the cursor. A screen reader reads the value
  at the cursor.
- The chart's text summary (range, last, minimum and maximum, trend and marks per series) is read with the chart
  and shown above the table.

## Playbooks

None.

## Sources

- TensorBoard's scalar smoothing (debiased exponential moving average).
- WCAG 2.2: 1.4.1 Use of Color, 1.4.11 Non-text Contrast (chart colours are checked at 3:1 in both themes).
- Machado, Oliveira & Fernandes, "A physiologically-based model for simulation of color vision deficiency", IEEE
  TVCG 2009 — the simulation that checks neighbouring series stay apart.
- Crameri, Shephard & Heron, "The misuse of colour in science communication", Nature Communications 2020 — why no
  rainbow or red–green scales.
