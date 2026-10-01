---
title: dataset_import (step kind)
summary: Imports a corpus (NeMo manifest, Hugging Face dataset such as FLEURS, or a folder with metadata.csv) as a dataset artifact that registers a source, utterances, transcripts and a frozen dataset version.
contexts: [step:dataset_import, artifact:dataset]
---

## What this is

`dataset_import@3` is a runtime-neutral core step kind (version 3 adds `transliterate`; it ships in every runtime image, runs on the CPU, job kind
`data`). It reads a corpus, converts every clip to 16-bit PCM WAV, mono, at `sample_rate`, and writes one `dataset`
artifact. When the step finishes, the control plane's `dataset` output hook — in the same transaction that marks the
step done — creates or reuses the **Source**, inserts **Utterances** by the BLAKE3 hash of their audio (audio already
in the registry is reused, never duplicated), **Transcripts** with origin `human`, an `audio-b3` fingerprint per
utterance, and registers a **frozen dataset version** in `dataset/<name>` (docs/spec/08-resolutions.md R18).

A new source is **eval-only** until a person clears it for training (`sources.edit`, `trainingCleared: true`): its
versions can be evaluated but mixes and runs refuse them ([eval-only-dataset](../errors/eval-only-dataset.md)).

## Place in the loop

Data → Train. `pipelines/import.yaml` runs it once per corpus; `pipelines/replay-base.yaml` imports the replay corpus
and the replay golden sets (R17). The Data phase (phase 4) replaces it with the full ingest path.

## The dataset artifact

A directory artifact (a content-store manifest `{files: [{path, hash, size}]}`); every file is its own blob.

| File | Content |
| --- | --- |
| `dataset.json` | `format: cadence.dataset/1`, `name?` (collection without `dataset/`), `source: {name, licence, kind, languages, url?, revision?, subset?}`, `splitRule`, `counts: {train, validation, test}`, `hours`, `tags?`, `evalOnly?` |
| `manifest.jsonl` | One line per utterance: `audio` (path inside the artifact), `duration` (s), `sampleRate`, `channels`, `language`, `speaker?`, `text`, `origin` (`human`, `pseudo-label`, `model:<id>`), `confidence?`, `split`, `fingerprints?` |
| `audio/<h2>/<h>.wav` | The audio (h = sha256 of the file); an utterance's content hash is its blob's BLAKE3 hash |

