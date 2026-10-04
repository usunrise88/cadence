---
title: Annotation — from calls to a golden set
summary: Sample an annotation batch from ingested calls, annotate it (you and invited reviewers, double and blind), adjudicate, meet the agreement target and freeze it into a golden set or training data.
contexts: [guide:annotation, command:batches.new, command:batches.freeze, command:invitations.new, command:annotations.new, command:batchItems.accept, command:triage.accept]
---

## What this is

The annotation workflow turns segments of your own audio into human transcripts (docs/spec/04-blocks.md "Annotation
workflow"). A **golden set** made this way is what every gate of the project compares against, so the workflow is
strict: guidelines pinned by commit, a share annotated twice and blind, disagreements adjudicated, and an agreement
target the batch must meet before it freezes.

1. **Guidelines** — `annotation/guidelines/default.md` is written into every project at bootstrap. Edit it (Recipe
   document) or add your own `annotation/guidelines/<name>.md`; a batch pins the commit it starts from (R27).
2. **Ingest** — stereo calls on a mount through `pipelines/calls-ingest` (`sdp_ingest`: each party its own channel,
   roles from the call's sidecar, the bot's channel labelled from its TTS script, voice activity per channel). It ends
   at the `segments` artifact: the callers have no text yet, so a draft would refuse them. Calls that already have
   transcripts can go through `pipelines/data-ingest` instead; for a golden set, ingest with `eval_only: true` on its
   draft step, or freezing the golden set is refused for leakage.
3. **Sample** — `batches.new` (Annotation batch panel, or an agent) with the frame: the calls-ingest run's segments
   (`segments: b3:…`, the index step's output in `pipelineRuns.get`) or a draft dataset version (`dataset`). One
   role (the caller by default), stratified by campaign, month, duration and confidence, `doubleShare` of the items
   annotated twice. Dry run first.
4. **Invite** (admin) — **Invite a reviewer** on the batch gives a link (shown once) that opens this batch only: its
   items and their audio, played through short-lived links with no download, until the due date or the freeze. No
   password; the reviewer's name labels their annotations.
5. **Annotate** — the Triage panel's **Annotate** mode (or the reviewer's page): the segment within its call, a channel
   switch, the level and speech lanes, the bot's turns, the machine's best guess to correct, tags and entity spans
   (names and addresses entity_score checks later). Done, flag (a second annotator takes it) or skip.
6. **Adjudicate** — the Annotation batch document's queue: two transcripts that differ, side by side; take one, write
   the final text, or exclude the item.
7. **Freeze** — when every item is resolved and the inter-annotator WER is at or under `annotation.max_iaa_wer` (5 %),
   **Freeze (approval)**. The admin's approval writes a draft dataset version of the accepted items, cuts their audio
   into the content store and freezes `golden-set/<name>` with the guidelines commit and the agreement in its card
   (training batches become `dataset/<name>-annotated`). Reviewers lose access at once.

The **triage queue** is the other human input: segments whose pseudo-label members disagreed. Accepting or correcting
one writes a human transcript; rejecting drops the segment.

## Place in the loop

Data → evaluation: ingest → annotate → golden set → `evals.new` → `evals.gate`.

## Fields and defaults

The `annotation.*` section of defaults.yaml: `batch_size` 200, `double_share` 0.10, `max_iaa_wer` 0.05,
`adjudicate_wer` 0, `max_skips` 2, `target_role` caller, `guidelines` default, `due_days` 14, `invitation_max_days`
14, `context_s` 2, strata edges, and the audio tracks' detector (`vad_margin_db`, `vad_floor_db`,
`vad_min_silence_ms`, `bandwidth_floor_db`).

## Commands

- `batches.new`, `batches.get`, `batches.list`, `batches.freeze` (approval).
- `batchItems.list`, `batchItems.get`, `batchItems.accept`; `annotations.new` (people only).
- `invitations.new`, `invitations.list` (admin); `auth.accept` (the link); `credentials.revoke`.
- `triage.list`, `triage.accept`, `triage.correct`, `triage.reject`.
- `tracks.get` — level, speech regions, bandwidth and the end-of-utterance gap of any audio the view shows.

## Playbooks

- Synthetic calls (`calls-synth-sr` on the corpora mount) exercise the whole flow; their golden set is tagged
  `synthetic` and is never the telephone golden set real calls will make.
- Agreement too low: see `annotation-agreement-low`.

## Sources

- docs/spec/04-blocks.md "Annotation workflow"; docs/spec/08-resolutions.md R25, R27, R51, R52.
- docs/review/2026-10-03-phase-4-plan.md, decisions 9 and 10, stream A.
