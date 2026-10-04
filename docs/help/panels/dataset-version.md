---
title: Dataset version
summary: One dataset version — a draft indexed in place on a mount or a frozen selection in the content store — with its preview, freeze (leakage check first), quality checks, card, shards, statistics charts, the projects that use it and its utterances.
contexts: [panel:dataset-version, command:datasets.get, command:datasets.preview, command:datasets.freeze, command:projects.adopt, command:versions.archive, command:utterances.search]
---

## What this is

A document (centre of the Data workspace) for one registry version in `dataset/…`. Everything it shows is
`datasets.get`, the numbers an agent reads. A version is one of:

| State | What it is | What you do |
| --- | --- | --- |
| draft | What `pipelines/data-ingest` ends in: segments indexed in place on a mount (`dataset.frozen: false`), no audio copied | Preview it, then freeze it |
| frozen | Cut into the content store (shards), or imported by `dataset_import` (frozen at import) | Adopt it, mix it, evaluate on it |
| archived | The registry's soft delete (`versions.archive`, the admin's) | Nothing; its lineage stays |

Only frozen versions are mixed, trained on, exported or adopted.

## Place in the loop

Data: source → ingest (draft) → **preview → freeze** → adopt → mix → run.

## Fields and defaults

| Section | Shows |
| --- | --- |
| Draft / Frozen | The state, the freeze's pipeline run while it cuts (**Open Pipeline run**), **Freeze…** and **Preview with filters…**; for a frozen version the leakage result |
| Freeze card | `datasets.freeze` dry run first: the leakage check against every golden set (passed, and how many sets), or [golden-set-leakage](../errors/golden-set-leakage.md) with the overlaps. **Freeze** starts the cut (a CPU pipeline run); the version turns frozen when it ends |
| Preview card | `datasets.preview`: kept utterances and hours per language and split after duration, characters-per-second, language and origin filters, and what each filter drops; the bounds show on the charts. Nothing is written |
| What it holds | Size, languages, source(s) (open as Source documents), licence, split rule, recipe (pipeline and commit), and the splits table |
| Quality checks | `dataset.quality`: silence share, clipped segments, length outliers with value and threshold (✓ pass, ⚠ warn; warnings never block a freeze); the dataset card's hash |
| Statistics | R53 charts from `dataset.stats` through `@/shell/charts`: hours by language and split, duration histogram with p5/p50/p95 and the preview's bounds, characters per second, level, source sample rates, hours by transcript origin and channel role. Each chart has a table view and CSV copy |
| Shards | `dataset.shards`: index, cuts manifest hash, utterances, hours, audio bytes, location now (`cas`; `mount` once evicted, while a mount copy can bring it back; `missing` when none can) and whether the cache pins the version now — read from the cache when the document loads, not from the frozen record |
| Used by | Projects that adopted it and their aliases; **Adopt into <project>…** opens the adopt card |
| Utterances | `utterances.search` within the version: transcript text, language, origin, speaker, duration and split; **Play** opens a row in Audio |

The adopt card dry-runs `projects.adopt` first: the licence
([licence-forbids-adoption](../errors/licence-forbids-adoption.md)) and the locale
([locale-mismatch](../errors/locale-mismatch.md)); **Check as replay** checks it as replay data of another language.

## Commands

- `datasets.freeze` (header **Freeze**, a draft only), `datasets.preview` (header **Preview**).
- `projects.adopt` (header **Adopt**, a frozen version only).
- `versions.archive` (header **Archive**; the admin's, refused with [version-in-use](../errors/version-in-use.md) while
  anything uses it).

## Playbooks

- "Adapt a new language": ingest from a mount → preview → freeze → adopt → mix → run → eval → gate.

## Sources

- docs/review/2026-10-03-phase-4-plan.md "Decisions taken for phase 4" 3–4; docs/spec/08-resolutions.md R53.
- docs/spec/11-ui-panels.md "Panel catalogue", Dataset version.
