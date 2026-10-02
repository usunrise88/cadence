---
title: wer_score (step kind)
summary: The runtime-neutral scorer — WER, CER, substitutions, deletions and insertions, duration buckets and partial stability of a transcribe step's hypotheses against a dataset, after a scoring normalizer.
contexts: [step:wer_score, artifact:scores, artifact:normalizer, artifact:hypotheses]
---

## What this is

`wer_score@1` is a core step kind (CPU, job kind `eval`, shipped in every runtime image). It reads three inputs:

- `hypotheses` — what a family's transcribe step wrote (one JSON line per utterance: `audio`, `text`, optional
  `partials`);
- `data` — the `dataset` those hypotheses decode (a golden set's dataset);
- `normalizer` — the scoring normalizer the golden set is frozen with (a `normalizer` artifact: the registry payload
  as JSON).

It joins each utterance to its hypothesis by the BLAKE3 hash of the audio file and fails with an input error when any
utterance of the dataset has no hypothesis (hypotheses for audio outside the dataset are ignored with a warning). It
writes `scores`, a directory artifact:

- `summary.json` — `schema` `cadence.scores/1`, `scorer` `wer_score@1`, `normalizer` (`versionId`, `hash`),
  `language`, `utterances`, `refWords`, `refChars`, `wer`, `cer`, `werNoPunct`, `sub`, `del`, `ins`, `charErrors`,
  `buckets` (`lo`, `hi`, `utterances`, `refWords`, `wer`; the last bucket's `hi` is null), `stability` when the
  hypotheses carry partials (`partialWords`, `unstableWords`, `ratio`, `editsPerSecond`), `groups` (`call`, `speaker`
  or `utterance`) and `hypotheses` (`family`, `weightsHash`, `decodingHash`, `profile` of the first row). Rates are
  fractions (0.123), not percent.
- `utterances.jsonl` — one row per utterance in dataset order: `audio`, `speaker` (when known), `group`, `durationS`,
  `ref` and `hyp` (normalized), `refWords`, `sub`, `del`, `ins`, `refChars`, `charErrors` and `ops`, the word
  alignment as `[op, ref, hyp]` with `op` one of `=`, `S`, `D`, `I` (the Diff panel reads them).

How the numbers are made:

- **Normalizer.** Both sides go through the same steps, in this order: the Unicode form (`NFC` or `NFKC`); the
  `mappings` in order (literal, case-sensitive); `removeMarks` (decompose, drop nonspacing combining marks such as
  niqqud and accents, recompose); `casefold`; `punctuation: strip` (every Unicode punctuation character, the Hebrew
  maqaf, geresh and gershayim included, becomes a space); whitespace collapsed. A normalizer that strips punctuation
  splits `צה״ל` into two words on both sides; map `״` to nothing first to keep acronyms whole.
- **WER** is (S + D + I) / reference words over the whole set, from a Levenshtein alignment of the normalized words.
  **CER** is the character edit distance over the normalized texts (spaces included) / reference characters.
  **werNoPunct** is the WER with punctuation stripped as well; equal to the WER when the normalizer strips it already.
- **Duration buckets** group utterances by duration (`duration_buckets_s`, the lower bounds; the last is open).
- **group** is the unit the eval's bootstrap resamples (R54): the call id when every utterance has one (`callId`),
  else the speaker when every utterance has one, else the utterance (its audio hash).
- **Partial stability** (Shangguan et al., Interspeech 2020): a word is *shown* when a partial puts it at a position
  the previous partial held empty or held another word; it is *unstable* when the final text does not have it at that
  position. `ratio` is unstable / shown; `editsPerSecond` counts words a later partial changes or drops, per second of
  audio. Both use the normalized texts, so a case or punctuation change is no edit when the normalizer folds it.

## Place in the loop

Evaluate — the `score-<cell>` step of every eval pipeline, after the family's transcribe step; the eval record of the
cell is written from `summary.json`. The conformance suite scores every transcription through it.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `duration_buckets_s` | `eval.duration_buckets_s` ([0, 2, 5, 10, 20] s) | Cadence recommendation | ascending, ≥ 0 |

## Commands

`evals.new` (phase 3) generates the pipeline; `pipelines.run` runs it directly.

## Playbooks

The phase-2 playbook's `evals.new` step (phase 3).

## Sources

- docs/spec/08-resolutions.md R21 (scoring normalizers), R42 (hypotheses), R54 (partial stability, resampling units);
  docs/review/2026-10-02-phase-3-plan.md "The scores artifact".
- Y. Shangguan et al., "Analyzing the Quality and Stability of a Streaming End-to-End On-Device Speech Recognizer",
  Interspeech 2020; G. Myers, "A fast bit-vector algorithm for approximate string matching", JACM 1999.
