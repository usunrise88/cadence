---
title: Export not allowed
summary: A dataset version cannot be pushed to the Hugging Face Hub — it holds production audio, a source without a usable licence, or a golden set's held-out data — or a pipeline names an export step, which only datasets.export runs.
contexts: [error:export-not-allowed, command:datasets.export, command:pipelines.run, step:hf_push, step:dataset_export, step:shar_export]
---

## What this is

A `422 Unprocessable Entity` problem of type `export-not-allowed`. `datasets.export` with format `hf-hub` runs a
licence check before anything is asked or pushed (docs/spec/03 "Interoperability": a dataset goes to the Hub "after a
licence check"). The detail lists every reason:

| Reason | Why it stops the push |
| --- | --- |
| A source is `production` | Customers' calls never leave the instance |
| A source has no usable licence (`unknown`, `none`, `NOASSERTION`, empty, …) | Nobody may publish audio whose licence is unknown |
| The version names no source | Its licence cannot be checked |
| A golden set is built on it, or it shares an utterance (or an acoustic fingerprint) with a golden set's data, in any split | Held-out test audio made public is no longer held out |

The same type answers `pipelines.run` (and every facade that starts a pipeline) when a step's kind is an export kind
(job kind `export`: `hf_push`, `dataset_export`, `shar_export`): exports run only through `datasets.export`, which
makes these checks and asks the approvals; a pipeline would skip them.

Nothing was started and no approval was asked. Exports that stay inside (`lhotse-shar`, `nemo-manifest`,
`cadence-bundle` to a mount or the content store) are not checked this way.

## Place in the loop

Data → published. A version that passes the check still waits for the admin's approval (preset rule `hub-export`).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/export-not-allowed` |
| `status` | `422` |
| `detail` | The version and every reason it is not pushed |

## Commands

- `sources.get` — a source's licence and kind; `sources.edit` — a person corrects a licence.
- `datasets.export` with another format — keep the data inside Cadence or on your own storage.

## Playbooks

- Agents: do not look for another way to publish the data; tell the person which source or golden set blocks it.

## Sources

- docs/spec/03-pipelines-defaults.md "Interoperability"; docs/spec/08-resolutions.md R26 (licences).
- RFC 9457, Problem Details for HTTP APIs.
