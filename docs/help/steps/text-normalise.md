---
title: text_normalise (step kind)
summary: Brings segment transcripts into the language pack's training text style — script, Unicode form, mappings, marks, case, punctuation — and applies spoken-to-written rules.
contexts: [step:text_normalise, artifact:segments]
---

## What this is

`text_normalise@1` is a runtime-neutral core step kind (CPU, job kind `data`). It reads a `segments` artifact and
rewrites every transcript in the training text style, in this order: `transliterate` (a script map from
`translit.yaml`, e.g. `sr-Cyrl-Latn`), the Unicode form, `mappings` (literal replacements in list order), mark removal,
case folding, punctuation, whitespace, then the `itn` rules (spoken → written; whole words, case-insensitive, the
longest spoken form first). A transcript that changes keeps its original as `textOriginal`. Every other key passes
through; segments without text are left alone.

The defaults keep the base model's style: cased, punctuated, NFC. Copy the values of `lang/<locale>/normalizer.yaml`
(`training`) and the written forms of `itn.yaml` into the pipeline's params; the scoring normalizer (R21) is a
different thing and is never applied here.

## Place in the loop

Data — `pipelines/data-ingest.yaml`, after ingest (and pseudo-labels), before `manifest_filter` (characters per second
are counted on the normalised text).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `unicode` | `NFC` | `normalizer.yaml` training.unicode | `NFC`, `NFKC` |
| `remove_marks` | `false` | `normalizer.yaml` training.removeMarks | true, false |
| `casefold` | `false` | model card (cased output) | true, false |
| `punctuation` | `keep` | model card (punctuated output) | `keep`, `strip` |
| `mappings` | `[]` | `normalizer.yaml` training.mappings | `[{from, to}]` |
| `transliterate` | `""` | `normalizer.yaml` training.transliterate | `""`, `sr-Cyrl-Latn` |
| `itn` | `[]` | `itn.yaml` | `[{spoken, written}]` |

## Commands

- `langpacks.get` — the pack's training style and ITN classes to copy.
- `pipelines.run` with `data-ingest`.

## Playbooks

- Serbian: `transliterate: sr-Cyrl-Latn` so Cyrillic and Latin sources train in one script.
- Numbers: add `{spoken, written}` rules for the forms your sources spell out; a full per-language number grammar is
  not part of this step.

## Sources

- docs/spec/03-pipelines-defaults.md "Language packs and hot words"; docs/spec/08-resolutions.md R21.
- Unicode Standard Annex #15, Unicode Normalization Forms.
