---
title: Checkpoints
summary: The checkpoints of the active run with validation WER, the top k kept, averaged ones; average the selected, start a new stage from one.
contexts: [panel:checkpoints, command:checkpoints.average, command:evals.new]
---

## What this is

A tool panel (right column of the Training workspace). It follows the active Run document like Metrics. The train
step saves checkpoints as it validates; each appears here with the step it was saved at and its validation WER, best
first. The run's best **k** by validation WER are **kept** (`training.keep_top_k`); the best is marked. An
**averaged** checkpoint (from Average selected) says how many checkpoints it was made from and ranks with the others.
Live on `run.{id}.checkpoints`.

## Place in the loop

Training · Review → Decide: which checkpoint goes on to evaluation, or becomes the start of the next stage.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Validation WER | from the train step | Lower is better; 0–1, shown in % |
| Kept | top `training.keep_top_k` | Checkpoints outside the top k may be pruned later |
| Rank | 1 = best | By validation WER within the run |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Average selected | `checkpoints.average` | Two or more checkpoints of the run; runs the family's average step as a pipeline run, and the result joins the list |
| New stage from here | `runs.stage` | Opens the Run document's stage form with this checkpoint; the peak LR is asked for explicitly |
| Evaluate | `evals.new` | The Run eval form: golden sets the project adopted, latency profiles of the family, decoding (boost lists with a weight), robustness profiles (`augment/*.yaml`), the languages map (prefilled when the run used `target_lang`) and the baseline; the plan (cells cached and to compute, GPU-hours) first, then Start eval opens the Eval report |
| Export | — | Arrives with deployment (phase 5) |
| From step | — | Opens the pipeline run that saved it |

## Playbooks

- **Average the last good checkpoints.** Select the kept checkpoints near the end of training, Average selected; the
  averaged checkpoint often validates better than any single one.
- **Start the next stage.** New stage from here on the best checkpoint, enter a lower peak LR in the Run document,
  Estimate, Start stage.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Checkpoints); docs/spec/04-blocks.md Block 2 (checkpoint averaging).
- Checkpoint averaging as practised in NeMo ASR fine-tuning recipes; the top-k default is a Cadence recommendation.
