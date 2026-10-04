---
title: Evaluation — golden sets, baseline, gate and registration
summary: The evaluation loop end to end — freeze and adopt golden sets, set the baseline, write gates.yaml, run the eval matrix with evals.new, read cells, deltas and intervals, apply the gate and register the model.
contexts: [guide:evaluation]
---

## What this is

Evaluation answers one question with numbers you can defend: is this checkpoint better than what the project has, on
held-out audio, at the latency it will run at, without forgetting the other languages? Cadence answers it in seven
steps; each is one command, from the UI, the CLI or an agent.

1. **Golden sets.** A golden set (`golden-set/<name>`) is a frozen, held-out test set: an eval-only dataset version
   tied to one scoring normalizer version, so its WERs stay comparable across checkpoints, projects and time (R21).
   Register the dataset version eval-only, then **Freeze golden set…** (`goldenSets.freeze`): Check (dry run), then
   Freeze, which always waits for the admin's approval. Freezing refuses a set that shares utterances with trainable
   data (`golden-set-leakage`); from then on mixes and pipelines refuse any dataset that overlaps it.
2. **Adopt.** A project evaluates on the golden sets it adopted. On the Golden set document, **Adopt into project**
   (`projects.adopt`; also **Adopt…** on its Library row). The card dry-runs first: adopting re-runs the leakage
   check against what this project trained on, and a refusal lists the overlapping dataset versions before anything
   changes.
3. **Baseline.** Evals compare against `@baseline`; until it is set, against the project's default base model (R23).
   **Set as baseline** on a Model document, or on a model or base model row in the Library (`aliases.set
   name=baseline`), is gated for everyone: it
   answers an approval id, and the alias moves once a person approves it in Approvals.
4. **Gate.** `gates.yaml` in the project repository says what counts as better. The Gate section of the Project
   document shows the effective gate with its departures from `defaults.yaml`; **Edit gates.yaml** (also in the Eval
   report's Gate section), **Check** (`gates.edit` dry run), then **Commit to main**. For agents a commit waits for an
   approval.

   ```yaml
   primaryProfile: 80ms
   target: { goldenSets: [golden-set/fleurs-he], rule: beat-baseline }
   replay: { goldenSets: [golden-set/replay-golden-*], maxRegression: 0.005 }
   deletionsInsertions: true
   significance: { samples: 1000, level: 0.95, seed: 1 }
   ```

   Every key is optional. Without golden sets in the file, the eval's sets in the project's locales are targets and
   the rest replay. Named collections must be adopted (`gate-config-invalid` otherwise).
5. **Run the eval.** **Evaluate** on a checkpoint (Checkpoints), **Evaluate best** (Experiment) and **Run eval…**
   (Eval report) open the same `evals.new` form. The subject is
   a checkpoint, a registered model or a base model; every axis has a default:

   | Axis | Default | Use it for |
   | --- | --- | --- |
   | Golden sets | those `gates.yaml` names, else every adopted set | a quick look at one set |
   | Latency profiles | `eval.matrix_profiles` the family declares (`80ms`, `160ms`, `1120ms`), always with the primary | the latency trade-off |
   | Decoding | no boosting | a language pack's boost list at a weight, against no boosting (R24) |
   | Augmentations | none | robustness: an `augment/<name>.yaml` profile applied to the target golden sets |
   | Languages | each golden set's own locale | decode a locale in a neighbour's language (below) |

   **Plan** shows the plan: the cells already in the eval-record cache, the cells to compute and the GPU-hour
   estimate. **Start eval** queues the missing cells only. On an Eval report, **Re-run missing cells** opens the form
   filled with that eval's axes (after a failed step, only the cells without scores compute) and **Run eval…** with
   the defaults; add an axis and only what is new computes. Over today's GPU budget an agent's eval waits for an approval.
6. **Read the report.** The Eval report fills as cells finish: one cell per model × golden set × profile × decoding
   (× augmentation). A subject cell's delta is subject minus baseline in percentage points with a 95 % interval from
   a paired bootstrap that resamples whole speakers (or utterances). The interval is the answer: entirely below zero
   is better, entirely above is worse, across zero is not enough evidence either way. Duration buckets, S/D/I, the
   worst utterances (open them in Diff or Audio), entity accuracy, latency to final and robustness are reported, not
   gated.
7. **Gate, then register.** **Run the gate** (`evals.gate`) reads `gates.yaml` at main and records the verdict with the
   file's commit: target sets must beat the baseline at the primary profile, replay sets must not regress beyond
   `maxRegression` with an interval that excludes zero, and deletions must not be traded for insertions. The verdict
   is passed only when every check passed. Then **Register model…** (`models.register`) publishes the
   checkpoint with its eval and model card; registering needs a passed gate, and an agent's call waits for an
   approval. The new model version may become the next baseline (step 3).

### Languages written without spaces

Golden sets in the languages of `eval.character_error_languages` (zh, yue, ja, th, lo, km, my) are compared and gated
on the **character** error rate: a word error rate over unsegmented text counts whole sentences. Their deltas carry
`unit: char`, deletion and insertion deltas are not computed, and the report labels them CER.

### The languages map

A model fine-tuned for a locale it has no language prompt for is trained under a neighbour's prompt (the run's
`target_lang`, e.g. Serbian under Croatian). Evaluate it the same way: `languages: {"sr-RS": "hr-HR"}` decodes the
Serbian golden sets with the Croatian prompt for both the subject and the baseline. The Run eval… form prefills the
map when the subject's run used `target_lang`. The map is part of the decoding hash, so these cells never mix with
cells decoded in the set's own language.

