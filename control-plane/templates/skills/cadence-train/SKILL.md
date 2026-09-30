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
2. `runs.calibrate` — `dryRun: true`, then for real; wait for its job with `jobs.wait`.
3. `runs.new` — `dryRun: true` first and report the estimate in one line (GPU-hours low–high, card, duration), then
   the same call without `dryRun`. A real spending call without its dry run answers `playbook-dry-run-required`.
4. `jobs.wait` on the run's job until `state` is done, failed or cancelled (60 s per call; call again).
   `metrics.get` shows loss and validation WER meanwhile.
5. `checkpoints.list` — report the registered checkpoints with their validation WER (top-k register by themselves).
6. Eval matrix and gate: phase 3 — skipped in the plan; do not attempt them.

- 202 with `approvalId`: a person decides; say what you asked for and wait. Do not retry.
- A failed job or `playbook-stopped`: stop, summarise, suggest the next step, end your turn.
- At the end: a short summary with `@mix:`/`@run:` references and the next step; `projects.note` what you learned.

Help: `help.get id=guides.playbooks`, `id=errors.playbook-dry-run-required`.
