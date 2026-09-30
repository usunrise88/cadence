# Cadence spec — The five blocks, metrics, annotation, experiments, consistency check

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Block 1 — Data

Raw corpora become frozen, fingerprinted dataset versions in the base model's text style, with licences tracked and test sets fenced off.

Process:

1. Register a source with licence, languages and kind. Non-commercial and share-alike sources are marked eval-only until a person clears them.
2. Ingest through an SDP recipe from a mount: index in place by content hash, split stereo call recordings into one track per party (the bot's track is self-labelled from its TTS text), resample on read to 16 kHz mono, segment, voice activity detection; nothing is copied until a version is frozen.
3. Pseudo-label where there is no human text: an ensemble of models, a language-ID check and an agreement filter, following the Granary pipeline.
4. Normalise text to the base model's style — punctuation, casing, numbers — with per-language rules kept in the recipe.
5. Filter on duration, characters per second, empty or mismatched-language segments.
6. Split speaker- and source-disjoint; utterances in any golden set are excluded by fingerprint.
7. Freeze a dataset version (fingerprint over members and recipe SHA), run data-quality checks (duration histogram, silence and clipping share, transcript-length outliers), generate its dataset card, export Lhotse Shar shards.

Windows: Source, Storage, Dataset version (with the leakage check result and shard locations), Recipe, Pipeline run, Library, Inspector, Audio, Diff, Logs.

Agent tools: `sources.``new`, `pipelines``.run`, `datasets.preview` (hours per language after filters), `datasets.freeze`, `datasets.export`, `utterances.``search`; recipe files are edited in the worktree.

Gates: no licence, no ingest; freezing requires the leakage check to pass; only frozen versions can be exported or trained on.

## Block 2 — Training

A run is one optimisation stage from a pinned base or checkpoint, on a frozen mix, from a committed recipe, with a step budget — reproducible from those four references.

Process:

1. Choose the start (`init`): the base model at its pinned revision (`base`), or a checkpoint for a new stage (`checkpoint`); the start fixes the model family (R44).
2. Compose the mix: dataset versions, weights, temperature and a replay share of the model's other locales from `dataset/replay-base` (03 "Replay"); the preview shows hours per language.
3. Fill the recipe from the family's train-stage template: its train step kind with parameters from the family's `defaults.yaml` section and a step budget. For Nemotron: `init_from_nemo_model`, bf16, Noam schedule with the computed peak learning rate shown next to the scale factor, `target_lang` on every clip.
4. Calibrate with the family's calibrate step (OOMptimizer for Nemotron) on the target card under its memory cap (24 GB, fraction 0.5, on the shared 48 GB staging card); the measured seconds per step replace the table estimate (`basis: measured`).
5. Dry run: GPU-hours, card and duration; over budget goes to approval.
6. Launch: the run's train-stage pipeline goes to the queue, a worker of the family's runtime leases it, and loss, validation WER and learning rate stream in as metric batches through the worker protocol (for NeMo, a Lightning callback hands them to the worker).
7. Register checkpoints (neutral `checkpoint` artifacts with step, validation WER, family and weights hash) through the `checkpoint` output hook, keep the top k, optionally average them with the family's average step.
8. Continue by resuming (from the `training-state` artifact, same optimiser state; a window close or preemption resumes the same way) or starting a new stage from a checkpoint with an explicit peak learning rate.

Windows: Mix, Run, Metrics, Checkpoints, Logs, Queue & GPU, Recipes, Library.

Agent tools: `mixes.``new`, `mixes.preview`, `runs.c``alibrate, runs.``new` (with `dryRun`), `runs.resume`, `runs.``s``tage`, `jobs.pause`, `jobs.resume`, `jobs.cancel`, `jobs.wait`, `metrics.get`, `checkpoints.list`, `checkpoints.average`.

Gates: a run needs a frozen dataset version and a committed recipe SHA; the production card never takes training jobs; each session has a GPU-hour budget.

## Block 3 — Evaluation

A checkpoint is judged only in true streaming at deployment latency, on frozen golden sets it never trained on, against the current production model.

Process:

1. Keep golden sets frozen per language and domain — a telephone set from own calls, public sets such as FLEURS — each tied to one normalizer version.
2. Run the matrix: checkpoint × golden sets × latency settings (`[56,0]`, `[56,1]`, `[56,13]`), with the right `target_lang`, through NeMo's cache-aware streaming inference.
3. Score WER and CER with substitutions, deletions and insertions, per-utterance rows, duration buckets and a punctuation-insensitive companion score.
4. Compare every cell with the baseline, normally the model version in production, with a bootstrap 95% confidence interval on each WER delta; a change counts only when the interval excludes zero.
5. Apply the gate: thresholds for target languages, maximum regression for replay languages, and a check that fewer deletions were not bought with more insertions.
6. Publish the eval report; the worst utterances of failing cells become triage candidates.

Windows: Eval report, Diff, Audio, Inspector, Golden set, Library, Transcription (a manual test on a file or the microphone, shown live, nothing stored; R47–R50).

Agent tools: `goldenSets.list`, `goldenSets.``freez``e`, `evals.``new`, `evals.``get` (cells, worst-N utterances, filters), `evals.g``ate`, gates.edit, `baselines.``set`.

Gates: freezing a golden set and changing a baseline need a person; a new normalizer version forces a new baseline; runs cannot reference golden sets, so checkpoint selection can only use validation splits — the leakage the Kenyan Nemotron study reported is impossible by construction.

## Block 4 — Export and deployment

A gated checkpoint becomes a model version with verified artifacts, then climbs shadow → canary → production, each step reversible and the last two signed by a person.

Process:

1. Register a model version from a checkpoint whose eval run passed its gate; the registry generates its model card from lineage, eval records and departures from defaults.
2. Export to ONNX (cache-aware encoder, decoder, joint) and GGUF where a CPU target exists.
3. Parity check: ONNX against NeMo on a fixed sample; any WER difference beyond tolerance fails the export.
4. Build the Triton model repository with sequence batching; benchmark p50 and p95 latency per chunk size and concurrent streams on the staging card.
5. Shadow: the candidate transcribes real call audio on the staging card with no effect on calls and divergence from production is recorded — first as a nightly replay of call recordings from a mount, later in real time when Эра pushes utterances through the samples API.
6. Canary: the delivery script loads the candidate beside the production model and routes a traffic share to it; flywheel signals on both models decide whether the share grows or the canary is withdrawn.
7. Production: a person runs the delivery script Cadence generated on the production host and confirms the promotion in Cadence; the previous version stays loaded for instant rollback, also by script.

Windows: Model, Shadow, Approvals, Queue & GPU, Metrics (latency view), Library.

Agent tools: `models.register`, `models.export`, `models.parity`, `models.benchmark`, `deployments.``promo``te` (shadow directly; canary and production as approval requests), `deployments.rollback` (approval request).

Gates: parity and latency budget must pass; shadow must reach a minimum volume before canary; canary, production and rollback need a person, and production and rollback are executed by that person through the generated delivery script.

## Block 5 — Production flywheel

Production audio where the model is likely wrong is found, corrected and fed back as the next dataset version; the loop can run on a schedule with an agent doing the routine work and people sampling its decisions.

Process:

1. Capture samples under a sampling policy, with PII redaction and a retention limit: in v1 from Эра's call recordings on a read-only mount (nightly replay), later pushed by Эра in real time through `POST /projects/{p}/samples`.
2. Raise signals: streaming model vs offline oracle disagreement, low confidence, an LLM judge flagging an implausible transcript in the dialogue's context, operator corrections from Эра.
3. Pseudo-label with an ensemble; unanimous high-confidence items are auto-accepted, the rest enter the triage queue.
4. Triage by the admin or an agent; for languages nobody on the team speaks, a review batch is assigned to an invited native speaker with the reviewer role, who sees only that batch.
5. Package accepted items into a correction batch, registered as a source for the next dataset version.
6. On a schedule: low-rate continuation from the production checkpoint with replay → eval → gate → shadow, then a person decides on canary.

Windows: Triage queue (each item shows its signals), Shadow, Audio, Diff, Dataset version, Agent sessions (schedules and their runs appear here).

Agent tools: `samples.query`, `signals.list`, `triage.next`, `triage.``accept, triage.correct, triage.reject`, `corrections.package`, `schedules.``new`.

Gates: a set share of agent triage decisions (10% to start) goes to human review; agent-only reviewed items never enter golden sets; production audio past its retention limit is deleted with its derived rows.

## Task and streaming metrics

WER stays the headline, but a voice agent fails on a wrong phone number or a late end-of-turn, so every eval also reports the metrics that map to those failures.

| Metric | How it is computed | Needs |
| --- | --- | --- |
| Entity accuracy | Numbers, dates, phone numbers and amounts compared after inverse normalisation; names and addresses compared on annotated spans | Annotated spans in the golden set (optional; unannotated sets report the number classes only) |
| Latency to final | Time from utterance end (per-channel VAD, else the aligned reference) to the final that covers it, per latency profile, with audio fed at real-time pace; p50 and p95 (R54) | Streaming eval with timestamps |
| Emission delay | Time from a word's aligned end to its first appearance in a partial, as PR50 and PR90 (Yu et al., FastEmit, ICASSP 2021; R54) | Streaming eval keeps partials; aligned references |
| Partial stability | Unstable partial word ratio: the share of words shown in partials that the final changed or dropped (Shangguan et al., Interspeech 2020); edits per second reported beside it (R54) | Streaming eval keeps partials |
| End-of-utterance | Delay and false end-of-utterance rate against the VAD reference | Per-channel VAD on the golden set |
| Throughput | RTF and maximum concurrent streams within the latency budget | The benchmark step on the staging card |

All are scorer step kinds with versions; the default gate uses WER plus entity accuracy for the target locale, the rest are reported. Sources: the metrics beyond WER are Cadence recommendations, re-checked against the first production shadow.

## Annotation workflow

A telephone golden set is built from the project's own calls in batches with double annotation on a sample and adjudication; call recordings are stereo with one party per channel, which makes speaker separation free and the bot's own channel self-labelling.

1. Sampling: a policy stratifies calls by campaign, month, duration bucket and production confidence; the caller's channel is the annotation target; the agent's channel carries the bot's TTS text, which is attached automatically and used to detect crosstalk and overlap.
2. Batch: a fixed number of utterances, a guidelines version (a help article), a due date, an annotator (a reviewer invited with an annotation batch).
3. Annotate: the Triage panel in Annotate mode — audio with a channel switch, a transcript prefilled by the best available hypothesis, tags (noise, crosstalk, foreign language, unintelligible), keyboard-first; each item is done, skipped or flagged.
4. Double annotation: 10% of every batch plus all flagged items go to a second annotator blind.
5. Adjudication: disagreements above a threshold are resolved by the admin or a third reviewer; the inter-annotator WER of the batch is recorded, target ≤ 5%; a batch above target is not frozen.
6. Freeze: accepted items become a golden set version in the registry with the guidelines version in its card; training data from the same flow needs single annotation only.

Windows: Annotation batch (document: progress, agreement, adjudication queue), Triage in Annotate mode, Golden set. Agent tools: `batches.new`, `batches.get`, `batches.freeze` (approval). Entities: Annotation batch, Guidelines (a help article version).

## Experiments and sweeps

An experiment groups the runs that answer one question; a sweep generates those runs from a parameter grid under a budget; the Experiment document compares them in one table and names the best by the validation metric.

- Experiment: a project entity with a question, a fixed mix and base, and the runs that belong to it; every run created from it inherits the tag.
- Sweep: a declared grid or random set over recipe parameters (peak LR, warmup, replay share, augmentation profile), a run count and a GPU-hour cap; runs are queued sequentially on the project's slot; a sweep stops when the cap is reached.
- Comparison: parameters × metrics table with the departures from defaults highlighted; Compare N replaces Compare 2 inside an experiment; the best run is marked by validation WER and can be registered directly.
- Windows: Experiment (document). Agent tools: `experiments.new`, `experiments.get`, `sweeps.run`. Playbooks may open an experiment instead of a single run.

## Consistency check

Every step now has a window, a palette command, an API operation, an agent tool and an event; two passes found seventeen gaps, all closed.

| Step | Window | Command | API | Agent tool | Event |
| --- | --- | --- | --- | --- | --- |
| Register source | Source | New source | `POST /projects/{p}/sources` | `sources.``new` | `entity.source.{id}` |
| Add or scan a mount | Storage | Add mount (approval); Rescan | `POST /mounts`; `POST /mounts/{id}:scan` | `mounts.list`, `mounts.scan` | `mount.{id}` |
| Ingest, pseudo-label | Pipeline run, Recipe, Logs | Run pipeline | `POST /projects/{p}/pipelines/{name}:run` | `pipelines``.run` | `pipeline_run.{id}` |
| Preview filters and split | Dataset version (draft) | Preview dataset | `POST /projects/{p}/datasets:preview` | `datasets.preview` | — |
| Freeze | Dataset version | Freeze dataset version | `POST /projects/{p}/datasets/{id}:freeze` | `datasets.freeze` | `entity.dataset_version.{id}` |
| Materialise, export | Dataset version, Storage | Materialise; Export to Shar | `…:materialize`, `…:export` | `datasets.materialize`, `datasets.export` | `job.{id}` |
| Compose mix | Mix | Save mix as version | `POST /projects/{p}/mixes` | `mixes.``new`, `mixes.preview` | `entity.mix.{id}` |
| Calibrate batch | Run, Queue & GPU | Calibrate run | `POST /projects/{p}/runs:calibrate` | `runs.calibrate` | `job.{id}` |
| Launch run | Run, Queue & GPU | New run from mix | `POST /projects/{p}/runs` with `dryRun` | `runs.create` | `run.{id}.status` |
| Watch training | Metrics, Logs, Checkpoints | — | `GET /runs/{id}/metrics` | `metrics.get` | `run.{id}.metrics`, `job.{id}.log` |
| Control a job | Queue & GPU | Pause / resume / cancel | `POST /jobs/{id}:pause`, `:resume`, `:cancel` | `jobs.*`, `jobs.wait` | `queue`, `job.{id}` |
| Continue | Checkpoints, Run | Resume; New stage | `POST /runs/{id}:resume`; `POST /runs` with `initFrom` | `runs.resume`, `runs.``s``tage` | `run.{id}.status` |
| Average checkpoints | Checkpoints | Average checkpoints | `POST /runs/{id}/checkpoints:average` | `checkpoints.average` | `job.{id}` |
| Golden set | Golden set | Propose / approve freeze | `POST /projects/{p}/golden-sets`; `…:freeze` | `goldenSets.``freez``e` | `entity.golden_set.{id}`, `approvals` |
| Eval run | Eval report | Run eval matrix | `POST /projects/{p}/evals` | `evals.create` | `eval.{id}.progress` |
| Inspect results | Diff, Audio, Inspector | — | `GET /evals/{id}/results` | `evals.``get` | — |
| Try a model by hand | Transcription, Audio | Try a model (file, microphone) | `POST /projects/{p}/transcriptions`; audio and words over `GET /transcriptions/{id}/stream` (WebSocket) | — (a person speaks or listens; agents use `evals.new`) | `job.{id}` |
| Gate | Eval report | Evaluate gate; Edit gate | `POST /evals/{id}:gate`; `PATCH /projects/{p}/gates/{id}` | `evals.g``ate`, `gates.``edit` | `entity.gate.{id}` |
| Baseline | Eval report | Set eval baseline (approval) | `PATCH /projects/{p}/baseline` | `baselines.``set` | `approvals` |
| Register model | Model | Register model version | `POST /projects/{p}/models` | `models.register` | `entity.model_version.{id}` |
| Export, parity, benchmark | Model, Queue & GPU | Export; Parity check; Benchmark | `POST /models/{id}:export`, `:parity`, `:benchmark` | `models.*` | `job.{id}` |
| Shadow, canary, production, rollback | Model, Shadow, Approvals | Promote; Roll back | `POST /projects/{p}/deployments`; `…:rollback` | `deployments.``promo``te`, `deployments.rollback` | `deploy.{id}`, `shadow.{deployment}`, `approvals` |
| Capture and signals | Shadow, Triage queue | — | `GET /projects/{p}/samples`, `/signals` | `samples.query`, `signals.list` | `triage.new` |
| Triage | Triage queue, Diff, Audio | Accept / correct / reject | `PATCH /triage/{id}` | `triage.next`, `triage.``accept, triage.correct, triage.reject` | `entity.triage_item.{id}` |
| Package corrections | Triage queue, Dataset version | Package correction batch | `POST /projects/{p}/corrections:package` | `corrections.package` | `entity.source.{id}` |
| Schedule the loop | Agent sessions | New schedule (approval) | `POST /projects/{p}/schedules` | `schedules.``new` | `agent.sessions` |
| Create project | Project wizard, Project, Agent settings | New project | `POST /projects`; `POST /projects/{p}:bootstrap`; `PATCH /projects/{p}/agent-profile` | `projects.get` (creation is a human action) | `entity.project.{id}`, `job.{id}` |
| Adopt a registry asset | Library, Dataset version, Golden set, Model, Lineage | Adopt into project; Set alias | `POST /projects/{p}/adoptions`; `PUT /projects/{p}/aliases/{name}` | `registry.search`, `projects.adopt`, `aliases.set` | `entity.project.{id}` |
| Manage compute and secrets | Settings, Queue & GPU | Edit compute; Add secret (write-only) | `GET` / `PATCH /compute/{id}`; `POST /secrets` | `compute.list` (secrets have no agent tool) | `compute.{id}` |
| Sync templates and skills | Project, Recipe | Sync templates | `POST /projects/{p}:syncTemplates` | `projects.sync` | `job.{id}` |
| Record a learning | Project | Add note | `POST /projects/{p}/notes` | `projects.note` | `entity.project.{id}` |
| Find and understand | Palette, Library, Help, Inspector | Search everything; Help for this; Explain this | `GET /search`; `GET /help/{slug}`, `GET /help/context` | `search.query`, `help.get` | — |

Gaps found and closed:

1. The command palette covered training and deployment but not data, gates, model registration or the flywheel — fifteen commands added.
2. Batch calibration (OOMptimizer) was a process step with no tool or command — `runs.calibrate` added.
3. Gate thresholds were editable in the Eval report but had no API or tool — `gates.update` added.
4. Checkpoint averaging had a tool but no command — added.
5. Schedules were used by Block 5 but were not an entity — Schedule added.
6. Project, Mount and Pipeline run did not exist — added, with sections above.
7. Event topics differed between tabs — one scheme fixed in Real-time model.
8. The leakage check had no window — Dataset version now shows it.
9. Budgets existed only in Guardrails — now set per project.
10. Signals were computed but shown nowhere — Triage queue shows each item's signals.
11. Second pass: compute (hosts and cards) was implied by "one slot per card" but never an entity — Compute added at registry level.
12. Secrets were referenced by mounts, the wizard and the judge with nowhere to live — Secret added, write-only, never in agent context.
13. Evaluations of the same model on the same golden set were project-scoped and would be recomputed per project — Eval record cache added at registry level.
14. Production samples were project-scoped though the same production serves several projects — published as registry Sources after redaction.
15. Adopting a golden set after training could leak — adoption now re-runs the leakage check.
16. Copied templates and skills would drift from Cadence's — projects.syncTemplates added.
17. Approvals had no scope for registry-level actions — scope added; cache eviction had no cross-project fairness — quotas added; agents had nowhere to record learnings — projects.note added; a project could only pin one base model — several may be adopted.
