---
title: Gate configuration invalid
summary: The project's gates.yaml does not parse, leaves a safe range, or names a golden set the project has not adopted.
contexts: [error:gate-config-invalid, field:content, field:config]
---

## What this is

A `422 Unprocessable Entity` problem of type `gate-config-invalid`, with one entry in `errors` per problem (a JSON
pointer into the file and a message). The project's gate is `gates.yaml` in its repository; a value it leaves out
takes `defaults.yaml` `gate.*` and `eval.*`:

```yaml
primaryProfile: 80ms
target: { goldenSets: [golden-set/fleurs-he], rule: beat-baseline }
replay: { goldenSets: [golden-set/replay-golden-*], maxRegression: 0.005 }
deletionsInsertions: true
significance: { samples: 1000, level: 0.95, seed: 1 }
```

| Rule | Why |
| --- | --- |
| Only these keys (unknown keys fail) | A typo must not silently fall back to a default |
| `maxRegression` 0–0.1, `samples` 100–100 000, `level` 0.5–0.999, `rule` `beat-baseline` | The safe ranges of `defaults.yaml` |
| Golden sets as `golden-set/<name>` (a `*` matches several) or `ver_…`; a set is target or replay, not both | The gate reads target and replay cells differently |
| A named collection is one the project adopted | The gate resolves golden sets to the project's adopted versions |

`gates.edit` (its dry run too) and `evals.gate` check the file; `evals.new` reads it for its default golden sets,
primary profile and significance. Nothing was written.

## Place in the loop

Evaluate. The gate decides what counts as better; the verdict records the SHA of the file it read.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/gate-config-invalid` |
| `status` | `422` |
| `errors` | `[{path, message}]`, one per problem |

## Commands

- `gates.get` — the file, the effective gate and its departures from the defaults.
- `gates.edit` with `dryRun=true` — validate a change without committing it.
- `projects.adopt` — adopt a frozen version of a golden set the gate names.

## Playbooks

- Agents: fix the file in the session worktree, or propose the change with `gates.edit` (a person approves it).

## Sources

- docs/spec/04-blocks.md Block 3 ("The gate"); docs/spec/03-pipelines-defaults.md "Key defaults".
- RFC 9457, Problem Details for HTTP APIs.
