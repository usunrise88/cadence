---
title: Annotation and the telephone golden set
chapter: 14
part: III
phase: 4
status: draft
written-against: 7ab0067
terms: [two-channel call, annotation batch, annotation guidelines, double annotation, adjudication, inter-annotator WER, triage, forced alignment]
prerequisites: [8, 13]
summary: Turn call recordings into a golden set people transcribed — sampled, double-checked and adjudicated — and give its words timings so streaming delay can be measured.
---

# Chapter 14. Annotation and the telephone golden set

Your model in Chapter 9 was judged on FLEURS: people reading sentences into good microphones. It will be deployed
on the telephone, where callers mumble, codecs throw away half the frequencies and a bot talks over them. A golden
set of read speech cannot tell you how that goes. You need a golden set built from calls, and for that, unlike
the pseudo-labels of Chapter 13, you need people: a golden set is the one place where a model's opinion of the truth
is not good enough.

The stand has no real calls yet. So this chapter uses forty synthetic ones we built for the purpose (Chapter 13):
the caller's channel is FLEURS Serbian speech passed through a telephone codec, the bot's channel is a speech
synthesiser reading a script. Everything you do here works the same on real calls; only the name of the result
says `synthetic`, and the book never pretends it is the real telephone golden set.

**In this chapter you will:**

- sample a batch of call segments to transcribe, stratified so it represents the archive;
- write annotation guidelines and invite a reviewer who can listen but not download;
- transcribe in the Triage panel, with double annotation on a sample;
- adjudicate disagreements and read the inter-annotator WER that decides whether the batch may freeze;
- freeze the batch as a golden set and align its words to the audio.

