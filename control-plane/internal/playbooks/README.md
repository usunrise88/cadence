# internal/playbooks
Playbooks and playbook sessions (docs/spec/03-pipelines-defaults.md "Playbooks", docs/spec/05-agents.md "Playbooks
and schedules as sessions", R16).

## Format (`templates/playbooks/<name>.yaml`)

```yaml
name: finetune-from-dataset            # = the file name
title: Fine-tune from a dataset version
description: …
typicalCost: 1–4 GPU-hours             # text, for the list
availableFrom: 2                       # roadmap phase from which it runs (later: listed, playbooks.run refuses)
inputs:                                # in file order; exactly one of required, defaultRef, from
  dataset: { type: dataset_version, multiple: true, required: true, description: … }
  replay:  { type: dataset_version, from: adoption, collection: dataset/replay-base }
  base:    { type: base_model, from: project }          # the project's default base model
  steps:   { type: integer, defaultRef: training.steps } # value and safe range from defaults.yaml
chain:
  - { id: mix, title: …, command: mixes.new, accepts: [mixes.edit] }
  - { id: calibrate, title: …, command: runs.calibrate, estimate: { gpuHours: 0.1, minutes: 6, plusMinus: 0.5 } }
  - { id: train, title: …, command: runs.new, with: { baseModel: $inputs.base, steps: $inputs.steps, datasets: [$inputs.dataset, $inputs.replay] } }
  - { id: watch, title: …, command: runs.get, accepts: [jobs.wait], until: terminal }
  - { id: eval, title: …, command: evals.new, phase: 3 }
stop: [ { step: failed }, { approval: denied }, { budget: exceeded }, { gate: failed } ]
next: { done: …, stopped: … }          # the next-step suggestion written when the chain ends
prompt: |                              # Go text/template over .Inputs.<name> (text) and .Project.{Name,Slug,Locales}
  …
```

Validation (`Validate`, run on every bundled file by the tests and at server start): names; input types and sources
(`defaultRef` resolves in defaults.yaml; `from: project` only for `base_model`; `from: adoption` needs a
collection); unique step ids; every operation of a step that can run now is an implemented operation of the contract
(`cli.Operations`) or in `Pending` (operations a parallel stream is building; empty now); steps of a later phase only need the `<entity>.<verb>` form (a test checks the verb
against api/vocabulary.yaml); `with` names declared inputs; the prompt renders with every input.

## Estimate
The sum of the chain's step estimates (`EstimateChain`): an operation with an `Estimator` (`runs.new` from the
estimate table, R12) uses it with the step's resolved `with`; else the step's `estimate` hint (basis `hint`); a step
of a later phase is listed as skipped and not counted; the rest spend nothing. The basis is `table`, `measured`,
`hint`, `mixed` or `none`; plusMinus the largest; the budget compares the upper bound with the project's daily
GPU-hours. The server registers `runs.calibrate` (the calibrate kind's plan over `with.mix`), `runs.stage` and
`runs.resume` (the runs service's plans over `with.run`); an estimator's problem (the mix or the parent run exists only
during the session) falls back to the step's hint, with the reason as its note.

## Sessions
`playbooks.run` resolves the inputs, sums the estimate, renders the prompt and creates an agent session of kind
`playbook` with the playbook state on the row (`agent_sessions.playbook`, migration 0017): the plan (one item per
step; later phases `skipped`), the estimate, `dryRuns`, `stop`, `summary`, `next`. The transcript opens with the
estimate notice. The server ticks the plan, never the agent:

- The command pipeline's session hook (`commands.SessionHook`): `Done` ticks from a command that succeeded, in its
  transaction; `DryRun` records a dry run; `Admit` refuses a real spending command (`Spending`: runs.new|calibrate|
  resume|stage, checkpoints.average) without a dry run of the same operation since its last real one
  (`playbook-dry-run-required`), and any spending command once the playbook ended (`playbook-stopped`).
- Reads a chain names (`runs.get`, `jobs.wait`, `checkpoints.list`) are observed by the server's `observeReads` middleware.
- Only the current item (the first pending or running one) ticks, from an operation it names. A terminal step
  (`until: terminal`) waits for what the nearest earlier step started: `runs.get` of that run with an ended status
  ticks it (`done`; `failed`/`cancelled` fail it and stop the playbook when the template stops on `step`); a job
  (`jobs.wait`) ends it only when the earlier step started just that job, else it marks the step running.
- `sessions.PlaybookWatcher`: a denied approval stops the playbook (`approval: denied`); a pause on the agent budget
  stops it (`budget: exceeded`); a turn that ends after the playbook ended ends the session; a turn that ends without
  progress gets one reminder notice for the agent (at most `MaxNudges` in a row).
