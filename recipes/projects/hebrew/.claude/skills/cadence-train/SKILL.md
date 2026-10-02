---
name: cadence-train
description: Cadence block "train" workflow: which MCP tools to call in what order, the gates to respect, and what to report. Load when working on the train block of a Cadence project, and in every playbook session.
---

# cadence-train

## Contract
- Act only through Cadence MCP tools; never edit the database or production.
- Every GPU-consuming command: call with dryRun first, compare with the project budget, then run.
- Report results as entity references (`@run:123`, `@eval:45`) so the UI can link them.

## Playbook: fine-tune from a dataset version (`finetune-from-dataset`)
A playbook session starts with the plan; Cadence ticks each step when your call for it succeeds (you cannot tick
steps). Work the steps in order:

1. `mixes.new` — groups: the dataset version(s); plus a group with `replay: true` holding `dataset/replay-base` when
   the project adopted it; `replayShare` as given. A draft or a follow-up `mixes.edit` also counts.
2. `runs.calibrate` with `mix` (and `baseModel`) — `dryRun: true`, then for real.
3. `runs.new` with `mix`, `baseModel`, `steps` — `dryRun: true` first and report the estimate in one line
   (GPU-hours low–high, card, duration), then the same call without `dryRun`; it answers the run (`run_…`). A real
   spending call without its dry run answers `playbook-dry-run-required`.
4. Follow the run: `jobs.wait` on its `currentJobId` (60 s per call; call again), then `runs.get id=<run>` until
   `status` is done, failed or cancelled. `metrics.get id=<run>` shows loss and validation WER meanwhile.
5. `checkpoints.list` with `run=<run>` — report the checkpoints with their validation WER (top-k are kept).
   `checkpoints.average` (dry run first) averages chosen ones; `runs.stage` (a parent run, `peakLr`) continues.
6. `evals.new` with `subject: {checkpointId: <the best kept checkpoint>}` (`p` = the project) — `dryRun: true` first
   and report the plan in one line (cells cached / to compute, GPU-hours), then the same call for real; it answers
   the eval (`evl_…`) or a 202 with an `approvalId` over budget. Leave golden sets, profiles and baseline out: they
   come from `gates.yaml` and `@baseline` (the `cadence-eval` skill has the axes).
7. Follow it: `evals.get id=<eval>` until `status` is done or failed (`progress` counts cells; call again).
8. `evals.gate id=<eval>` with `ifMatch` = the `etag` of your last `evals.get` — read `gate.verdict` and report every
   check (`kind`, golden set, delta with its interval, `state`). A failed verdict: stop and name the checks that
   failed or were inconclusive.
9. Only after a passed gate: suggest `models.register` with the checkpoint (dry run first; for an agent it waits for
   a person's approval — say so and do not retry). Never register after a failed gate.

- 202 with `approvalId`: a person decides; say what you asked for and wait. Do not retry.
- A failed job or `playbook-stopped`: stop, summarise, suggest the next step, end your turn.
- At the end: a short summary with `@mix:`/`@run:`/`@eval:` references and the next step; `projects.note` what you
  learned.

Help: `help.get id=guides.playbooks`, `id=guides.evaluation`, `id=errors.playbook-dry-run-required`,
`id=errors.gate-not-passed`.
