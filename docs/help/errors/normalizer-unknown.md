---
title: Unknown normalizer
summary: The scoring normalizer named is not a frozen normalizer version in the registry.
contexts: [error:normalizer-unknown, field:normalizerVersionId]
---

## What this is

A `422 Unprocessable Entity` problem of type `normalizer-unknown`: a request named a scoring normalizer — by version id
(`ver_…`) or by collection (`normalizer/he-il`, its newest frozen version) — that the registry does not hold as a frozen
`normalizer` version. `goldenSets.freeze` answers it for its `normalizerVersionId`, or for `defaults.yaml`
`eval.normalizer` when the request names none. Collection names are lowercase (`normalizer/he-IL` is read as
`normalizer/he-il`). Nothing was written.

A scoring normalizer is the text both sides of a WER are compared after (R21): Unicode form, case folding, literal
mappings, punctuation and combining marks. Cadence registers `normalizer/basic` and `normalizer/he-il` at start.

## Place in the loop

Evaluate. Every golden set is frozen with one normalizer version and every eval record is keyed by it, so scores stay
comparable across projects and over time; a new normalizer version means a new golden set version and a new baseline.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/normalizer-unknown` |
| `status` | `422` |
| `detail` | The reference that did not resolve |

## Commands

- `normalizers.list`, `normalizers.get` — the scoring normalizers and their rules.
- `defaults.get` — `eval.normalizer`, the one used when a request names none.

## Playbooks

- Agents: list the normalizers and retry with one that exists for the dataset's locale (or `normalizer/basic`); never
  invent a collection name.

## Sources

- docs/spec/08-resolutions.md R21 (normalizer identity).
- RFC 9457, Problem Details for HTTP APIs.
