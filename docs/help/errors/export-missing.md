---
title: Model export missing
summary: A parity check, a benchmark or a promotion named a model version, latency profile and format that has no exported deployable yet. Export it first with models.export.
contexts: [error:export-missing, command:models.parity, command:models.benchmark, command:models.export, artifact:deployable]
---

## What this is

A `422 Unprocessable Entity` problem of type `export-missing`. `models.parity` and `models.benchmark` decode an
export through the staging server, so the model version needs an **exported** deployable at the profile and format
the request names (or defaults to: the project's primary profile and the family's first export format). The detail
says what there is instead:

| Detail says | Why | What to do |
| --- | --- | --- |
| has no export at profile … | The version was never exported at that profile and format | `models.export` with `{version, profiles: [<profile>]}` (dry run first) |
| has an export that is exporting | Its export pipeline has not finished | Wait for its pipeline run (`pipelineRuns.wait`), then ask again |
| has an export that is failed | The export step failed | `models.get` shows the export's error; `models.export` runs it again |

## Place in the loop

Block 4, Deploy: export → parity → benchmark → shadow → canary. Every later step reads the export.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/export-missing` |
| `status` | `422` |
| `detail` | The model version, the profile, the format and the export's state |

- The profiles an export writes when the request names none: `deploy.export_profiles` (`primary`).

## Commands

- `models.get` — `exports[]`: each export's profile, format, state, deployable and error.
- `models.export` — export a model version for serving (GPU spend; dry run first).

## Playbooks

- An agent exports, then checks parity, then benchmarks — each with `dryRun=true` first.

## Sources

- docs/spec/02-domain-projects-registry.md "Deployment entities" (Model exports); docs/spec/03-pipelines-defaults.md
  "Export, parity and benchmark (phase 5)".
