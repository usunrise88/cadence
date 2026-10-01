---
title: Pipelines, pipeline runs and artifacts
summary: How a pipeline file pins typed steps, how pipelines.run validates it against the published step kinds and runs each step on a worker, and how outputs land in the content store, are reused by input hash and retried.
contexts: [guide:pipelines, panel:pipeline-run, error:pipeline-invalid]
---

## What this is

Every block's work — importing data, calibrating, training, evaluating — is a **pipeline**: a YAML file in the
project repository (`pipelines/<name>.yaml`) listing typed steps. A **pipeline run** (`plr_…`) is one execution of
a pipeline at a commit, with a status per step. Steps read and write **artifacts**: immutable content in the
content store, addressed by the BLAKE3 hash of their bytes (`b3:<64 hex>`).

```yaml
name: echo
inputs: { text: text }                     # pipeline inputs by artifact type
steps:
  - id: first
    kind: echo@1                           # a step kind pinned at a version
    in: { text: $inputs.text }             # wired from a pipeline input …
  - id: second
    kind: echo@1
    in: { text: first.text }               # … or from another step's output
    params: { prefix: "echo: " }           # only the parameters that depart from defaults
```

- **Step kinds** are published by workers at start (kind, version, parameter schema, consumed and produced artifact
  types, resources); `stepKinds.list` shows them. A pipeline pins `kind@version`, so a new version never changes a
  pipeline silently.
- **Defaults**: a parameter not written takes its default — the `defaults.yaml` value its `x-cadence.defaultRef`
  names, else its `x-cadence.default`. A written value that differs is a **departure from defaults**, recorded on
  the step (`param`, `value`, `default`) and shown on the pipeline run.
- **Versions**: a pipeline's version is the commit that last changed its file; the run records it and the commit it
  was read at. A project without its own file of a name uses the bundled template of that name.

## Place in the loop

Run. Block facades (`runs.new`, later `evals.new`) start pipeline runs through the same engine; a playbook chains
them.

## Fields and defaults

A pipeline run carries `pipeline`, `source` (`repository`, `template`), `ref`, `commit`, `version`, `inputs`,
`state` (`running`, `done`, `failed`, `cancelled`) and its steps. Each step carries:

| Field | Meaning |
| --- | --- |
| `state` | `waiting` (an input is not produced yet), `queued` (its step job waits for a worker), `running` (a worker leased it), `done`, `reused`, `failed`, `skipped` (the run failed first), `cancelled` |
| `params`, `departures` | Resolved parameters and where they differ from defaults |
| `inputs`, `outputs` | Artifacts by name (`hash`, `type`, `size`, `meta`) |
| `inputHash` | Hash of kind, version, resolved parameters and input hashes |
| `reusedFrom` | The finished step whose outputs were reused |
| `attempts`, `attemptLog` | Each attempt is its own step job: `initial`, `oom`, `lost` or `retry`, with its batch scale and error |

Rules the engine applies:

- **Reuse by input hash**: a finished step of the same project with the same input hash supplies its outputs instead
  of running again, unless the run asks for `fresh`.
- **Out of memory** (`error.type = oom`): one automatic retry at 0.75× the batch (`overrides.batchScale`).
- **Lost lease** (the worker missed three heartbeats): one automatic retry.
- Any other failure fails the step and the run; steps that never started are skipped. `pipelineRuns.retry` runs a
  failed step again as a new attempt and the run continues from there; with no failed step it continues a cancelled
  run from its cancelled steps (or name one with `step`). A retry spends GPU time like a new run, so an agent's retry
  over budget waits for an approval.
- **Parameter checks**: the plan checks every parameter against its step kind's safe range (`min`/`max`,
  `minLength`/`maxLength`, `values`, `pattern`) — defaults included, so a kind whose empty default means "give a
  value" (`dataset_import`'s `source_name` and `licence`, R18) is refused by `pipelines.run` and its dry run, before
  a worker is involved.
- **A control-plane restart**: for one heartbeat window (30 s) after it starts, the control plane reaps no lease, so
  a step whose worker reports again (or releases a finished step) survives an outage of the control plane; a worker
  retries a release for about 13 minutes.
- **Output hooks**: when a step finishes, its outputs are recorded and the domains react in the same transaction —
  an import's `dataset` output registers a dataset version, a training step's `checkpoint` outputs register
  checkpoints. If a hook refuses an output, the step fails and nothing of it is kept.

Artifacts (`artifacts.get`) show type, size, neutral metadata, the producing step and, for a directory artifact (a
Shar set, a checkpoint directory), its files. `content=true` returns a file of at most 1 MiB inline.

## Commands

- `pipelines.list` — the project's pipelines with versions, inputs and steps.
- `pipelines.run` — `dryRun=true` validates and answers the plan (resolved parameters, departures, the estimate);
  the real run needs `If-Match` with the version from `pipelines.list` (or `*`) and answers the pipeline run.
  Spending GPU time over the project's budget waits for an approval.
- `pipelineRuns.list`, `pipelineRuns.get`, `pipelineRuns.wait` — follow runs; events on `pipeline_run.{id}`.
- `pipelineRuns.cancel` — waiting steps never start, running step jobs stop.
- `pipelineRuns.retry` — one failed step (or every failed step) as a new attempt.
- `artifacts.get` — one artifact by hash.

## Playbooks

- Agents: `pipelines.list` → `pipelines.run?dryRun=true` (fix every path of a [pipeline-invalid](../errors/pipeline-invalid.md)
  answer) → `pipelines.run` → `pipelineRuns.wait` until the state is final → on failure read the step's `error`,
  then `pipelineRuns.retry` or fix the pipeline.
- The `echo` starter pipeline checks that a worker runs steps end to end: give it any text artifact.

## Sources

- Cadence recommendation — docs/spec/03-pipelines-defaults.md "Pipelines and extension points";
  docs/spec/08-resolutions.md R11 (defaults.yaml), R14 (worker protocol), R15 (content store), R42 (neutral
  artifacts).
