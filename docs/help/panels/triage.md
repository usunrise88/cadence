---
title: Triage
summary: The project's disputed pseudo-labels, resolved by a person (accept, correct, reject), and the Annotate mode of annotation batches — audio with a channel switch, a prefilled transcript, tags, keyboard-first.
contexts: [panel:triage, command:triage.list, command:triage.accept, command:triage.correct, command:triage.reject, command:annotations.new, command:guidelines.get]
---

## What this is

The centre of the **Triage** workspace. Two modes:

- **Queue** — the project's open **triage items** (`triage.list`): segments a pseudo-label ensemble could not settle —
  its members disagree, the language identification disagrees with the source's language, or no member heard speech.
  Each item shows why it is disputed, every member's text with its mean WER to the others, and the segment's window of
  its call (two seconds either side, every channel). Disputed segments never reach training until a person resolves
  them:
  - **Accept** (`Enter`) — the best candidate becomes the segment's **human** transcript (`triage.accept`);
  - **Correct** (`E`, type, `Ctrl+Enter`) — your own text becomes the human transcript (`triage.correct`); click a
    member's text to start from it;
  - **Reject** (`Backspace`) — the segment is dropped (`triage.reject`).
  A resolution creates the segment's utterance when no dataset version holds it yet, so the next ingest or batch can
  use the transcript. `↑`/`↓` move through the list.
- **Annotate** — an open **annotation batch** of the project, one item at a time (the same view a reviewer's invitation
  opens): the item's window of the call with a **channel switch** (the target channel first — usually the caller —
  then the other party, then both), the level and voice-activity lanes, the other party's turns around it (the bot's
  TTS script), and a transcript prefilled with the best machine hypothesis. Mark **tags** (noise, crosstalk, foreign,
  unintelligible) and **entity spans** (select words, choose the class, **Mark entity**), then **Done**, **Flag** or
  **Skip**. Your queue is your own: double items reach a second annotator **blind**, and you never see another
  annotator's text. **Guidelines** above the item opens the batch's annotation guidelines (`guidelines.get`): the
  Markdown file at the commit the batch pinned, not the file on main today. A reviewer invited to the batch reads
  them too — and nothing else of the project repository.

Keys of the Annotate mode (no browser-reserved keys): `Ctrl+Enter` done, `Ctrl+Shift+Enter` flag, `Alt+S` skip,
`Alt+P` play/pause, `Alt+R` replay the segment, `Alt+C` switch channel, `Alt+1`…`Alt+4` tags, `Alt+E` mark the
selected words as an entity.

## Place in the loop

Data → evaluation: ingest → pseudo-labels → **triage** → dataset version; ingest → **annotation batch** → golden set.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| Reason | `disagreement`, `lid-mismatch`, `lid-unknown`, `no-speech`, `too-few-members` |
| Mean WER | A member's text against the others' (after the scoring normalizer) |
| Window | The segment plus 2 s of its call each side (triage), or `annotation.context_s` (annotation). Its waveform peaks are stored when the batch is created or the items are indexed (`media.peaks`), so the first view does not compute them |
| 8 kHz origin | Telephone audio: the spectrogram stops at 4 kHz (R52) |

## Commands

- `triage.accept`, `triage.correct`, `triage.reject` — resolve a triage item (people only; agents are refused).
- `annotations.new` — submit your annotation of a batch item; `batches.new` — sample a batch.

## Playbooks

- Many `lid-mismatch` items from one file: the file is probably in another language; reject them and fix the source.
- An item you cannot hear well: switch to the target channel alone (`Alt+C`) before flagging it.

## Sources

- docs/spec/04-blocks.md "Annotation workflow"; docs/spec/11-ui-panels.md "Panel catalogue" (Triage queue).
- docs/review/2026-10-03-phase-4-plan.md, decisions 6 and 10.
