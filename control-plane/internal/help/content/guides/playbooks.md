---
title: Playbooks and playbook sessions
summary: A playbook is a prefilled chain of commands with inputs, stop conditions, a prompt and an estimate; playbooks.run starts an agent session of kind playbook whose plan is the chain and ticks on the server as the session's commands succeed — "Try Cadence", "Adapt a new language", "Fine-tune from a dataset version" and more.
contexts: [guide:playbooks, panel:agent-sessions, panel:chat, error:playbook-dry-run-required, error:playbook-stopped, error:playbook-unavailable]
---

## What this is

A **playbook** is a template (`templates/playbooks/<name>.yaml`, a registry template version of kind `playbook`,
copied into every project's `playbooks/`) with:

- **inputs** — each with a type (`dataset_version`, `base_model`, `integer`, `number`, `string`, `boolean`) and one
  source: `required` (the person gives it), a `defaultRef` into `defaults.yaml`, `from: project` (the project's base
  model) or `from: adoption` (the project's adopted version of a collection, such as `dataset/replay-base`);
- a **chain** of steps — each names the command whose success ticks it (`command`, plus `accepts`), optionally
  `until: terminal` (it ticks when the job, run or pipeline run it waits for ended), `phase:` for steps a later
  roadmap phase ships (listed and skipped), `with:` (how inputs feed the command, used for its estimate), an
  `estimate` hint, and for steps a person does `person:` (what they do), `optional: true` (passed over when a later
  step ticks first) and `when:` (the answer's fields that tick it, e.g. `trainingCleared: true`);
- **stop** conditions (`step: failed`, `approval: denied`, `budget: exceeded`, `gate: failed`), the **next** step
  suggestions and the **prompt** (Go `text/template` over `.Inputs` and `.Project`).

The **estimate** is the sum of the chain's step estimates: `runs.new` from the estimate table (or the calibration),
other spending steps from their hint, skipped steps listed and not counted (R12, R16).

Phase 4 runs five of the six bundled playbooks:

| Playbook | What it does | Typical cost |
| --- | --- | --- |
| **Try Cadence** (`try-cadence`) | The smoke project: two hours of FLEURS (`playbooks.try_hours`) imported through `pipelines/import`, mixed with replay, calibrated, a short run (`playbooks.try_steps`, 300 steps), eval and gate. Model export and the parity check are listed for phase 5. A short run rarely passes the gate; the point is that the loop works. `make e2e` runs it | about 1 GPU-hour |
| **Adapt a new language** (`adapt-new-language`) | A corpus on a mount → source → ingest (`data-ingest`, or `pseudo-label` for untranscribed audio) → preview → freeze → mix with replay → calibrate → train → eval → gate | 4–8 GPU-hours |
| **Fine-tune from a dataset version** | Mix a frozen version with replay → calibrate → train → checkpoints → eval → gate | 1–4 GPU-hours |
| **Fix names and terms**, **Improve on telephony** | Boost list, correction batch or recordings, then a continuation stage; their phase-5 steps are skipped | 1–4 GPU-hours |
| **Weekly flywheel** | Listed; runs from phase 5 (signals, triage verbs, schedules) | 2–3 GPU-hours a week |

### Steps a person does

Some steps wait for a person, and the plan says what they do ("A person: …"). They still tick only from an
operation the step names, never from what the agent reports:

| Step | What the person does | What ticks it |
| --- | --- | --- |
| The mount exists | The admin approves the agent's `mounts.new` in Approvals | The approved request (it runs as the agent's), or `mounts.get` of an existing mount |
| The source is cleared for training | Only when `sources.get` answers `trainingCleared: false`: the admin reads the licence and approves the agent's `sources.edit` `trainingCleared: true` | `sources.get` answering `trainingCleared: true` — the same read that ticked "Register the source" ticks it too, so an already cleared source never asks for an approval — or the approved edit |
| The project adopts the auxiliaries (optional) | The admin approves each `projects.adopt` of an auxiliary model after checking its licence (R26) | The approved adoption, or `adoptions.list` |
| OASIS answers (pseudo-label only) | Starts the service on the host: `scripts/serve.sh ensemble no-300m` in the OASIS checkout (about 8.6 GB beside vLLM; its token in the secret `oasis-token`). Cadence never starts it | `auxiliaries.get` answering `reachable: true` |

An optional step is skipped when the next step ticks first: "OASIS answers" is passed over by a `data-ingest` run.
`pipelines/pseudo-label` cannot run without OASIS — its ensemble votes Whisper and OASIS, so the `oasis` step is not
optional and the dry run refuses with `auxiliary-unavailable` while the service does not answer. A read that does
not meet a step's `when` marks it running ("waiting for a person"); a read that ticks one step ticks the `when` steps
right after it that it already satisfies. While a step waits, tell the agent in Chat when
you have done your part; a denied approval stops the playbook.

## Place in the loop

Train (and later every block). A playbook is how a person hands a whole chain to an agent: the agent works it
through ordinary MCP tools, so every step is attributed, gated and budgeted like any other command.

## Fields and defaults

| Plan item state | Meaning |
| --- | --- |
| pending | Not reached yet |
| running | Its dry run answered (spending steps), its job is still running (terminal steps), or an answer did not meet its `when` yet (a person has not done their part) |
| done | The session's command succeeded (the note names the entity and job) |
| failed | The job it waited for failed or was cancelled |
| skipped | Its phase has not shipped, or it was optional and a later step ticked first |

Rules the server enforces in a playbook session:

- Only the current item ticks, and only from an operation it names; the agent cannot tick items itself.
- A spending command (`runs.new`, `runs.calibrate`, `runs.resume`, `runs.stage`, `checkpoints.average`,
  `evals.new`, `sweeps.run`, `pipelines.run`) needs a successful dry run of the same request (path, query and body)
  in the session first (`playbook-dry-run-required`).
- After the playbook ended, spending commands are refused (`playbook-stopped`); the session ends after its turn.
- A turn that ends without progress gets a reminder of the next step, at most twice in a row.

## Commands

- `playbooks.list`, `playbooks.get` (`?project=` fills project facts and estimates with them).
- `playbooks.run` — `dryRun=true` answers the resolved inputs, the estimate, the plan and the rendered prompt; the
  real run starts the session (agents cannot start playbook sessions).
- `agentSessions.get` — the session's `playbook`: state, plan, estimate, stop, summary, next.

## Playbooks

- In a playbook session: follow the plan in order, dry run before each spending step, wait on jobs with `jobs.wait`
  (pipeline runs with `pipelineRuns.wait`) until they end, finish with a summary and the next step.
- First time on an instance: run **Try Cadence** in a new project (Project home → Playbooks, or
  `make e2e` / `cadence smoke --project <slug> --input fleurs=sr_rs --input language=sr-RS`).
- A new language: put the corpus on a mount (`/cadence/corpora/<source>/<revision>/`, `scripts/corpora/`), then run
  **Adapt a new language** with its path, source name, licence, language and `data-ingest` or `pseudo-label`.

## Sources

- docs/spec/03-pipelines-defaults.md "Playbooks"; docs/spec/05-agents.md "Playbooks and schedules as sessions";
  docs/spec/08-resolutions.md R12, R16, R17; docs/review/2026-10-03-phase-4-plan.md (stream B, decisions 1, 5–6).
  Cadence recommendation for the rest.
