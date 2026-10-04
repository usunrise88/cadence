---
title: Eval report
summary: One eval — golden sets × latency profiles against the baseline, filling live — with deltas and their 95 % intervals, the gate's verdict and checks, charts, the worst utterances, and model registration.
contexts: [panel:eval, command:evals.new, command:evals.gate, command:models.register]
---

## What this is

The document for one **eval** (`evals.get`): a subject — a checkpoint of this project, a registered model version or
a base model — compared with the **baseline** (the project's `@baseline`, else its default base model) on the
project's golden sets at each latency profile (`80ms`, `160ms`, `1120ms`) and each decoding variant (no boosting, or a
boost list of a language pack).

- **Matrix.** One row per golden set, one column per latency profile. Each cell shows the subject's WER, the
  baseline's, and the **delta** (subject − baseline, in percentage points) with its 95 % interval from a paired
  blockwise bootstrap over the golden set's calls, speakers or utterances. The delta reads by glyph as well as colour:
  **▼** better than the baseline (the interval lies below zero), **▲** worse (above zero), **≈** no significant
  difference. **★** marks the primary profile: the gate reads that column only. **⟲** marks a cell taken from the
  eval-record cache (the same weights, golden set, normalizer and decoding were scored before, in any project). A
  check's state frames the cell: ✓ passed, ✗ failed, ? inconclusive. Replay golden sets are scored at the primary
  profile only. CER and WER without punctuation are under each cell.
- **Charts** (table view and CSV copy on each): the delta heatmap, a forest plot of every delta with its interval,
  substitutions / deletions / insertions as a share of reference words for subject and baseline, WER by
  utterance duration, the **per-utterance WER distribution** (ECDF) of the selected cell against its baseline cell,
  and **entity accuracy** per inverse-normalisation class (numbers, phones, dates…; reported, not gated). The ECDF
  reads the per-utterance rows `evals.get` returns (at most 200 per cell, worst first): for a golden set of up to
  200 utterances that is every utterance; for a larger one the chart says "worst rows only" and shows that tail.
- **Streaming** (follows the selected cell's golden set and the decoding shown): **WER against latency** — one line
  per model across the latency profiles, the primary profile marked ★ with a dashed line, and on the subject the
  baseline's WER plus the delta's 95 % interval; this is the chart to choose the primary profile from. **Latency to
  final** (p50, p95 and max milliseconds after speech end, per profile and model), **emission delay** (PR50 and PR90:
  milliseconds from each matched reference word's aligned end to the first partial that shows it for good; only for
  golden sets whose references are aligned, see [align_reference](../steps/align-reference.md)) and **partial
  stability** (the share of partial words that later changed, and partial edits per second). A cell without a latency
  measurement says why (for example no VAD for the language), and one without emission delay says it is n/a and why
  (no aligned references, or a language the aligner does not cover) — it is never estimated.
- **Robustness**: WER degradation (percentage points, positive is worse) under each augmentation profile against
  the same cell without it, by golden set, augmentation, model and latency profile. Both sections start folded while
  they have nothing to show; the arrow opens them.
- **Worst utterances** of the selected cell (the 50 with most errors), filterable by text or speaker. Enter or a click
  opens the utterance in **Diff**. Eval artifacts are kept by age (`eval.artifact_retention_days`, 30 days after the
  record's last use): once a cell's per-utterance scores were evicted, the table and the per-utterance chart show the
  cell's note (when, and how long artifacts are kept) instead of rows, and Audio answers *Artifact evicted*. The
  matrix, the deltas and the verdict stay; **Run eval…** computes the cell again and brings the rows back.
- **Gate.** The verdict, the `gates.yaml` commit it used and each check with its numbers. **Run the gate** applies the
  project's `gates.yaml` at main to a finished eval; it can be run again after the file changes. The verdict passes
  only against the project's own baseline (`@baseline`, or the default base model: an eval that named another
  baseline gets a failed **Baseline** check) and only when the eval scored every target and replay golden set
  `gates.yaml` names, at the version the project adopted (a missing one is a failed check: run `evals.new` with it).
- A checkpoint's standing is its **latest** gated eval: registration refuses an older passed eval while a later one
  failed.
- **Register model…** publishes the checkpoint as a model version with this eval and a generated model card. It is
  offered only for a checkpoint whose gate passed (`gate-not-passed` otherwise). **Check** shows what would be
  registered; **Register** then asks for an inline confirm, because a registration cannot be undone.

Live on `entity.eval.{id}` and `eval.{id}.progress`: the matrix fills as cells are scored. Details lists the
definition (models and their keys, golden sets, profiles, decoding, significance, estimate); Lineage opens the
Lineage panel on the subject.

## Place in the loop

Evaluation · Run → Review → Decide → Record: an eval runs, its deltas are reviewed, the gate decides, a passed
checkpoint is recorded as a model version.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Golden sets | those `gates.yaml` names, else the project's adopted golden sets | Rows of the matrix |
| Profiles | `eval.matrix_profiles` the model families declare, plus the primary profile | Columns of the matrix |
| Primary profile | `gates.yaml` `primaryProfile`, else `eval.primary_profile` (160 ms) | The column the gate reads (R20) |
| Decoding | no boosting | Boost lists are a decoding axis (R24) |
| Significance | 1 000 bootstrap samples, 95 %, seed 1 | Paired blockwise bootstrap (R54) |
| Rates | fractions, shown in % | Deltas in percentage points (pp) |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Evaluate (Checkpoints) | `evals.new` | The plan first (cells, cached, GPU-hours), then the eval starts and this report opens |
| Run eval… | `evals.new` | The Run eval form for this eval's subject with the default axes (golden sets, profiles, decoding, robustness, languages, baseline); Plan, then Start eval |
| Re-run missing cells | `evals.new` | The same form filled with this eval's axes, planned at once: scored cells come back cached, only the missing ones compute (after a failed step) |
| Edit gates.yaml | `gates.edit` | Opens the Project home's gate editor (the effective gate, Check, Commit to main) |
| Run the gate | `evals.gate` | A finished eval; the verdict and its checks are stored on the eval |
| Register model… | `models.register` | Check, then Register with an inline confirm; agents' registrations wait for approval |
| Open in Diff | — | A row of the worst-utterance table |
| Pipeline run | — | The pipeline run computing the missing cells |

## Playbooks

- **Does the new checkpoint beat the baseline?** Evaluate it from Checkpoints, wait for the matrix, read the ★
  column's deltas and the target check; open the worst utterances in Diff to see what changed.
- **The worst utterances say "evicted".** The cell is older than the retention: Run eval… with the same axes; the
  evicted cells compute again (they are not cached), the others come from the cache.
- **Gate failed on a replay set.** Read the replay check's delta and threshold; compare deletions and insertions in
  the error-type chart before changing the mix's replay share.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Eval report); docs/spec/04-blocks.md Block 3; R20–R24, R43, R53, R54
  in docs/spec/08-resolutions.md.
- Paired bootstrap for ASR significance: Bisani & Ney, "Bootstrap estimates for confidence intervals in ASR
  performance evaluation" (ICASSP 2004).
