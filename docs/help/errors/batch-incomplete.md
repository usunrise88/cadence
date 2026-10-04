---
title: Annotation batch incomplete
summary: The batch still has items without the transcripts they need, or disputed items waiting for adjudication; it does not freeze yet.
contexts: [error:batch-incomplete, entity:annotation_batch, panel:annotation-batch, op:batches.freeze]
---

## What this is

A `409 Conflict` problem of type `batch-incomplete`, answered by `batches.freeze` (and its dry run) when the batch is
not finished. Every item must be resolved before a batch freezes:

- **agreed** — it has the transcripts it needs and they agree (one transcript, or two within
  `annotation.adjudicate_wer`);
- **adjudicated** — the admin or an adjudicator set its final transcript (`batchItems.accept`);
- **excluded** — skipped by `annotation.max_skips` people, tagged foreign or unintelligible, or excluded at
  adjudication.

Items still **pending** (an annotation missing; a double or flagged item needs two) or **disputed** (two transcripts
that differ) block the freeze. A batch with no accepted item at all is incomplete too.

## Place in the loop

Data → evaluation: annotation batch → freeze → golden set (`goldenSets.freeze`) or a training dataset version.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/batch-incomplete` |
| `status` | `409` |
| `detail` | How many items still need annotations and how many wait for adjudication |

`batches.get` shows the same counts in `progress` and the reasons in `canFreeze.reasons`.

## Commands

- `batchItems.list` with `queue=mine` — the next items to annotate; `queue=adjudication` — the disputed ones.
- `batchItems.accept` — adjudicate an item, or exclude it (`exclude: true`).
- `invitations.new` — invite another reviewer when the batch needs more hands.

## Playbooks

- Many items pending near the due date: invite a second annotator, or exclude what nobody can transcribe.
- A long adjudication queue: read the guidelines with the annotators; a systematic difference (numbers written as
  digits or words, fillers) belongs in the guidelines, then adjudicate.

## Sources

- docs/spec/04-blocks.md "Annotation workflow" (double annotation, adjudication, freeze).
- docs/review/2026-10-03-phase-4-plan.md, stream A.
- RFC 9457, Problem Details for HTTP APIs.
