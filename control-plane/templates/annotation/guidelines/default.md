# Annotation guidelines — default

<!-- written by Cadence at bootstrap (templates/annotation/guidelines/default.md). Edit freely: every annotation batch
pins the commit of the guidelines it names (batches.new guidelines: default), and the golden set it freezes into cites
that commit. Change the rules here, then annotate a new batch; a batch never changes its guidelines. -->

These rules decide what a reference transcript is. A golden set made from them is what every model of the project is
scored against, so two careful annotators following them must write the same words. Where this file is silent, write
what was said, as it is normally spelled, and flag the item.

## What you hear and what you annotate

- You annotate **one party**: the channel the batch targets (usually the caller). The player starts on that channel;
  switch channels (`Alt+C`) to hear the other party when you need the context.
- The text above the field is the **best guess** of a machine (a pseudo-label or a model's output). It is often wrong.
  Listen first, then correct it; never accept it unheard.
- Transcribe **only speech in the segment** — from its start to its end mark — not what the other party says.

## Words

1. Write what was **said**, in the language's standard spelling and script (the project's language pack decides the
   script: Serbian in Latin, Hebrew without vowel points).
2. **Numbers** as words, exactly as spoken ("dvadeset tri", not "23"); the scorer's normalizer and the language pack's
   ITN compare them either way, but the reference keeps the spoken form.
3. **Names, addresses, product names**: as the speaker said them, in standard spelling. Mark them as entities
   (select the span, `Alt+E`): class `name` for people and organisations, `address` for streets, towns and numbers of an
   address; numbers, dates, phone numbers and amounts use the language pack's classes.
4. **Hesitations and fillers** (eh, mm, hm): leave them out. Repetitions and false starts that are words: keep them.
5. **Cut-off words** at the segment's edges: write the part you hear only when it is clearly a word; otherwise leave
   it out.
6. **Punctuation and capitals** are optional: the agreement and the scorer ignore them.

## Tags

| Tag | Use it when | Effect |
| --- | --- | --- |
| `noise` | Noise, music or a line problem covers part of the speech, but you can transcribe it | Kept |
| `crosstalk` | The other party speaks at the same time | Kept |
| `foreign` | The speech is mostly not in the batch's language | The item is excluded |
| `unintelligible` | You cannot make out what was said | The item is excluded |

## Done, skip, flag

- **Done** (`Ctrl+Enter`): the transcript is right as far as you can tell.
- **Flag** (`Ctrl+Shift+Enter`): you wrote a transcript but are unsure — a second annotator takes the item blind and
  disagreements go to adjudication.
- **Skip** (`Alt+S`): someone else should take it (the language, the domain, the audio). Skips do not count; an item
  skipped by two people is excluded.

## Agreement

A share of every batch (10 %) is annotated twice, blind. The batch freezes as a golden set only when the two
transcripts agree to within 5 % word error rate. When you disagree, the admin (or an adjudicator) decides, and a
recurring disagreement becomes a rule in this file.
