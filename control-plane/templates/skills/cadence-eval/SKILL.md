---
name: cadence-eval
description: Cadence block "eval" workflow: golden sets, adoption, the baseline, gates.yaml, the eval matrix (evals.new and its axes), reading cells, deltas and intervals, the gate and model registration — which MCP tools to call in what order, the approvals to respect, and what to report. Load when evaluating a checkpoint or model of a Cadence project.
---

# cadence-eval

## Contract
- Act only through Cadence MCP tools; never edit the database or production.
- Every GPU-consuming command: call with dryRun first, compare with the project budget, then run.
- Report results as entity references (`@eval:evl_…`, `@run:run_…`) so the UI can link them.
- Golden sets, the baseline and the gate decide what counts as better: you propose changes to them, a person
  approves. Never edit `gates.yaml` in your worktree — propose it with `gates.edit` (an approval).

## 1. Golden sets (registry, read-mostly)
- `goldenSets.list` (`collection`, `state`) — frozen held-out test sets, one locale each, each tied to a scoring
  normalizer version (`normalizers.get`). `goldenSets.get id=<ver_…>` shows size, hours, resampling unit and users.
- A new golden set is frozen from a dataset version registered eval-only: `goldenSets.freeze` with
  `datasetVersionId` (and `normalizerVersionId`, `name`, `domain`, `groups`) — `dryRun: true` first. The real call
  always answers 202 with an `approvalId`: only the admin decides. Say so and wait.
- `golden-set-leakage`: the set shares utterances with trainable data. Report the overlapping versions; do not work
  around it.

## 2. Adopt into the project
- `adoptions.list kind=golden_set` — what the project already uses.
- `projects.adopt` with `body.version` = the golden set's `ver_…` and `ifMatch` = the project's etag
  (`projects.get`); `dryRun: true` first. Adopting a golden set re-runs the leakage check against what the project
  trained on and may refuse with `golden-set-leakage`. A replay set (another language, kept to measure forgetting)
  is adopted with `body.purpose: replay`; as a target it is refused with `locale-mismatch`.

## 3. The baseline
- `aliases.get name=baseline` — what evals compare against. Unset: the project's default base model.
- To move it (e.g. to a registered model): the version must be adopted, then `aliases.set name=baseline` with
  `body.version`, `ifMatch` = the alias's etag (omit when it does not exist yet). It is gated for everyone: it
  answers 202 with an `approvalId`. Report the id and wait; do not retry.

## 4. The gate (`gates.yaml`)
- `gates.get` — `exists`, the file, the effective gate with defaults filled in, and the departures from
  `defaults.yaml`. Its etag is the commit that last changed the file, or `defaults`.
- Keys: `primaryProfile` (the profile the gate reads, default `80ms`); `target: {goldenSets, rule: beat-baseline}`
  (the WER delta's whole interval below zero); `replay: {goldenSets, maxRegression}` (default 0.005 = 0.5 points; fails
  only when the interval excludes zero); `deletionsInsertions` (fail when deletions fall while insertions rise);
  `significance: {samples, level, seed}`. Golden sets are `golden-set/<name>` (a `*` matches several) or `ver_…` and
  must be adopted. Without golden sets in the file, the project's locales are targets and the rest replay.
- `gates.edit` with `body.content` (the YAML) and `ifMatch` = the etag of `gates.get`: `dryRun: true` first
  (`gate-config-invalid` lists every problem), then for real — for an agent it waits for a person's approval.

## 5. Run the eval matrix (`evals.new`)
Only `subject` is required — exactly one of `checkpointId` (`ckp_…`), `modelVersionId` or `baseModelVersionId`.
Everything else defaults from the project; add an axis only when the question needs it:
- `goldenSets` — default: those `gates.yaml` names, else every adopted golden set.
- `profiles` — latency profiles (`80ms`, `160ms`, `1120ms`, …); default `eval.matrix_profiles` the family declares,
  always with the primary profile. Replay golden sets are scored at the primary profile only.
- `decoding` — `[{boost: none}, {boost: "lang/<locale>/boost/<file>.txt@<commit>", weight}]`. The commit is the pack's
  `sha` from `langpacks.get locale=<locale>`. Boosting is evaluated, never assumed: compare boosted and unboosted
  cells; over-boosting shows as insertions (NeMo's optimum measured ≈ 0.5; above ≈ 0.7 listed words replace others).
- `augmentations` — the robustness axis: `[{profile: "augment/<name>.yaml@<commit>", seed?}]` (none is always
  included); target golden sets only; reported, not gated.
- `languages` — golden-set locale → the language the models decode it in, e.g. `{"sr-RS": "hr-HR"}` when the model has
  no prompt for the locale and was fine-tuned under a neighbour's (the run's `target_lang`). Applies to subject and
  baseline alike.
- `baseline` — default `@baseline`, else the default base model. Change it only when the person asked: the gate
  compares against the project's baseline.

Always `dryRun: true` first and report the plan in one line: cells cached / to compute and `estimate.gpuHours`
(`eval.gpu_hours_per_audio_hour` × audio hours of the cells to compute). Then the same body for real: 201 with the
eval (`evl_…`), or 202 with an `approvalId` when the estimate exceeds today's GPU budget. Cells already in the eval
records (same weights × golden set × normalizer × decoding × scorer, any project) are reused, so re-running with an
extra axis computes only the missing cells.

## 6. Read the result (`evals.get id=<eval>`)
- Poll until `status` is done or failed; `progress` counts cells (`cellsDone`, `cellsCached`, `cellsTotal`).
- One cell per role (subject, baseline) × golden set × profile × decoding (× augmentation). `summary`: `wer`, `cer`,
  `werNoPunct`, `sub`/`del`/`ins`, duration buckets, partial stability. Rates are fractions: 0.012 = 1.2 points.
- A subject cell's `delta`: `wer` `{value, low, high}` = subject − baseline with the paired bootstrap interval;
  `significant` when the interval excludes zero. Negative is better. An interval across zero is "no evidence", not
  "no change" — say so.
- CER languages (`eval.character_error_languages`: zh, yue, ja, th, lo, km, my) are compared and gated on characters:
  `delta.unit: char` means `wer` holds the CER delta and `del`/`ins` are not computed. Report them as CER.
- `metrics` (entity accuracy, latency to final) and `robustness` are reported, not gated.
- `worst=N` (with `cell`, `goldenSet`, `profile`, `role` filters) adds the N utterances with the most errors and
  their alignment — cite them as `@eval:<id>#cell:<evc_…>/utt:<index>`.

## 7. Gate, then register
- `evals.gate id=<eval>` with `ifMatch` = the `etag` of your last `evals.get` (`dryRun: true` computes without
  recording). Report `gate.verdict`, `gatesSha`, and each check: kind, golden set, delta with interval, state
  (passed, failed, inconclusive). The verdict is passed only when every check passed.
- Passed: `models.register` with `checkpointId` (and `evalId`) — dry run first to show the card; for an agent it
  waits for a person's approval. Failed: do not register; name the failing checks and suggest the next step (more
  replay for regressed replay sets, more steps or data for an inconclusive target).
- At the end: a short summary with `@eval:` references and the next step; `projects.note` what you learned.

Help: `help.get id=guides.evaluation`, `id=panels.eval`, `id=errors.gate-config-invalid`, `id=errors.gate-not-passed`,
`id=errors.golden-set-leakage`, `id=errors.eval-baseline-missing`.
