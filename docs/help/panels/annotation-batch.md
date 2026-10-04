---
title: Annotation batch
summary: One annotation batch — its sample and strata, progress, inter-annotator agreement, reviewers and invitations, the adjudication queue and the freeze into a golden set or training data (approval).
contexts: [panel:annotation-batch, entity:annotation_batch, command:batches.new, command:batches.get, command:batches.freeze, command:invitations.new, command:batchItems.accept, command:guidelines.get]
---

## What this is

A document for one **annotation batch** (`batches.get`): a fixed sample of segments people transcribe by hand.

- **Sample** — drawn by `batches.new` from a **frame** (a dataset version's segments, usually a draft from
  `pipelines/data-ingest`, or a segments artifact) over one channel role (the **caller** by default: the bot's channel
  labels itself from its TTS script), stratified by campaign, month, duration bucket and confidence. The Details tab
  lists the strata: how many segments each holds in the frame and how many were sampled.
- **Guidelines** — `annotation/guidelines/<name>.md` in the project repository at the commit the batch pinned (R27);
  **Guidelines** under the details opens its text (`guidelines.get`, Markdown, sanitised), as an annotator reads it.
- **Progress** — items by state: **pending** (an annotation missing; double and flagged items need two), **agreed**,
  **disputed** (two transcripts differ), **adjudicated**, **excluded** (skipped by `annotation.max_skips` people, tagged
  foreign or unintelligible, or excluded at adjudication).
- **Agreement** — the inter-annotator WER over the double items (`doubleShare`, 10 % by default, plus flagged ones),
  against `annotation.max_iaa_wer` (5 %); and the end-of-utterance gaps — the target's last speech to the other
  party's next speech, from per-channel voice activity.
- **Reviewers** — who annotated, and **Invite a reviewer** (admin): a name and a role (annotator, or adjudicator who
  may also decide disputed items) give a link, shown once, that opens this batch only — its items and their audio,
  played through short-lived links with no download — until the due date (14 days at most) or the freeze.
- **Adjudication** — each disputed item with its audio and both transcripts side by side: take one, write the final
  text, or exclude the item (`batchItems.accept`).
- **Freeze** — **Check** answers what would freeze; **Freeze (approval)** asks the admin. The approved freeze writes
  the accepted items as a draft dataset version (human transcripts, entity spans), cuts it into the content store and,
  for a golden-set batch, freezes `golden-set/<name>` through `goldenSets.freeze`, its card citing the guidelines
  commit and the inter-annotator WER. A training batch becomes `dataset/<name>-annotated`. Reviewers lose access when
  the freeze starts.

## Place in the loop

Data → evaluation: ingest → **annotate** → golden set → evals and gates.

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `annotation.batch_size` | 200 | Items a batch samples |
| `annotation.double_share` | 0.10 | Share annotated twice, blind |
| `annotation.max_iaa_wer` | 0.05 | A golden-set batch freezes only at or under it |
| `annotation.adjudicate_wer` | 0 | Two transcripts farther apart go to adjudication |
| `annotation.context_s` | 2 | Seconds of the call played around the segment |
| `annotation.invitation_max_days` | 14 | Longest an invitation lasts |

## Commands

- `batches.new` (dry run first), `batches.get`, `batches.list`, `batches.freeze` (approval).
- `invitations.new`, `invitations.list` (admin); `credentials.revoke` ends an invitation.
- `batchItems.list`, `batchItems.get`, `batchItems.accept`; `annotations.new` (the Triage panel's Annotate mode).

## Playbooks

- Agreement above the target: read the double items with the highest WER with both annotators, write the missing rule
  into the guidelines, and annotate a new batch under the new commit.
- A golden set from calls: ingest the calls with `eval_only: true`, or the freeze is refused for leakage — the same
  audio would sit in a trainable dataset version.

## Sources

- docs/spec/04-blocks.md "Annotation workflow"; docs/spec/08-resolutions.md R27.
- docs/review/2026-10-03-phase-4-plan.md, decision 10 and stream A.
