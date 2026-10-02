---
title: Language packs and boost lists
summary: Everything a locale needs — the scoring normalizer it names, the training text style, written forms of numbers, script maps, language ID, boost lists and the golden-set recipe — as lang/<locale>/ in the project repository.
contexts: [guide:language-packs]
---

## What this is

A **language pack** is one directory per locale in the project repository, `lang/<locale>/`, versioned by commits
like any recipe. Cadence ships starter packs (he-IL, and sr for Serbian in Latin and Cyrillic); the project wizard
copies the pack of every project locale, and `projects.sync` offers updates to them.

| File | What it holds |
| --- | --- |
| `normalizer.yaml` | `scoring.normalizer`: the registry normalizer WER is computed after (`normalizer/he-il`, or a pinned `ver_…`); `training`: the text style transcripts are trained in (Unicode form, marks, case, punctuation, mappings, a transliteration) |
| `itn.yaml` | Written forms of numbers, phone numbers, dates, times, amounts and percentages, each class with a pattern and examples |
| `translit.yaml` | Script maps where a locale has two (`sr-Cyrl-Latn`), named after the worker transliteration that applies them |
| `lid.yaml` | Accepted locales, what happens to code-switched utterances (`keep`, `drop`, `flag`) and the confidence floor |
| `boost/<domain>.txt` | Boost lists: names, products, streets — terms the decoder should prefer |
| `golden-recipe.yaml` | How a golden set for the locale is sampled and sized |
| `README.md` | The locale's known pitfalls, for people and agents |

Scoring and text style are different jobs (R21): the **scoring normalizer** is a registry asset, versioned and shared,
so WER stays comparable across projects and over time; the **training text style** is project configuration and is
pinned by the commit that changed the pack. A pack is identified by the last commit that changed it (`sha`).

## Place in the loop

Prepare data (the training text style and transliteration), evaluate (the scoring normalizer, boost lists as a
decoding axis, number classes for entity accuracy) and find things (the search index folds text with the locale's
scoring normalizer).

## Fields and defaults

**Boost list format.** One file per list:

```
# weight: 1.0
# Comment lines start with #; blank lines are ignored.
Moshe Cohen
WhatsApp
```

The `# weight: <number>` header is required once; every other non-comment line is one term, trimmed, written as the
model should write it, and appears once. A new list starts at `langpacks.boost_weight` (1.0, range 0–10) and holds at
most `langpacks.boost_max_terms` terms (5000). The eval decoder receives a list as a `boost_list` artifact
`{"terms": [...], "weight": w}`; its sha256 is the list's identity in an eval's decoding axis.

**Shapes.** Every YAML file has `version: 1` and a `locale` of the pack's language; unknown keys are refused, so the
files stay machine-editable. A pack needs `normalizer.yaml`; files the layout does not know are refused (Markdown
notes are fine). Collection names are lower case: `normalizer/he-il`, not `normalizer/he-IL`.

## Commands

| Operation | What it does |
| --- | --- |
| `langpacks.list` | The project's packs with their commit, scoring normalizer and boost lists, and the locales Cadence ships |
| `langpacks.get` | One pack: every file, the resolved scoring normalizer, the parsed boost lists, `issues` for files whose shape is wrong; ETag is the pack's sha |
| `langpacks.edit` | Writes or deletes files of one pack in one commit; `If-Match` is the pack's sha; shapes and boost lists are checked first ([validation-failed](../errors/validation-failed.md)); a pack changed since answers [precondition-failed](../errors/precondition-failed.md); `dryRun` only checks |
| `boost.edit` | Replaces one list's terms (and weight) in one commit; the list's comment lines are kept |

A person's edit commits to `main`. An agent's edit, while the project's draft policy for language packs is `draft`
(Agent settings, the default), lands on a branch `langpack/<locale>-<date>` that a person accepts with
`branches.accept` or discards with `branches.revert`.

`projects.sync` treats packs three-way: a file the project never edited is updated to Cadence's current version, a
file the project edited stays as it is, a file the project deleted is not brought back, and a pack missing entirely
(a project older than its pack) is offered whole.

## Playbooks

- Fix names and terms: put the names in `boost/names.txt` with `boost.edit`, then evaluate with and without the list
  (an eval's decoding axis) before trusting it — over-boosting shows as insertions of listed terms.
- Serbian: both sides of a WER must be in one script; import golden sets with the training transliteration
  (`sr-Cyrl-Latn`), or every word scores wrong.
- See [Search and the query language](search.md) for how packs shape search, and [Lineage](lineage.md) for what a
  run or eval pinned.

## Sources

- docs/spec/03-pipelines-defaults.md "Language packs and hot words"; docs/spec/08-resolutions.md R21 (scoring vs text
  style), R24 (boosting in evaluation).
- The starter packs' README.md files cite their sources (ivrit.ai, the Academy of the Hebrew Language); the boost
  defaults are a Cadence recommendation.
