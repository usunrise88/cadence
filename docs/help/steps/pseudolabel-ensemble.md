---
title: pseudolabel_ensemble (step kind)
summary: Combine several pseudo-label members' hypotheses into one text per segment where they agree, and send the rest to the triage queue (core, CPU).
contexts: [step:pseudolabel_ensemble, artifact:segments, artifact:hypotheses, artifact:lid]
---

## What this is

`pseudolabel_ensemble@2` is a runtime-neutral core step kind (CPU, job kind `data`; every worker runtime publishes it).
It reads a `segments` artifact (`cadence.segments/1`: `segments.jsonl` with `uri`, `hash`, `start`, `end`, `channel`,
`role`, `language`, `text?`, `origin?`), the `hypotheses` of `min_members` (2) or more members wired as
`hypotheses.0`, `hypotheses.1`, … (`whisper_transcribe`, `oasis_transcribe`, or `nemotron_transcribe` in a pipeline of
your own), the scoring `normalizer` and,
optionally, a `lid` artifact from `lid_classify`. Hypotheses join segments by the segment's audio hash. With fewer
members wired the step refuses to run: a one-member "ensemble" would agree with itself.

For every segment without a text of its own (the bot channel's TTS script and other source text pass unchanged):

| Step | Rule | Dispute reason when it fails |
| --- | --- | --- |
| 1. Candidates | At least `min_members` members wrote a hypothesis; not every text is empty | `too-few-members`, `no-speech` |
| 2. Agreement | Pairwise WER after the scoring normalizer (word edits over the longer text) ≤ `max_pairwise_wer` for at least `min_agreeing_members` members, each agreeing with another | `disagreement` |
| 3. Language | The `lid` row (when its confidence ≥ `lid_min_confidence`), else the members' own detected languages (Whisper's), agrees with the segment's language by primary subtag or within one of `lid_equivalents` | `lid-mismatch`; `lid-unknown` when there is no evidence and `require_lid` |
| 4. Pick | Among the agreeing members: with `prefer_written_form`, first a text in written form (a capital or sentence punctuation; apostrophes and hyphens do not count); then a member that is itself a vote (`vote: true`, OASIS); then the lowest mean WER to the others | — |

A kept segment gets `origin: pseudo-label`, the picked text as the member wrote it and `confidence` = (agreeing
members / members) × (1 − the pick's mean WER to the other agreeing members). A disputed segment gets
`origin: pseudo-label:disputed`, the best candidate as its text and `dispute: {reason, candidates, lid}`. The control
plane puts every disputed segment in the project's triage queue (`triage.list`); `manifest_filter` drops them by
default, so they never reach training. Every row with language evidence — kept, disputed, or a passed-through row the
`lid` input covers — carries `lid: {language, confidence?, agrees?, source}` (`source`: `lid` or `members`), which
`manifest_filter` compares with the segment's language (`lid_mismatch`). Rows that repeat an audio hash get the same
verdict; `hypotheses` holds one row per audio.

The control plane indexes disputes from this step's output only (its meta counts them): the steps after it carry the
disputed rows on under new segments hashes, and a segment never gets a second open triage item.

Outputs: `segments` (every input row, in order; header `segments.json` with the members, the normalizer and the
step appended to `steps`; `files.jsonl` kept) and
`hypotheses` (one row per labelled segment: `{audio, text, origin, confidence, pick?, reason?, members: [{member,
text, meanWer, …}]}`). Meta: counts of labelled, disputed and passed-through segments and the reasons; the final
metric `disputed_share`.

The picked text keeps the member's style. OASIS writes spoken form without punctuation or capitals, Whisper writes
cased, punctuated text; with `prefer_written_form` (the default) the label is Whisper's text whenever the two agree, so
pseudo-labels keep the training style. Without it OASIS, a vote, wins and the labels come out lowercase.
`text_normalise` applies the language's training normalizer downstream either way.

## Place in the loop

Data. In `pipelines/pseudo-label.yaml`: `sdp_ingest` → `segments_cut` (the untranscribed segments as a dataset) →
the members and `lid_classify` → **`pseudolabel_ensemble`** → `text_normalise` → `manifest_filter` →
`speaker_disjoint_split` → `dataset_freeze` (draft). The template's members are Whisper and OASIS (owner decision
2026-10-04): the base model is not a member — it is the model being taught, and on the stand it added nothing (FLEURS
Serbian: its WER against the references 0.336, Whisper's 0.121; the ensemble with it kept 490 of 2 944 segments, no
better than Whisper alone). With two members OASIS is required, so its step is not optional there: when the service
does not answer, the dry run refuses with `auxiliary-unavailable`. In a pipeline of your own, a member wired as
`hypotheses.<n>` from an optional step may fail or be skipped; the ensemble runs with the others as long as
`min_members` remain, and refuses otherwise.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `max_pairwise_wer` | `pseudolabel.max_pairwise_wer` (0.15) | the phase-4 plan, decision 6 (after NVIDIA Granary) | 0 – 1 |
| `min_agreeing_members` | `pseudolabel.min_agreeing_members` (2) | decision 6 | 2 – 10 |
| `lid_min_confidence` | `pseudolabel.lid_min_confidence` (0.5) | Cadence recommendation | 0 – 1 |
| `lid_equivalents` | `pseudolabel.lid_equivalents` (`[[sr, hr, bs]]`) | Cadence recommendation | — |
| `require_lid` | `pseudolabel.require_lid` (true) | decision 6 | true, false |
| `min_members` | `pseudolabel.min_members` (2) | owner decision 2026-10-04 | 2 – 10 |
| `prefer_written_form` | `pseudolabel.prefer_written_form` (true) | owner decision 2026-10-04 | true, false |

## Commands

`pipelines.run` runs it; `triage.list` shows its disputes; `artifacts.get` its outputs.

## Playbooks

"Adapt a new language" pseudo-labels the corpus where it is untranscribed, previews the result and freezes it.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Decisions taken for phase 4" 6–7 and "Interfaces between streams" (D → X,
  X → D).
- docs/spec/08-resolutions.md R26.
- Koluguri et al., "Granary: Speech Recognition and Translation Dataset in 25 European Languages", 2025 (agreement of
  several models as a pseudo-label filter).