A member of a macrolanguage needs no map: a Bokmål (`nb-NO`) or Nynorsk (`nn-NO`) golden set decodes on a base model
tagged `no` (Norwegian), as Standard Malay (`zsm`) does on `ms` — Cadence folds a small table of BCP 47
macrolanguage members before comparing a set's language with the model's `locale:` tags (Cadence recommendation;
spoken varieties such as Egyptian Arabic or Cantonese are not folded).

## Place in the loop

Train → **evaluate** → register → (phase 5) deploy. Every playbook that trains ends with an eval and a gate
(`finetune-from-dataset` steps 6–8).

## Fields and defaults

| Key | Default | Meaning |
| --- | --- | --- |
| `eval.primary_profile` | `80ms` | The profile the gate reads (`gates.yaml` `primaryProfile` overrides it) |
| `eval.matrix_profiles` | `80ms`, `160ms`, `1120ms` | Profiles an eval runs by default |
| `eval.normalizer` | `normalizer/basic` | Normalizer a golden set is frozen with when none is named |
| `eval.bootstrap_samples`, `eval.confidence`, `eval.bootstrap_seed` | 1000, 0.95, 1 | The interval on every delta |
| `eval.gpu_hours_per_audio_hour` | 0.025 | GPU-hours per audio hour of a cell to compute (the estimate) |
| `eval.character_error_languages` | zh, yue, ja, th, lo, km, my | Languages scored and gated on CER |
| `eval.artifact_retention_days` | 30 | Days a record's hypotheses, scores and metric scores stay after its last use; summaries, deltas and verdicts stay for ever |
| `gate.replay_max_regression` | 0.005 | Largest replay WER rise (0.5 points) before the gate fails |
| `gate.deletions_insertions` | true | Fail when deletions fall while insertions rise |

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Freeze golden set… | `goldenSets.freeze` | Dry run first; always the admin's approval |
| Adopt into project | `projects.adopt` | Golden set document; may refuse with `golden-set-leakage` |
| Set as baseline | `aliases.set` (`baseline`) | Model document, or a model or base model in the Library; answers an approval id |
| Edit gates.yaml | `gates.get`, `gates.edit` | Project document's Gate section; Check, then Commit to main; If-Match is the file's commit or `defaults` |
| Evaluate / Run eval… / Re-run missing cells | `evals.new` | Checkpoints, Experiment, Eval report; the plan first, then Start eval |
| Run the gate | `evals.gate` | Records the verdict and the `gates.yaml` commit |
| Register model… | `models.register` | Needs a passed gate |

## Playbooks

- **First gate of a project.** Adopt the target golden set and the replay golden sets, leave the baseline unset (the
  base model), check `gates.yaml` (or keep the defaults), run the playbook "Fine-tune from a dataset version".
- **Is boosting worth it?** Run eval… on the current model with decoding none and a boost list at weights 0.3 and
  0.5; compare general WER and look for insertions of listed terms in the worst utterances.
- **Make a passed model the baseline.** Register it, then Set as baseline; the next evals compare against it, and its
  cells come from the cache.
- **An old eval's utterances are gone.** Eval artifacts are kept by age (`eval.artifact_retention_days`, owner
  decision 2026-10-03): the cell says its per-utterance scores were evicted, the matrix, deltas and verdict stay. Run
  the eval again; cells whose artifacts were evicted are computed, not taken from the cache. A registered model's eval
  is never evicted (see *Freeing store space*).

## Sources

- docs/spec/04-blocks.md Block 3; docs/spec/02-domain-projects-registry.md "Evaluation entities"; R20–R24, R43, R54
  in docs/spec/08-resolutions.md.
- M. Bisani, H. Ney, "Bootstrap estimates for confidence intervals in ASR performance evaluation", ICASSP 2004; Z. Liu,
  F. Peng, "Statistical Testing on ASR Performance via Blockwise Bootstrap", arXiv:1912.09508.
