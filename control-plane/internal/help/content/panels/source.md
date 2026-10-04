---
title: Source
summary: A corpus in the registry — licence, kind, languages, whether it is cleared for training, its clearing history, the dataset versions ingested or imported from it, and its utterances.
contexts: [panel:source, command:sources.get, command:sources.edit, command:sources.archive, command:utterances.search]
---

## What this is

A document (centre of the Data workspace) for one source (`src_…`), from `sources.get`. A source is registered with
its licence (`sources.new`, or by the first import that names it) and is **eval-only** until a person clears it for
training (R18): its dataset versions are evaluated on, never mixed, until then.

## Place in the loop

Data: **register the source** → ingest it from a mount (`pipelines/data-ingest`, which names it: no licence, no
ingest) → preview → freeze → mix.

## Fields and defaults

| Section | Shows |
| --- | --- |
| Licence and clearance | Licence, kind (public, production, synthetic), languages, URL, training (cleared by whom and when, or eval-only), archived |
| Clearance card | **Clear for training** (or **Make eval-only**) and the licence, both `sources.edit`; an agent's call waits for a person's approval |
| Ingest history | `ingests`: each dataset version an import or ingest registered from it, newest first — when, the version (opens as a document), the step kind, utterances, hours, draft or frozen. **Open data-ingest** opens the pipeline file to ingest it again |
| Utterances | `utterances.search` within the source: text, language, origin, speaker, duration; **Play** opens a row in Audio |
| Activity tab | The clearing history (`clearances`, oldest first): registered, licence changed, cleared, made eval-only, by whom |

## Commands

- `sources.edit` (header **Edit**): licence, description, training clearance.
- `sources.archive` (header **Archive**, the admin's): it takes no new ingests; its utterances and versions stay.

## Playbooks

- Agents: never clear a source; read the corpus's licence (`SOURCE.yaml` on the mount) and ask the person.

## Sources

- docs/spec/02-domain-projects-registry.md "Data entities as built"; docs/spec/08-resolutions.md R18, R26.
- docs/spec/11-ui-panels.md "Panel catalogue", Source.