The dataset version's fingerprint is the sha256 of the sorted `[audio hash, split, text]` tuples: re-importing the same
content into the same collection returns the version already there. Its payload carries hours, counts per split and
language, the source ids, the licence, the artifact (`artifact.hash`, which training steps read) and the lineage
(pipeline run, step). A version with `evalOnly` or from a source not cleared is tagged `eval-only`.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `format` | `hf-dataset` | Cadence recommendation | `nemo-manifest`, `hf-dataset`, `folder-csv` |
| `name` | `""` (source name) | Cadence recommendation | collection name without `dataset/` |
| `source_name` | required | R18 | 2–100 lowercase letters, digits, `.`, `_`, `-` |
| `source_kind` | `public` | spec 02 | `public`, `production`, `synthetic` |
| `licence` | required | R18 | the corpus card's licence, e.g. `CC-BY-4.0` |
| `source_url` | `""` (`hf://datasets/<repo>`) | Cadence recommendation | ≤ 500 characters |
| `locale` | `""` | Cadence recommendation | the language of every utterance unless the input names one |
| `path` | `""` | Cadence recommendation | NeMo manifest file, or folder with `metadata.csv` (columns `file`, `text`, `speaker?`, `language?`, `split?`) |
| `hf_repo` | `google/fleurs` | R17 | Hugging Face dataset |
| `hf_config` / `hf_configs` | `""` / `{}` | FLEURS card | one configuration, or configuration → locale for a multi-language version |
| `hf_split` | `train` | Cadence recommendation | the source split to read |
| `hf_revision` | `""` | Cadence recommendation | pin it for reproducible imports |
| `text_field` | `""` (auto) | Cadence recommendation | auto picks `raw_transcription`, `sentence`, `text`, `transcription` |
| `max_hours` | `0` (no cap) — `data.max_hours` | Cadence recommendation | per language, in source order; with a cap an `hf-dataset` is streamed and a full language is read no further |
| `max_utterances` | `0` (no cap) — `data.max_utterances` | Cadence recommendation | per language, in source order |
| `split_rule` | `speaker-disjoint` | spec 03 starter pipeline | `speaker-disjoint`, `source`, `all-train`, `all-validation`, `all-test` |
| `validation_share` | `0.02` — `data.validation_share` | Cadence recommendation | 0–0.5 |
| `min_validation_utterances` | `100` — `data.min_validation_utterances` | Gate rehearsal 2026-10-01 (19 validation clips from a 3 h import) | 0–100000; a speaker-disjoint split with fewer validation utterances moves more whole speakers (or transcripts) over, never past half the import |
| `sample_rate` | `16000` — `data.sample_rate` | Nemotron 3.5 model card | 8000–48000 Hz |
| `text_normalisation` | `false` — `data.text_normalisation` | Nemotron 3.5 model card (cased, punctuated) | on: NFKC, case-folded, no punctuation |
| `transliterate` | `""` (keep the script) | Cadence recommendation | `""`, `sr-Cyrl-Latn` (Serbian Cyrillic → Gaj Latin, before normalisation) |
| `eval_only` | `false` | R17, R18 | golden and replay test sets |
| `tags` | `[]` | Cadence recommendation | ≤ 20 tags |
| `purpose` | `speech` | spec 02 entity Noise bank | `speech`; `noise`: background noise clips registered as a noise bank |

**Noise banks.** With `purpose: noise` the step keeps clips without transcripts (language `und` unless given, all
`train`, tag `noise-bank`, header `purpose: noise`), and a `folder-csv` folder needs no `metadata.csv`: every audio
file under `path` is read. The hook then registers the Source and a frozen version in `noise-bank/<name>` (registry
kind `noise_bank`, payload: clips, hours, licence, source, artifact, lineage) — no utterances or transcripts, and a
noise bank never enters a mix. `pipelines/noise-bank.yaml` imports the noise part of MUSAN (CC BY 4.0).

Speaker-disjoint split: a speaker is wholly in train or in validation, chosen by a stable hash of the speaker id;
without speaker ids (FLEURS) the transcript is the group, so readings of the same sentence stay together. Clips with
empty text, no language, or audio already imported in the same run are skipped and counted in the step's log.

## Commands

- `pipelines.run` with `pipelines/import` (phase 2, stream P) — the only way to run it.
- `sources.list|get|edit|archive`, `utterances.list|get`, `datasets.list|get` — what an import registered.

## Playbooks

- Import FLEURS Hebrew: `format: hf-dataset`, `hf_config: he_il`, `locale: he-IL`, `source_name: fleurs`,
  `licence: CC-BY-4.0`, `hf_revision` pinned; then ask a person to clear `fleurs` for training.
- Golden or replay test sets: `split_rule: all-test`, `eval_only: true`, `tags: [golden]`.
- A language the base model knows only in another script (Serbian FLEURS is Cyrillic; Nemotron 3.5's tokenizer lacks
  six of its letters): `transliterate: sr-Cyrl-Latn`, then train with a close language the model knows
  (`target_lang: hr-HR`). `runs.new` refuses a mix whose language the base model does not know before any GPU time.

## Sources

- docs/spec/08-resolutions.md R17 (replay), R18 (minimal data entities); docs/spec/02-domain-projects-registry.md.
- A. Conneau et al., "FLEURS: Few-shot Learning Evaluation of Universal Representations of Speech", 2022 (CC-BY-4.0).
- nvidia/nemotron-3.5-asr-streaming-0.6b model card (16 kHz mono input; punctuated, cased output).
