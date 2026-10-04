---
title: Not in the project's languages
summary: projects.adopt refused a dataset version, golden set, normalizer or auxiliary model whose languages are none of the project's; adopt it with purpose replay when it is kept to measure forgetting.
contexts: [error:locale-mismatch, command:projects.adopt, field:purpose]
---

## What this is

A `422 Unprocessable Entity` problem of type `locale-mismatch`. A target adoption (`purpose` omitted or `target`)
checks that the version is in one of the project's languages (`project.yaml` `locales`). The version's languages are
its collection's `locale:` tags and its payload's `locales`, `locale` and `languages`; two locales match when their
language is the same (`he` matches `he-IL`, `sr-Latn` matches `sr-RS`), and a normalizer for every locale (`*`)
always matches.

| Kind | Checked |
| --- | --- |
| Dataset version, golden set, scoring normalizer, auxiliary model | yes, for a target adoption |
| Base model, model version | no: adapting a model to a new language is what a project does |
| Template, noise bank | no: they carry no language |

A version that declares no language, and a project without locales, pass.

## Replay

Replay data is in other languages on purpose: replay golden sets measure forgetting, replay dataset versions limit it
(R43, the mix's replay share). Adopt them with `purpose: replay` (the Golden set document offers **Adopt as
replay** when the dry run answers this problem); the licence is still checked.

## Place in the loop

Evaluation and data: freeze → **adopt** → name it in `gates.yaml` (target or replay) or in a mix.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/locale-mismatch` |
| `status` | `422` |
| `detail` | The version's languages and the project's |

## Commands

- `projects.adopt` with `purpose: replay` — adopt it as replay.
- `projects.edit` — add a locale to the project when the language is a target after all.

## Playbooks

- Agents: a golden set named under `replay` in `gates.yaml`, or a replay dataset of a mix, is adopted with
  `purpose: replay`; a target language the project lacks is a person's change to the project's locales.

## Sources

- docs/spec/02-domain-projects-registry.md "Registry": "Adoption checks licence and locale".
