---
title: Pipeline invalid
summary: The pipeline does not parse or does not fit the published step kinds — an unknown kind@version, wiring between mismatched artifact types, bad parameters, a cycle or a missing input. errors lists every problem with its path.
contexts: [error:pipeline-invalid, guide:pipelines]
---

## What this is

A `422` problem answered by `pipelines.run` — a dry run or a real one — when the pipeline cannot run as written.
Cadence validates the whole pipeline before anything starts (docs/spec/03-pipelines-defaults.md, "Pipelines and
extension points"), so a mistake shows up at plan time, not at step 4 of a run. Every problem is listed in `errors`,
each with a `path` into the pipeline file or the request:

| Path | Typical message |
| --- | --- |
| `name`, `steps[i].id` | Not a valid name, a duplicate step id, or the name differs from the file name |
| `steps[i].kind` | Not pinned as `name@version`, or no runtime publishes that kind and version (see `stepKinds.list`) |
| `steps[i].in.<input>` | Not `$inputs.<name>` or `<step>.<output>`; an input the kind does not consume; a consumed input left unwired; an artifact type that differs from the one the kind consumes |
| `steps[i].params.<param>` | A parameter the kind does not have, a required one without a default, a value outside the kind's schema or its safe range, or a `defaultRef` that does not resolve in `defaults.yaml` |
| `steps` | The steps form a cycle |
| `inputs.<name>` | A pipeline input that is missing, of the wrong type, not declared, or not in the content store |
| `params.<step>` | An override for a step the pipeline does not have |

A file that does not parse at all (unknown keys, bad YAML) answers one error with the parser's message;
`pipelines.list` shows such a file with `error` instead of steps.

## Place in the loop

Run: every training, eval and data block is a pipeline run, and block facades such as `runs.new` validate their
pipeline the same way.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/pipeline-invalid` |
| `status` | `422` |
| `detail` | The pipeline, how many problems, and the first one |
| `errors[]` | `{path, message}` for every problem |

## Commands

- `pipelines.list` — the project's pipelines with their steps, inputs and versions.
- `pipelines.run?dryRun=true` — validate and plan without starting anything; repeat until it answers the plan.
- `recipes.get pipelines/<name>.yaml` — read the file; an agent edits it in its session worktree and commits.

## Playbooks

- Agents: call `pipelines.run` with `dryRun=true` first, fix each listed path, and run for real only once the dry
  run answers the plan (resolved parameters, departures from defaults and the estimate).

## Sources

- Cadence recommendation — docs/spec/08-resolutions.md R11 (defaults and `defaultRef`), R42 (artifact types).
