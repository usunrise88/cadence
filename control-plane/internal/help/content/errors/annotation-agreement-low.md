---
title: Inter-annotator agreement too low
summary: A golden-set batch's inter-annotator WER is above annotation.max_iaa_wer, or unknown; it does not freeze.
contexts: [error:annotation-agreement-low, entity:annotation_batch, panel:annotation-batch, op:batches.freeze]
---

## What this is

A `409 Conflict` problem of type `annotation-agreement-low`, answered by `batches.freeze` for a batch of purpose
`golden-set` when:

- the **inter-annotator WER** is above `annotation.max_iaa_wer` (5 %), or
- no item has two transcripts yet, so the agreement is unknown.

The inter-annotator WER is measured over the double-annotated items (the `doubleShare` of the sample plus every
flagged item): the word edits between the first and the second transcript, summed, over the words of the first
transcripts. Both are folded first (lower case, punctuation removed), so casing and punctuation never count. It is
recorded before adjudication: adjudicating a disputed item does not lower it. A golden set is the reference every
gate compares against; references that two careful people do not agree on measure the annotators, not the model.

Training batches (purpose `training`) need single annotation only and never answer this problem.

## Place in the loop

Data → evaluation: annotation batch → freeze → golden set.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/annotation-agreement-low` |
| `status` | `409` |
| `detail` | The WER, the number of double items and the target |
| `annotation.max_iaa_wer` | 0.05 — the target (docs/spec/04-blocks.md: ≤ 5 %) |
| `annotation.double_share` | 0.10 — the share of each batch annotated twice |

## Commands

- `batches.get` — `agreement` (pairs, edits, reference words, WER, target) and `canFreeze.reasons`.
- `batchItems.list` with `queue=all` — each double item's `wer` shows where the annotators differ.
- `batches.new` — a new batch under revised guidelines (`guidelines` names the file; the batch pins its commit).

## Playbooks

- Read the double items with the highest `wer` with both annotators: most disagreements are one rule (numbers,
  hesitations, names, code-switching) the guidelines leave open. Write the rule into
  `annotation/guidelines/<name>.md`, then annotate a new batch under the new commit.
- A batch made with `doubleShare: 0` has no pairs: a golden set needs double annotation.

## Sources

- docs/spec/04-blocks.md "Annotation workflow" (inter-annotator WER, target ≤ 5 %).
- docs/spec/08-resolutions.md R27 (guidelines in the project repository, pinned by commit).
- RFC 9457, Problem Details for HTTP APIs.
