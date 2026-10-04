---
title: Source without a usable licence
summary: No licence, no ingest — the ingest names a source that is not registered, is archived, or has no usable licence (unknown, none, NOASSERTION).
contexts: [error:source-unlicensed, field:source, step:sdp_ingest]
---

## What this is

A `422 Unprocessable Entity` problem of type `source-unlicensed`. Every ingest names the registry source its audio
belongs to (`sdp_ingest`'s `source` parameter, marked `x-cadence.registry: source`). The pipeline engine checks that
source when a pipeline is planned or started (`pipelines.run`, its dry run included), and the `dataset` output hook
checks it again when the draft registers:

| Why | What to do |
| --- | --- |
| No source of that name | Register the corpus first: `sources.new` with its name, licence (the corpus card's, an SPDX id where one exists), kind and languages |
| The source is archived | Archived sources take no new ingests; register the corpus under a new source or ask the admin |
| The licence names no licence | `unknown`, `none`, `unlicensed`, `NOASSERTION`, `n/a`, `tbd` and an empty licence are not licences: a person finds the corpus's licence and sets it with `sources.edit` |

Imports through `dataset_import` meet the same rule: an import whose `licence` parameter names no licence fails.
Nothing was ingested.

## Place in the loop

Data: register the source → ingest from a mount (`pipelines/data-ingest`) → preview → freeze. A licence allows a
corpus to be ingested; clearing it for training (`trainingCleared`) is a separate decision a person makes
(docs/spec/08-resolutions.md R18, R26).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/source-unlicensed` |
| `status` | `422` |
| `detail` | The step and parameter, the source, and why it was refused |

## Commands

- `sources.new` — register a corpus with its licence (it starts eval-only).
- `sources.get` — the licence, its history (`clearances`) and the ingests from the source (`ingests`).
- `sources.edit` — set the licence (an agent's call waits for a person's approval).

## Playbooks

- Agents: never guess a licence. Read the corpus's card or its `SOURCE.yaml` on the mount, and ask the person to
  register or correct the source when the licence is missing.

## Sources

- docs/spec/04-blocks.md Block 1, "Gates: no licence, no ingest".
- RFC 9457, Problem Details for HTTP APIs.
