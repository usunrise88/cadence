---
title: "Your own recordings: mounts and ingest"
chapter: 13
part: III
phase: 4
status: draft
written-against: 7ab0067
terms: [mount, segment, draft, voice activity detection, pseudo-label, ensemble agreement, language identification, auxiliary model]
prerequisites: [5, 8, 9]
summary: Index recordings where they already live, label the ones nobody transcribed with a vote of models, and freeze the result into a dataset version you can train on.
---

# Chapter 13. Your own recordings: mounts and ingest

Until now every recording you trained on came from a public corpus that someone had already cut into sentences,
transcribed and published. In Chapter 5 you imported FLEURS with one command, and the transcripts came with it.
Real projects rarely start like that. They start with a disk full of audio — call-centre recordings, a folder of
voice messages, a bucket of meetings — and nobody has written down a word of it. This chapter takes the Serbian
project from that situation to a frozen dataset version, without copying a single file before it is needed and
without a person typing a transcript.

**In this chapter you will:**

- point Cadence at a directory of recordings and index them where they are, without copying them;
- read how a long recording becomes short pieces, and why a stereo call becomes two;
- label untranscribed audio with several models and keep only what they agree on;
- preview and freeze the result, and see the leakage check guard your golden set;
- train on the new dataset version through the path you already know from Chapter 7.

**Before you start.** You need the project from Chapters 3–9 with its golden set `golden-set/fleurs-sr-latn-test`,
an admin who can decide approvals (three of them come up in this chapter), and about
half an hour of the card for labelling 10 hours of audio, and a few minutes for a short run.

## Where the audio lives

Copying a corpus into Cadence before you know whether you will use it is expensive in two ways. It costs disk —
call archives run to terabytes — and it costs trust: once a copy exists, nobody is sure which copy is the real
one. So Cadence does the opposite of an import. It reads your audio *where it already lives* and copies only what a
job needs, when the job needs it.

The place where your audio lives is a *mount*: a named root that Cadence can read, such as a directory on the
staging host, a network share the operating system has mounted, an S3-compatible bucket or a Hugging Face
repository pinned to a revision. On the book's stand the mount is called `corpora` and holds two sources: the
FLEURS Serbian audio, and forty synthetic phone calls we built for the next chapter.

> **The ML behind it** — A model learns from examples. The examples' *bytes* matter (the waveform the model hears)
> and their *identity* matters (so you can tell train from test). Cadence keeps the identity as a hash of the
> audio, which is the same wherever the bytes sit. That is why it can index in place: the hash is the dataset's
> memory, the mount is just where the bytes happen to be.

### Hands-on: register the mount

