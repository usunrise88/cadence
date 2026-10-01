---
title: Playbooks and playbook sessions
summary: A playbook is a prefilled chain of commands with inputs, stop conditions, a prompt and an estimate; playbooks.run starts an agent session of kind playbook whose plan is the chain and ticks on the server as the session's commands succeed.
contexts: [guide:playbooks, panel:agent-sessions, panel:chat, error:playbook-dry-run-required, error:playbook-stopped, error:playbook-unavailable]
---

## What this is

A **playbook** is a template (`templates/playbooks/<name>.yaml`, a registry template version of kind `playbook`,
copied into every project's `playbooks/`) with:

- **inputs** — each with a type (`dataset_version`, `base_model`, `integer`, `number`, `string`, `boolean`) and one
  source: `required` (the person gives it), a `defaultRef` into `defaults.yaml`, `from: project` (the project's base
  model) or `from: adoption` (the project's adopted version of a collection, such as `dataset/replay-base`);
- a **chain** of steps — each names the command whose success ticks it (`command`, plus `accepts`), optionally
  `until: terminal` (it ticks when the job it waits for ended), `phase:` for steps a later roadmap phase ships (listed
  and skipped), `with:` (how inputs feed the command, used for its estimate) and an `estimate` hint;
- **stop** conditions (`step: failed`, `approval: denied`, `budget: exceeded`, `gate: failed`), the **next** step
  suggestions and the **prompt** (Go `text/template` over `.Inputs` and `.Project`).

The **estimate** is the sum of the chain's step estimates: `runs.new` from the estimate table (or the calibration),
other spending steps from their hint, skipped steps listed and not counted (R12, R16).

Version 1 ships five playbooks; phase 2 runs **Fine-tune from a dataset version** (mix with replay → calibrate →
train → wait → checkpoints; eval and gate from phase 3). The other four are listed and run from phase 4.

## Place in the loop

Train (and later every block). A playbook is how a person hands a whole chain to an agent: the agent works it
through ordinary MCP tools, so every step is attributed, gated and budgeted like any other command.

## Fields and defaults

| Plan item state | Meaning |
| --- | --- |
| pending | Not reached yet |
| running | Its dry run answered (spending steps) or its job is still running (terminal steps) |
| done | The session's command succeeded (the note names the entity and job) |
| failed | The job it waited for failed or was cancelled |
| skipped | Its phase has not shipped |

Rules the server enforces in a playbook session:

- Only the current item ticks, and only from an operation it names; the agent cannot tick items itself.
- A spending command (`runs.new`, `runs.calibrate`, `runs.resume`, `runs.stage`, `checkpoints.average`) needs a
  successful dry run of the same operation in the session first (`playbook-dry-run-required`).
- After the playbook ended, spending commands are refused (`playbook-stopped`); the session ends after its turn.
- A turn that ends without progress gets a reminder of the next step, at most twice in a row.

## Commands

- `playbooks.list`, `playbooks.get` (`?project=` fills project facts and estimates with them).
- `playbooks.run` — `dryRun=true` answers the resolved inputs, the estimate, the plan and the rendered prompt; the
  real run starts the session (agents cannot start playbook sessions).
- `agentSessions.get` — the session's `playbook`: state, plan, estimate, stop, summary, next.

## Playbooks

- In a playbook session: follow the plan in order, dry run before each spending step, wait on jobs with `jobs.wait`
  until they end, finish with a summary and the next step.

## Sources

- docs/spec/03-pipelines-defaults.md "Playbooks"; docs/spec/05-agents.md "Playbooks and schedules as sessions";
  docs/spec/08-resolutions.md R12, R16, R17. Cadence recommendation for the rest.
