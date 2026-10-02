---
title: Diff
summary: Reference against hypothesis for the utterance selected in an Eval report, word by word, with substitutions, deletions and insertions marked by glyph, letter and colour; Hebrew right to left.
contexts: [panel:diff]
---

## What this is

A tool panel (bottom of the Eval workspace). It follows the active Eval report: select a cell there, then an
utterance in its worst-utterance table, and Diff shows that utterance's alignment from the cell's scores — the
reference and the hypothesis after the golden set's scoring normalizer, one column per aligned pair, reference above,
hypothesis below.

| Mark | Meaning |
| --- | --- |
| ≠ S, wavy underline | Substitution: the model wrote another word |
| − D, struck through | Deletion: a reference word the model missed (· below it) |
| + I, underlined | Insertion: a word the model added (· above it) |

Colour is never the only channel: every error carries its glyph and letter, and a screen reader hears "substitution:
reference → hypothesis". Each word is isolated for bidirectional text and the line runs in the golden set's direction,
so Hebrew reads right to left with Latin words and numbers intact.

Above the alignment: the utterance's WER, substitutions, deletions, insertions, reference words, duration and
speaker. **Show texts** gives both texts as lines; **Copy** puts them on the clipboard as `REF:` / `HYP:` lines.
**Previous / Next** (or Alt+↑ / Alt+↓ inside the panel) step through the cell's utterances, worst first.

## Place in the loop

Evaluation · Review: why a cell's WER moved, one utterance at a time.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Utterances | the 50 worst of the cell | Most errors first (`evals.get ?worst=`) |
| Texts | after the scoring normalizer | What the WER was computed on |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Previous / Next | — | Moves the Eval report's selection |
| Copy | — | Reference and hypothesis, two lines |

## Playbooks

- **Why did deletions rise?** Select the cell with the ▲, step through its worst utterances and look for runs of
  − D at the ends of utterances (endpointing) or on short function words (Cadence recommendation).
- **Ask the agent.** Ctrl/Cmd+I attaches the Eval report and the selected utterance (`#cell:<id>/utt:<n>`) to Chat.

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Diff); docs/spec/10-ui-shell.md "Audio view and charts" (words as
  bidi-isolated runs).
- WCAG 2.2 success criterion 1.4.1 (use of colour); Unicode Bidirectional Algorithm (UAX #9), isolates.