1. Open **Storage** from the command palette (Ctrl+K, type `storage`).
2. Choose **Add mount**. Name it `corpora`, kind `local`, root `/mnt/corpora` (the path inside Cadence's
   containers, not the host's), read-only.
3. Run **Check** first. The dry run reports what Cadence would register, and nothing is written.
4. Submit. The answer is not a mount but an **approval** <!-- apr_01a1050b-d015…, 2026-10-04 -->: adding a mount
   gives every job on the stand a new place to read from, so a person decides. Open **Approvals** as the admin
   and approve it.

```annotated id=storage-mounts
image: screens/storage-mounts.png
caption: "Figure 13-1. The Storage panel after the corpora mount was approved"
alt: The Storage panel listing the corpora mount with its health, the cache use and the pinned dataset versions.
regions:
  - target: "[data-panel='storage'] >> text=read-only"
    label: Mounts
    note: >
      Every place Cadence may read audio from. Adding one was an approval; a mount is never edited in place — a different root is a new mount.
  - target: "[data-panel='storage'] >> text=Healthy"
    label: Health
    note: >
      A worker checks the mount: reachable, free space, a throughput sample. A job never starts on an unhealthy mount, and Check health re-runs the test.
  - target: "[data-panel='storage'] >> text=evicts above 85 %"
    label: Cache
    note: >
      The local copies jobs made. Above 85 % Cadence evicts the least recently used copies that also exist on a mount, down to 70 %. 'evictable 0 B': nothing cached here exists anywhere else yet, so nothing may go.
  - target: "[data-panel='storage'] >> text=200.0 GB"
    label: Quota
    note: >
      What the project's frozen dataset versions occupy against its quota; a freeze that would pass it is refused before it starts.
  - target: "[data-panel='storage'] >> text=Pinned by"
    label: Pinned by
    note: >
      A dataset a golden set is built on, or that a queued run reads, is pinned: the sweep never evicts it.
```

<!-- captured 2026-10-04 from the stand (capture/figures/storage-mounts.json) -->

> **Under the hood** — A copy that a job made is kept on the worker's fast disk and *pinned* while a queued or
> running job, or a promoted model, refers to it. When the cache passes 85 % it is trimmed back to 70 %, oldest use
> first, but only blobs that also exist on a mount can go. Your imported FLEURS audio from Chapter 5 lives nowhere
> else, so it is never evicted.

## From long recordings to segments

A model trains on short pieces: a few seconds to about twenty. A recording of a call or a meeting is minutes long,
mostly silence between turns. So the first step of ingest finds the speech and cuts it into *segments*: stretches
of one recording, given by a start time, an end time and a channel. A segment is what becomes an utterance.

To find speech, ingest uses *voice activity detection* (VAD): it measures the energy of every 20 ms frame on each
channel, estimates the channel's noise floor, and marks the frames clearly above it as speech. Short pauses are
closed, very short bursts are dropped, a little padding is kept around each run, and anything longer than twenty
seconds is split at its quietest point.

Two details matter for phone audio, and Chapter 14 depends on both:

- **A stereo call becomes two tracks.** Call recorders put each party on its own channel. Ingest splits them, so
  the caller and the bot never mix in one segment.
- **The bot's channel labels itself.** The text the bot said is known: it is the script the TTS read. When a
  sidecar file carries that script, ingest takes the bot's segments from it with origin `model:tts-script`. Only
  the caller's speech needs labelling.

A corpus that is already cut into sentences, like FLEURS, should not be cut again: set `segmentation: file`, and
each file becomes one segment as published.

Nothing is copied yet. Ingest writes a list of segments, each with a URI such as
`mount://corpora/calls-synth-sr/2bb966076641/calls/call-0000.wav#t=6.2,15.42&ch=0` and the hash of the audio it
names, decoded the same way every time: 16 kHz, mono, 16-bit. The hash is the utterance's identity, so the leakage
check works before any audio exists in Cadence.

On the FLEURS Serbian train folder, ingest with `segmentation: file` finds 2 944 files and makes 2 944 segments,
10.7 hours, each with both fingerprints. Point it at the whole revision instead and it finds 3 234 — train and dev —
and not one file from `test/`: the default leaves test folders out.
<!-- sdp_ingest@2 run locally against /cadence/corpora/fleurs-sr/70bb2e84b976 at f075ab6, 2026-10-04; replace with
     the gate's pipeline run (plr_…) when it is recorded on the stand -->

## Labels nobody typed

The FLEURS audio on the mount has transcripts in its `.tsv` files, but the ingest step does not read them, and we
left it that way on purpose: it makes the stand's FLEURS Serbian behave like a real untranscribed archive, while
the hidden transcripts let us check how good the labels turned out. Without text there is nothing to train on. You
could pay people to transcribe hundreds of hours. Or you could let models do it.

A transcript written by a model rather than a person is a *pseudo-label*. One model's transcript is not good
enough: it will be confidently wrong where it is weakest, and training on its mistakes teaches your model the same
mistakes. Several different models are wrong in different places. Where they agree, they are very likely right.
Keeping only those segments is *ensemble agreement*, the core of NVIDIA's Granary recipe that Cadence follows.

> **The ML behind it** — Agreement is measured as WER between the members' transcripts after the scoring
> normalizer (Chapter 8). Cadence keeps a segment when at least two members are within 0.15 of each other, and
> picks the member closest to the others. A segment where they disagree is not thrown away silently: it goes to a
> queue a person works through, which Chapter 14 calls *triage*.

Before any of that, a cheap check: is the segment even in the right language? A call archive contains wrong
numbers, a radio in the background, a caller who switches to English. *Language identification* (LID) names the
language of a segment from its sound. A segment LID places outside the expected language family is disputed, not
labelled. For Serbian, Croatian and Bosnian count as one family: they are written and spoken nearly alike, and the
model in Chapter 9 was even trained under the Croatian prompt.

The members of the ensemble are not the model you are training. They are *auxiliary models*: models Cadence uses
to prepare data — label it, identify its language, align it — but never fine-tunes or ships. Each is a registry
entry with a pinned revision and a licence check, because a model's licence can forbid using its output
commercially, and pseudo-labels *are* its output.

| Member (`auxiliary/…`) | What it is | Licence | Why it is in |
| --- | --- | --- | --- |
| the base model (Nemotron) | The model you start from, decoding under `hr-HR` | OpenMDW-1.1 | Fast; already in the pack |
| `whisper-large-v3` | OpenAI Whisper, multilingual | Apache-2.0 | Strong, different architecture; also gives LID |
| `oasis` | The owner's ensemble service (omniASR + a Qwen3 judge) | Apache-2.0 (both) | Optional; a vote of its own |

> **Warning** — Adopting an auxiliary model into a project is an approval for everyone, the admin included, and
> Cadence refuses outright any model whose licence forbids commercial use of its output. The licence check that
> keeps MMS out (CC-BY-NC) is in `docs/review/2026-10-03-phase-4-plan.md`.

### Hands-on: run the pseudo-label pipeline

1. Register the source: **Library → Sources → New source**, name `fleurs-sr-mount`, licence `CC-BY-4.0`, kind
   `public`. A new source is eval-only.
2. Clear it for training. This is the second approval: clearing says "the licence allows training and we may use
   this data", which only a person can say.
3. Adopt `auxiliary/whisper-large-v3` (and, if the service runs, `auxiliary/oasis`) into the project. Third
   approval.
4. In the Recipe, open `pipelines/pseudo-label.yaml` and set the mount path
   (`mount://corpora/fleurs-sr/70bb2e84b976/train`), the source and `segmentation: file`. Ingest skips any `test/`
   folder unless you ask for it: the test split is where golden sets come from. Run **Check**: the plan
   lists every step, warns if the OASIS service does not answer (the step is optional, so the pipeline still
   runs), and estimates the GPU time.
5. Run it and watch the **Pipeline run** panel: ingest, cut, the members one by one, LID, the ensemble, text
   normalisation (Whisper writes Serbian in Cyrillic, so the pipeline transliterates to Latin), filtering,
   the speaker-disjoint split and the draft.

On the stand the run took 29 minutes for the 2 944 FLEURS train files, 10.7 hours of audio. Most of it was Nemotron
decoding at its streaming 80 ms profile; Whisper needed seven minutes. The ensemble kept 490 segments, 1.9 hours, and
disputed the other 2 454, every one for the same reason: the members disagreed.

That is a lot to throw away, so we checked the labels against the transcripts FLEURS publishes and we withheld:

| Text | WER against the FLEURS transcripts | Segments |
| --- | --- | --- |
| The base model alone (Nemotron, `hr-HR` prompt) | 0.336 | 2 944 |
| Whisper large-v3 alone | 0.121 | 2 944 |
| The ensemble's kept pseudo-labels | 0.121 | 490 |
| The disputed segments (the text the ensemble picked) | 0.377 | 2 454 |

<!-- plr_01a10639-2160-7b6b…, 2026-10-04; WER after lower-casing, Cyrillic→Latin and punctuation removal -->

Read the table carefully, because it holds the chapter's main lesson. The kept labels are exactly as good as Whisper
alone — no better. With two members, one strong and one weak, "agreement" mostly selects the utterances the weak
member happens to get right: the easy ones. The weak member here is the very base model you are trying to improve,
so it vetoed five segments in six. An ensemble is only as useful as its members are *independently* good. Two
remedies are in your hands: replace the weak member (the `oasis` service, or Whisper's Hebrew fine-tune for Hebrew),
or keep the weak member out of the vote and use agreement between strong ones.

> **In the field** — The first time the ensemble ran on Serbian, every segment was disputed. Whisper wrote
> Cyrillic, Nemotron wrote Latin, and the two "disagreed" on every word. Nothing was wrong with either model; the
> texts were in different scripts. The fix is the transliteration you met in Chapter 5, applied to each member's
> output before the vote; the stand's run, transliterating, had no such dispute. <!-- found in development
> 2026-10-03 (whisper_transcribe tests); the 2026-10-04 run's disputes were all agreement, none script -->

## The draft, and freezing it

The pipeline ends in a *draft*: a dataset version that exists — you can open it, preview it, search its
utterances — but is not frozen. Its audio is still only on the mount. You cannot train on a draft, export it or
adopt it into another project; Cadence answers `dataset-not-frozen` if you try.

Freezing does three things, in order:

1. **The leakage check.** Every utterance is compared by fingerprint with every golden set. Two fingerprints
   matter: the hash of the segment itself, and the hash of the whole file it was cut from. The second one catches
   golden-set audio that ingest cut differently — trimmed by VAD, say — so its own hash no longer matches. If FLEURS
   test audio had slipped into the folder you ingested, the freeze would stop here with `golden-set-leakage` and
   list the overlap. We tried it on the stand: ingesting the FLEURS `test/` folder on purpose (with the exclusion
   turned off) gives a draft, and freezing it answers:

   ```text
   422 Golden set leakage: dataset/fleurs-sr-test-from-mount … shares 700 utterances with
   golden-set/fleurs-sr-latn-test 2026-10-02.bbf381630549
   ```

   All 700 — the whole golden set. <!-- ver_01a10646-c5fc…, datasets.freeze dry run, 2026-10-04 -->
   > **In the field** — The first version of this check compared only the segments' own hashes. A review before
   > the stand ran it noticed that ingest trims every FLEURS file with VAD, so a test file ingested from the mount
   > would get a new hash and pass the check — and the model would train on its own exam. The fix was the second
   > fingerprint, and a default that leaves `test/` folders out of ingest. A leakage check is only as good as its
   > idea of "the same audio". <!-- phase-4 audit C1, fixed in sdp_ingest@2, 2026-10-04 -->
2. **The cut.** The segments are decoded from the mount and written into Cadence's content store, one WAV per
   utterance. This is the first copy, and the project's storage quota is checked before it starts.
3. **The card.** Quality checks (duration histogram, silence and clipping share, transcript-length outliers) and a
   dataset card in Markdown.

### Hands-on: preview and freeze

1. Open the draft from the pipeline run. The **Dataset version** document shows hours per language and split.
2. Use **Preview** with a filter (for example, at most 25 characters per second) to see what training would read.
3. **Freeze**. Watch the cut's pipeline run; when it ends, the document shows the frozen version, its quality
   checks and its shards.

Freezing the stand's draft took about a minute. The filter had already dropped 15 of the 490 labels (too fast or
too slow for their audio), so the version holds 475 utterances, 1.84 hours: 375 to train on and 100 to validate.
Every quality check passed, and the leakage check covered all 35 golden sets. <!-- ver_01a10657-64e5…, 2026-10-04 -->

```annotated id=dataset-version
image: screens/dataset-version.png
caption: "Figure 13-2. The frozen pseudo-labelled dataset version"
alt: The Dataset version document of dataset/fleurs-sr-pseudo: state frozen, hours and splits, the leakage result, the quality checks and the shards.
regions:
  - target: "[data-panel='dataset-version'] >> text=Frozen 10/4/2026"
    label: Frozen
    note: >
      The freeze copied the audio into the content store once and fixed the content: from here a run may train on it, and nothing changes it.
  - target: "[data-panel='dataset-version'] >> text=Leakage: checked when it was frozen"
    label: Leakage
    note: >
      Checked against all 35 golden sets by audio hash and by the hash of the source file, so a re-cut golden recording is caught too.
  - target: "[data-panel='dataset-version'] >> text=Split rule"
    label: Splits
    note: >
      Speaker-disjoint: no speaker is in two splits. FLEURS publishes no speaker ids, so each file counts as its own speaker here.
  - target: "[data-panel='dataset-version'] >> text=Every check passed."
    label: Quality checks
    note: >
      Silence, clipping and transcripts too long or short for their audio. A failed check does not block the freeze; it tells you what to look at.
  - target: "[data-panel='dataset-version'] >> text=Adopt into project"
    label: Adopt
    note: >
      Next step: adopting writes the version into data.lock, the project's record of which data its pipelines may read.
    style: subtle
```

## Training on it

From here the path is Chapter 7's. Add the frozen version to a mix with replay (Chapter 9 showed why), calibrate,
start a short run, evaluate the best checkpoint against the base model and read the gate's verdict. The playbook
**Adapt a new language** strings every step of this chapter and of Chapters 6–9 together, and stops at each of the
three approvals for a person.

<!-- TBD gate: run id, best validation WER, eval id, verdict; compare with the FLEURS-transcript run of Chapter 7 -->

## What you learned

- A *mount* is a named place Cadence reads audio from; registering one is an approval.
- Ingest cuts recordings into *segments* with *voice activity detection*, splits stereo calls by channel and lets
  the bot's channel label itself from its script; nothing is copied until a freeze.
- A *pseudo-label* is a model's transcript; *ensemble agreement* keeps only segments where different models agree,
  after *language identification* has removed the wrong language. The models doing this are *auxiliary models*,
  each licence-checked before it is adopted.
- A *draft* is a dataset version you can inspect but not train on; freezing runs the leakage check, copies the
  audio once and writes the card.

## Exercises

1. Run the pseudo-label pipeline again with `max_pairwise_wer` at 0.05 instead of 0.15. How many segments are kept,
   and how does the pseudo-label WER against the withheld transcripts change?
2. Put one FLEURS test file into the ingested folder and freeze. Which check stops you, and which golden set does
   it name?
3. Explain why the bot's channel of a call never needs a pseudo-label, and what would go wrong if you trained on
   it anyway.
4. Which of the three members could you drop with the least loss? Use the ensemble's per-member numbers to argue.

## Further reading

- Koluguri et al., *Granary: Speech Recognition and Translation Dataset in 25 European Languages* (NVIDIA, 2025) —
  the pseudo-labelling recipe Cadence follows.
- Cadence help: `steps/sdp-ingest`, `steps/pseudolabel-ensemble`, `guides/auxiliary-models`, `panels/storage`.
