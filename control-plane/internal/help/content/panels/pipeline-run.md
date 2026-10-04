---
title: Pipeline run
summary: Any pipeline run — steps with status and attempts, inputs and outputs with previews, departures from defaults, parameters from the step schema, each step's log; retry a step, cancel, open the pipeline file.
contexts: [panel:pipeline-run, command:pipelineRuns.get, command:datasets.materialize]
---

## What this is

A tool panel (right column of the Data workspace). A **pipeline run** executes a pipeline file (`pipelines/<name>.yaml`
in the project repository, or a bundled template) step by step; every step that needs a worker becomes a step job in
the queue. The panel shows one run, chosen from the project's runs (newest first) or focused from Queue & GPU or a
training run:

| Part | Meaning |
| --- | --- |
| Run | State, pipeline, source (repository or template), the commit it was read at, who started it, wall time, the run's inputs and why it failed |
| Needs materialize | For a run that is not done: each dataset version a training step that has not finished would read whose shards the cache evicted (`pipelineRuns.get` `needsMaterialize`) — the version (opens its document), the bytes and shards `datasets.materialize` copies back and the mounts they come from. Training reads only what the cache holds, so a retry fails that step (`artifact-missing`) until **Materialize** has brought it back; the notice says so when the copy ends |
| Steps | In execution order: `kind@version`, state (waiting, queued, running, done, reused, failed, skipped, cancelled), departures from defaults, ⚠ deprecated when its pack deprecates the pinned kind version (the tooltip names the cut-off day and the replacement; [step-kind-deprecated](../errors/step-kind-deprecated.md)), attempts, wall time |
| Inputs and outputs | Artifacts by name, type, hash and size; select one for its preview (`artifacts.get`: metadata, a directory's files, or up to 4 000 characters of text) |
| Departures | The parameters that differ from their defaults, with the default beside the value |
| Parameters | The resolved parameters as a read-only form rendered from the step kind's schema (`stepKinds.get`): each with "Why this default?" (description, default, source, safe range) and a "departs from default" chip |
| Attempts | Each attempt with why it started (initial, the automatic OOM retry at 0.75× batch, a lost lease, a retry), its batch scale, state and error |
| Log | The step's current job log (the same view as the Logs panel) |

A step marked **reused** did not run: a finished step with the same input hash (kind, version, resolved parameters and
input hashes) gave its outputs. Live on `pipeline_run.{id}`.

## Place in the loop

Run → Review. Follow an import, a calibration or a training stage step by step; open what a step produced.

## Fields and defaults

| Field | Default | Meaning |
| --- | --- | --- |
| Run | The run focused elsewhere, else the project's newest | Pick any run of the project |
| Parameters | The step kind's `x-cadence` defaults (`defaultRef` into defaults.yaml) | Only departures are written in the pipeline file |
| Retry batch scale | 1 (full batch) | The API also takes a smaller scale; the automatic OOM retry uses 0.75 |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Retry | `pipelineRuns.retry` | A failed or cancelled step, as a new attempt, once the run has stopped; the run continues from there |
| Cancel run | `pipelineRuns.cancel` | Two clicks; waiting steps never start and running step jobs are cancelled |
| Open pipeline file | — | Opens `pipelines/<name>.yaml` in the Recipe document (not for a bundled template) |
| Open in Logs | — | Focuses the step's job in the Logs panel |
| Materialize | `datasets.materialize` | In the needs-materialize notice: copies an evicted dataset version back into the cache (a job); then retry |

## Playbooks

- **A step failed with an OOM twice.** Read its log, lower the batch in the pipeline file (the parameter's safe range
  is in "Why this default?"), and retry the step.
- **A training step failed with artifact-missing.** The cache evicted a dataset version it reads (the run stopped,
  so nothing pinned it). The needs-materialize notice names it: **Materialize**, wait for "Back in the cache", then
  **Retry**. A dry run of `pipelines.run` or `runs.new` reports the same as a `needs-materialize` warning before
  anything is queued.
- **Why is this run's parameter different?** The Departures list shows every value that departs from defaults.yaml
  and its default; the same list is on the run for agents (`pipelineRuns.get`).

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Pipeline run); docs/spec/03-pipelines-defaults.md "Model".
- docs/review/2026-09-30-phase-2-plan.md "Pipelines (P)" (input-hash reuse, OOM retry at 0.75× batch, retry of one
  step).