**Before you start.** The `corpora` mount and the ingest of `calls-synth-sr` from Chapter 13, as a draft that is
eval-only (a golden set's audio must never be trainable). About an hour of a person's time for a 40-item batch.

## Calls are two recordings

A *two-channel call* is a recording that keeps each party on its own channel: channel 0 the caller, channel 1 the
bot or the agent. It is the most useful accident in telephone ASR. Speaker separation is free, the bot's channel
labels itself from the script it read, and crosstalk — both parties speaking at once — can be measured instead of
guessed.

The ingest of Chapter 13 already split the calls. On the stand it found 577 segments in the forty calls: 262 on the
bot's channel, each with its script text, and 315 on the caller's, 31 minutes of speech with no text at all. Only the
caller's need people. <!-- data-ingest plr_01a10639-c1d2…, index step, 2026-10-04 -->

```annotated id=audio-two-channels
image: screens/audio-two-channels.png
caption: "Figure 14-1. One synthetic call in the Audio panel: two channels, voice activity, narrowband spectrum"
alt: The Audio panel showing a call with the caller and bot channels, an energy and voice-activity lane, and a spectrogram that stops at 4 kHz.
regions:
  - target: '[data-tour="audio-channels"]'
    label: Channels
    note: Switch between the caller and the bot. The inactive channel is dimmed, not hidden, so overlap stays
      visible.
  - target: '[data-tour="audio-vad"]'
    label: Voice activity
    note: Where each channel has speech. The gap between the bot's last word and the caller's first is the
      end-of-utterance time a voice bot has to wait.
  - target: '[data-tour="audio-spectrogram"]'
    label: Bandwidth
    note: Telephone audio carries nothing above 4 kHz. Cadence estimates the bandwidth and draws the spectrogram only
      as high as the audio goes.
```

<!-- TBD capture: the Audio panel plays media, which serves people only (API keys are refused), so this figure is
     captured from a person's session by the owner -->

## A batch, not a backlog

You will never transcribe the whole archive. You transcribe a sample, and the sample has to look like the
archive: if all your items come from one campaign or one month, the golden set measures that campaign. An
*annotation batch* is a fixed sample of segments, a guidelines version, a deadline and the people who work on it.
Cadence draws it stratified — by campaign, month, duration bucket and the production model's confidence —
proportionally, with at least one item per stratum, and reproducibly from a seed.

What "correct" means has to be written down before anyone types. Do you write numbers as digits? Filler words
("ovaj", "pa")? A word the caller cut off? *Annotation guidelines* are that document. They live in the project's
repository as Markdown (`annotation/guidelines/<name>.md`), so they are versioned like everything else; the batch
pins the commit it was started with, and the golden set's card cites it. Change the guidelines and the next batch
gets the new version, while the frozen set keeps the old one on record.

### Hands-on: open a batch

1. In the Recipe, open `annotation/guidelines/default.md` (bootstrap created it). Add one rule for Serbian, for
   example "write numbers in words", and commit.
2. Open the command palette and run **New annotation batch**: frame the `calls-synth-sr` draft, purpose
   `golden-set`, 40 items, guidelines `default`. Run **Check** first — the dry run shows the strata and the exact
   sample, and writes nothing.
3. Create it. The **Annotation batch** document opens with progress at 0 of 40.

On the stand the batch `calls-synth-sr-1` drew 40 items from the 315 caller segments of the forty calls; four of
them go to a second person. Before anyone typed a word it already knows how long the bot waits after a caller
stops: a median of 2.3 seconds, and 6.1 seconds for the slowest tenth of the turns.
<!-- anb_01a1063f-9408-7cdf-a261-e7cff28bc57a, 2026-10-04 -->

```annotated id=annotation-batch
image: screens/annotation-batch.png
caption: "Figure 14-2. The annotation batch of the synthetic calls, before anyone annotated"
alt: The Annotation batch document: progress, agreement, the strata of the sample and the end-of-utterance gaps.
regions:
  - target: "[data-panel='annotation-batch'] >> text=40 items"
    label: Sample
    note: >
      Forty caller segments drawn from 315, stratified by campaign, month, duration and confidence, reproducible from the seed; a tenth go to a second person, blind.
  - target: "[data-panel='annotation-batch'] >> text=annotation/guidelines/default.md"
    label: Guidelines
    note: >
      The rules of a correct transcript, pinned at the commit the batch started from. The golden set's card will cite that commit.
  - target: "[data-panel='annotation-batch'] >> text=Inter-annotator WER —"
    label: Agreement
    note: >
      The WER between the two transcripts of the doubly annotated items. Above 5 %, the batch does not freeze.
  - target: "[data-panel='annotation-batch'] >> text=End of utterance"
    label: End of utterance
    note: >
      How long the bot waits after the caller stops, from per-channel voice activity: the number a voice bot's turn-taking is tuned on.
  - target: "[data-panel='annotation-batch'] >> text=Invite a reviewer"
    label: Invite a reviewer
    note: >
      A link that lets one person play this batch's audio and write transcripts — no download, no other data — until the batch freezes.
  - target: "[data-panel='annotation-batch'] >> text=Freeze (approval)"
    label: Freeze
    note: >
      Freezing creates a golden set, so it is an approval; Check lists what still blocks it.
    style: warning
```

## Two people, one truth

People disagree, and their disagreements are the noise floor of your golden set. If two careful transcribers
differ on 8 % of the words, a model that improves by 3 % is lost in that noise. So a sample of every batch is
transcribed twice, independently: *double annotation*. Cadence gives 10 % of the items, plus every item someone
flagged as hard, to a second person who cannot see the first transcript.

Where the two disagree, a third person — the admin or a senior reviewer — reads both, listens and decides. That is
*adjudication*. The disagreement that remains measurable is the *inter-annotator WER*: the WER of one person's
transcripts against the other's on the doubly annotated items. It is the batch's quality number. Cadence will not
freeze a golden set from a batch whose inter-annotator WER is above 5 %; the batch answers
`annotation-agreement-low` instead, and the guidelines usually need a sharper rule.

> **Note** — The reviewer who annotates does not need an account with access to anything else. The admin sends an
> invitation link; it opens a session that can play this batch's audio and write transcripts, and nothing more —
> no download link, no other project, no other data. The session ends when the invitation expires or the batch
> freezes.

### Hands-on: annotate

1. As the admin, open the batch and choose **Invite reviewer**. Send the link to a second person (or open it in a
   private window to play both roles).
2. Open the **Triage** panel and switch to **Annotate**. The first item plays; its transcript is prefilled with
   the best hypothesis Cadence has, so you correct instead of typing from nothing.
3. Work with the keyboard: Alt+P plays, Alt+R replays, Alt+C switches channel, Alt+1…4 set the tags (noise,
   crosstalk, foreign, unintelligible), Ctrl+Enter marks done, Ctrl+Shift+Enter flags.
4. When both of you are done, open the batch's adjudication queue and decide each disagreement.

<!-- TBD gate: inter-annotator WER of the batch; Figure 14-2 annotated Triage in Annotate mode -->

> **In the field** — The synthetic calls have a cheat sheet: we kept the FLEURS transcript of every caller turn
> outside the ingested folder. Comparing the batch's adjudicated text with it shows how far careful people land
> from a published reference — <!-- TBD gate: number --> — mostly in punctuation and in how numbers are written,
> exactly what the scoring normalizer of Chapter 8 exists to absorb.

## Triage: the disputes from Chapter 13

The same panel has a second mode. Every segment the pseudo-label ensemble of Chapter 13 could not agree on landed
in a queue, with the reason — the members disagreed, LID said another language, there was no speech. *Triage* is
working through that queue: accept one member's text, correct it, or reject the segment. A corrected segment
becomes a human transcript, and the next freeze of that dataset includes it. Triage is how the disputed tail of a
pseudo-labelled corpus becomes training data instead of waste.

On the stand, Chapter 13's run left 2 454 items in the queue, all for one reason — the members disagreed — each
listed once with both members' texts. That is most of the corpus, and Chapter 13 explains why: a weak member vetoes
what a strong one got right. Triage is the expensive way out; changing the ensemble is the cheap one. Work the queue
when the disputes are few and genuinely hard. <!-- triage_items, project test, 2026-10-04 -->

## Freezing the batch

**Freeze** on the batch is an approval: it creates a golden set, and golden sets judge every model after it. It
checks that every item is complete and the agreement is within the limit, writes the transcripts as a dataset
version, cuts the audio, and freezes `golden-set/calls-synth-sr` with the guidelines commit and the
inter-annotator WER in its card. Items tagged foreign or unintelligible are left out.

<!-- TBD gate: golden set id, size; eval of the Chapter 13 checkpoint on it -->

## When did each word happen?

The last piece makes the telephone golden set useful for streaming. Chapter 10 measured latency to the final
transcript of a whole utterance. A voice bot cares about each word: how long after the caller *said* "dvadeset"
did it appear on screen? To answer that, Cadence must know when each reference word was spoken.

*Forced alignment* finds those times: given the audio and the text that is known to be in it, a CTC model's
frame-by-frame letter probabilities are searched for the most likely path that spells the text, and each word gets
a start and an end. Cadence aligns with omniASR's CTC model — an auxiliary model, Apache-licensed, that covers
Serbian, Croatian and Hebrew — after MMS, the usual choice, was ruled out by its non-commercial licence.

With the reference aligned, the latency scorer reports *emission delay*: for every word, the time from the end of
the word in the audio to the moment the streaming model first showed it and never took it back, summarised as the
median (PR50) and the 90th percentile (PR90). Where a golden set is not aligned, Cadence reports emission delay as
"n/a" with the reason, never a guess.

### Hands-on: align and measure

1. Run `pipelines/align-reference.yaml` on the golden set's dataset (the Golden set document has its hash).
2. When it finishes, the Golden set document shows **Word timings**.
3. Evaluate the Chapter 13 checkpoint on it. The Eval report's **Streaming** section now has an **Emission delay**
   chart per latency profile.

<!-- TBD gate: emission delay PR50/PR90 per profile, eval id -->

## What you learned

- A *two-channel call* puts each party on its own channel, which makes the bot's side self-labelling and overlap
  measurable.
- An *annotation batch* is a stratified, reproducible sample with pinned *annotation guidelines*.
- *Double annotation* of a sample and *adjudication* of the disagreements give the batch its *inter-annotator WER*;
  above 5 %, it does not become a golden set.
- *Triage* turns the pseudo-label ensemble's disputes into human transcripts.
- *Forced alignment* gives reference words their times, so streaming models can be scored on emission delay.

## Exercises

1. Open a second batch with a different seed. How many items do the two batches share, and why does that matter
   for a golden set?
2. Change one guideline (numbers as digits instead of words), annotate ten items twice, and compare the
   inter-annotator WER with the first batch's.
3. Explain why a golden set's audio must come from an eval-only draft, and what the freeze would answer otherwise.
4. Emission delay can be negative. What does a negative delay mean about the model, and is it good?

## Further reading

- Cadence help: `guides/annotation`, `panels/triage`, `panels/annotation-batch`, `steps/align-reference`,
  `steps/latency-score`.
- Kürzinger et al., *CTC-Segmentation of Large Corpora for German End-to-end Speech Recognition* (2020) — forced
  alignment with CTC models.
- Omnilingual ASR (Meta, 2025), the CTC models Cadence aligns with.
