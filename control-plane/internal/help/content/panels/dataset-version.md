---
title: Dataset version
summary: One dataset version — a draft indexed in place on a mount or a frozen selection in the content store — with its preview, freeze (leakage check first), quality checks, dataset card, shards, statistics charts, exports, the projects that use it and its utterances.
contexts: [panel:dataset-version, command:datasets.get, command:datasets.preview, command:datasets.freeze, command:datasets.export, command:exports.list, command:texts.get, command:projects.adopt, command:versions.archive, command:utterances.search]
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
| Quality checks | `dataset.quality`: silence share, clipped segments, length outliers with value and threshold (✓ pass, ⚠ warn; warnings never block a freeze) |
| Dataset card | The card the freeze wrote (`dataset.card`), read with `texts.get` and rendered as Markdown — sanitised: raw HTML is dropped and links open in a new tab. Up to 256 KiB is shown; the hash and size are below it |
| Statistics | R53 charts from `dataset.stats` through `@/shell/charts`: hours by language and split, duration histogram with p5/p50/p95 and the preview's bounds, characters per second, level, source sample rates, hours by transcript origin and channel role. Each chart has a table view and CSV copy. For call recordings, **End-of-utterance gap** charts `stats.eou`: the gap from a segment's last speech to the other party's next speech, with p50/p90 marks, how many segments had a gap within 10 s and how many overlapped (barge-in); shown only when the version has gaps measured |
| Shards | `dataset.shards`: index, cuts manifest hash, utterances, hours, audio bytes, location now (`cas`; `mount` once evicted, while a mount copy can bring it back; `missing` when none can) and whether the cache pins the version now — read from the cache when the document loads, not from the frozen record |
| Exports | The version's exports run in the open project (`exports.list`, live on `entity.export.*`): format, state with its pipeline run, target (a mount directory, the content store, or the Hub repository with a link), files, size, mount copies, who started it. **Export…** opens the export card (a frozen version only) |
| Export card | Format (Lhotse Shar, NeMo manifest, Cadence bundle, Hugging Face Hub), the target — the default (`storage.export_mount` when registered and writable, else the content store), the content store, or a writable path mount with a directory (default `<collection>/<version>/<format>`) — or, for the Hub, the repository and its visibility (default `storage.export_hub_private`). **Plan** dry-runs `datasets.export`: the step kind, target, what it reads, the licence and sources, whether the audio becomes mount copies and whether an approval is needed. **Export** sends exactly the planned request (an edit makes the plan stale); a Hub push answers an approval the admin decides |
| Used by | Projects that adopted it and their aliases; **Adopt into <project>…** opens the adopt card |
| Utterances | `utterances.search` within the version: transcript text, language, origin, speaker, duration and split; **Play** opens a row in Audio |

The adopt card dry-runs `projects.adopt` first: the licence
([licence-forbids-adoption](../errors/licence-forbids-adoption.md)) and the locale
([locale-mismatch](../errors/locale-mismatch.md)); **Check as replay** checks it as replay data of another language.

## Commands

- `datasets.freeze` (header **Freeze**, a draft only), `datasets.preview` (header **Preview**).
- `datasets.export` (header **Export**, a frozen version only; the Hub needs an approval —
  [export-not-allowed](../errors/export-not-allowed.md) for a source whose licence forbids it). See the
  [interoperability guide](../guides/interoperability.md) for the formats.
- `projects.adopt` (header **Adopt**, a frozen version only).
- `versions.archive` (header **Archive**; the admin's, refused with [version-in-use](../errors/version-in-use.md) while
  anything uses it).

## Playbooks

- "Adapt a new language": ingest from a mount → preview → freeze → adopt → mix → run → eval → gate.

## Sources

- docs/review/2026-10-03-phase-4-plan.md "Decisions taken for phase 4" 3–4; docs/spec/08-resolutions.md R53.
- docs/spec/11-ui-panels.md "Panel catalogue", Dataset version.
