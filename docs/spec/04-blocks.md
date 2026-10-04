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

As built (phase 4, 2026-10-03/04; plan `docs/review/2026-10-03-phase-4-plan.md`, entities in 02 "Storage and mounts" and "Registry", step kinds in 03; departures in `ROADMAP.md` "Phase 4 notes"):

1. Source and mount. `sources.new` (`POST /registry/sources`) registers the corpus eval-only; clearing it for training is `sources.edit` (registry approval for agents, `registry-changes`), and `sources.get` lists the clearing history and every ingest. The audio lives on a mount — `mounts.new` is a registry approval the admin decides (`mount-registration`, for people too); the stand's first is `corpora` (`local`, root `/mnt/corpora`, read-only), with `exports` (`/mnt/exports`, writable) beside it.
2. Ingest. `pipelines/data-ingest.yaml` runs `sdp_ingest` (walk, decode, stereo split with roles, the bot's channel labelled from the TTS script with origin `model:tts-script`, 16 kHz, energy VAD per channel, segments with `mount://` URIs and canonical hashes; no audio copied) → `text_normalise` → `manifest_filter` → `speaker_disjoint_split` → `dataset_freeze` in mode draft, which registers a draft dataset version (`frozen: false`) and records each utterance's mount URI. The engine refuses a step whose `x-cadence.registry: source` parameter names a missing, archived or unlicensed source (`source-unlicensed`), and the draft hook checks again.
3. Pseudo-label where there is no human text: `pipelines/pseudo-label.yaml` cuts the segments (`segments_cut`), runs the two members (`whisper_transcribe@1` and the `oasis_transcribe@1` service, required), `lid_classify@2` (VoxLingua107 in runtime `omni`; Whisper's detected language is the second opinion), and `pseudolabel_ensemble@2` (kept when the members agree within `pseudolabel.max_pairwise_wer` 0.15 and LID agrees; the label is Whisper's cased, punctuated text when OASIS's spoken form agrees, `pseudolabel.prefer_written_form`). Every member is an adopted `auxiliary` version (approval `auxiliary-adoption`). Owner decision 2026-10-04: the base model is no longer a member (on the stand it kept 490 of 2 944 FLEURS segments with Whisper, no better than Whisper alone: WER against the references 0.336 against Whisper's 0.121), so OASIS is required — without its service the dry run refuses (`auxiliary-unavailable`), and the ensemble refuses fewer than `pseudolabel.min_members` (2) members wired rather than agreeing with itself.
   Untranscribed calls for annotation: `pipelines/calls-ingest.yaml` is `sdp_ingest` alone (`channels: split`, roles from the sidecar) and ends at the `segments` artifact, the frame of `batches.new` (`segments: b3:…`); `data-ingest` cannot draft them (owner decision 2026-10-04).
4. Triage. Disagreements get origin `pseudo-label:disputed`, never reach training (`manifest_filter` drops them) and are indexed as triage items (`triage.list`, event `triage.item_added` on `triage.new`); a person resolves each in the Triage panel with `triage.accept|correct|reject` (agents never do).
5. Preview and freeze. `datasets.preview` answers utterances and hours per language and split after filters from the registry alone; `datasets.freeze` runs the leakage check against every golden set (`golden-set-leakage`; dry run: the check only — by identity and by fingerprint, where a segment's `file-b3`, the canonical hash of the whole file it was cut from, matches a golden utterance's `audio-b3`, so a golden FLEURS file re-cut by VAD from the `corpora` mount is refused; `sdp_ingest@2` also leaves a corpus's `test/` split out by default), checks the project quota, then reruns the draft's `dataset_freeze` in mode cut — one 16 kHz WAV per utterance in the content store plus Lhotse `MonoCut` manifests in shards of 2 000, quality checks, the dataset card — and sets `frozen: true` (event `dataset_version.frozen`).
6. Synthetic calls stand in for real calls: `calls-synth-sr` on `corpora` (40 stereo G.711 μ-law calls, Serbian caller, OmniVoice bot, kind `synthetic`) exercises the stereo split, bot self-labelling, per-channel VAD and the annotation workflow; the real-call golden set waits for real calls.
7. Noise bank: `noise_mine@1` reads an ingest's `segments` and cuts clips where no track of the file has speech (a margin from any speech, digital silence and loud clips dropped) into a frozen `noise_bank` version tagged `mined`.
8. Export and import: `datasets.export` (Shar, NeMo manifest, Cadence bundle to the `exports` mount or the content store; the Hugging Face Hub behind the `hub-export` approval) and `dataset_import@4` (Lhotse cuts and Shar, Cadence bundles, NeMo manifests; 03 "Interoperability").

Agent tools as built: `sources.new|list|get` (`sources.edit` waits for approval), `mounts.list|get|scan|verify` (`mounts.new` waits for approval), `storage.get`, `pipelines.run`, `datasets.preview`, `datasets.freeze`, `datasets.materialize`, `datasets.evict`, `datasets.export` (Hub: approval), `exports.list|get`, `utterances.search`, `auxiliaries.list|get`, `triage.list` (05 "What the agent can do").

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

Built in phase 2 (stream R; `control-plane/internal/runs`, guide `guides.runs`):

- `runs.new` (dry run: the estimate; real: `201` with the run) pins the start (`init: base` with a base model version,
  or `init: checkpoint` with a `ckp_…`, whose run becomes the parent), renders the mix revision as a `mix` artifact
  (format `cadence.mix/1`: the resolved `input_cfg` with group weights, sampling probabilities and dataset artifacts;
  its hash is the content hash R13 asks for) and a `base_model` artifact, and starts the recipe — the project's
  `train-stage` pipeline (`training.pipeline`) at a commit — with the run id. The recipe must hold exactly one step of
  the base model's family's train kind (`recipe-mismatch`); step kinds come from the family descriptor, never from
  a family name (`family-unavailable` when unpublished). Eval-only datasets are refused.
- Status mirrors the pipeline run (`queued`, `running`, `paused`, `done`, `failed`, `cancelled`) through an engine
  observer; events `run.created` and `run.status_changed` on `run.{id}.status`. `runs.get` answers the stage
  timeline (steps with role, state, attempts, OOM retries and batch scale), `currentJobId` for Pause, Resume and Stop
  (`jobs.pause|resume|cancel`), final metrics, departures per step, the parent and the config diff against it.
- `runs.calibrate` runs the family's calibrate kind as a one-step pipeline on the mix; the `calibration` hook caches
  seconds per step (±, batch sizes, bucket configuration) per base model, card class, memory cap and precision, and
  estimates switch to `basis: measured`. Before any calibration the defaults table answers (`basis: table`).
- Checkpoints (`ckp_…`) come from the `checkpoint` hook with step, validation WER, family and weights hash; the top
  `training.keep_top_k` (3) by validation WER are kept; `checkpoint.saved` on `run.{id}.checkpoints`.
  `checkpoints.average` runs the family's average kind over chosen checkpoints; the result is a checkpoint of the run
  (`kind: averaged`).
- `runs.resume` continues a cancelled or failed run's train step from its last `training-state` (a new attempt with
  `overrides.resumeFrom`); `runs.stage` starts a new run from a checkpoint (default the best kept) with an explicit
  peak learning rate.
- `metrics.get` (`GET /metrics/{runId}`) answers series on the step, epoch, wall-time or GPU-hours axis, binned
  server-side to `maxPoints` with min and max per bucket (R53), `afterStep` for live append, checkpoint marks.
- GPU budgets: spend is lease time on GPU cards per project per day and per agent session
  (`budgets.agent_gpu_hours_per_session`); an agent's spending command whose estimate exceeds what is left waits for
  an approval (the first real gated command); the daily digest shows the metered spend.

## Block 3 — Evaluation

A checkpoint is judged only in true streaming at deployment latency, on frozen golden sets it never trained on, against the baseline — the base model until something is promoted.

Process (phase 3; plan `docs/review/2026-10-02-phase-3-plan.md`, entities in 02 "Evaluation entities"):

1. Keep golden sets frozen per language and domain — a telephone set from own calls (phase 4), public sets such as FLEURS, the replay locales' FLEURS test sets — each an eval-only dataset version tied to one scoring normalizer version (`goldenSets.freeze`, approval).
2. Run the matrix (`evals.new`): models × golden sets × latency profiles (`80ms`, `160ms`, `1120ms`; replay sets at the primary profile only) × decoding configs (boost none or a list with a weight, R24), through the family's transcribe step in real streaming with the right `target_lang`; base models are materialised into checkpoints first. Cells already in the eval-record cache are linked, not recomputed (03 "The eval pipeline").
3. Score WER and CER with substitutions, deletions and insertions, per-utterance rows with alignment, duration buckets, the punctuation-insensitive companion score `werNoPunct` and partial stability (03 "Scorers and metrics").
4. Compare every cell with the baseline's cell of the same golden set, profile and decoding: a paired blockwise bootstrap gives each WER delta a 95 % confidence interval (below); a change counts only when the interval excludes zero.
5. Apply the gate (`evals.gate`): the project's `gates.yaml` turns the primary-profile cells into a verdict (below).
6. Register a checkpoint whose verdict passed (`models.register`, R22); the worst utterances of failing cells become triage candidates (phase 5).

**The gate.** `gates.yaml` lives in the project repository, versioned like recipes; people and agents edit it through
commits, and `gates.get|edit` read and write it through the recipes service (decision 3). A missing file or key falls
back to `defaults.yaml` `gate.*` and `eval.*`; an invalid file answers `gate-config-invalid`.

```yaml
primaryProfile: 80ms                      # R20: the gate reads this cell only (eval.primary_profile)
target: { goldenSets: [golden-set/fleurs-he], rule: beat-baseline }            # gate.target_rule
replay: { goldenSets: [golden-set/replay-golden-*], maxRegression: 0.005 }     # gate.replay_max_regression: 0.5 WER points
deletionsInsertions: true                 # gate.deletions_insertions
significance: { samples: 1000, level: 0.95, seed: 1 }                          # eval.bootstrap_samples, confidence, bootstrap_seed
```

Golden sets are named by collection (a `*` matches several) and resolve to the versions the project adopted. Checks,
all at the primary profile against the baseline's cells:

| Check | Passes when | Does not pass when |
| --- | --- | --- |
| Target (each target golden set, rule `beat-baseline`) | The WER delta (candidate − baseline) < 0 and the interval's upper bound < 0 | Anything else: `failed`, or `inconclusive` while the interval spans zero |
| Replay (each replay golden set) | Not failed | `failed` when the delta > `maxRegression` and the interval's lower bound > 0 |
| Deletions and insertions (target cells) | Not failed | `failed` when the deletion-rate delta's interval lies below zero and the insertion-rate delta's above zero |

The verdict is `passed` only when every check passed, else `failed`; each check reports `passed | failed |
inconclusive` with its numbers (delta, interval, baseline and candidate WER). The eval records the verdict with the SHA of the `gates.yaml` it read; `models.register`
needs a passed verdict (`gate-not-passed`). `evals.gate` without the baseline's cells answers `eval-baseline-missing`.
Entity accuracy, boosted-term recall (R24) and a maximum degradation under the `telephony` augmentation profile are
reported, not gated, until `gates.yaml` grows those checks (07 "Open questions").

As built (stream E; `internal/evals` `gates.go`, `verdict.go`):

- `gates.get` reads `gates.yaml` at main's head with the defaults where it is silent; its ETag is the commit that last
  changed the file, or `defaults` when there is none. `gates.edit` (`PATCH /projects/{p}/gates`) validates strictly
  (unknown keys, ranges from `defaults.yaml`, named collections the project has not adopted → `gate-config-invalid`)
  and commits to main with If-Match that commit or `defaults`. An agent's call waits for an approval (preset rule
  `evaluation-gates`); an agent may also change the file in its session worktree, which reaches main only through
  accepted session changes.
- `evals.gate` (`POST /evals/{id}:gate`, If-Match the eval's revision) reads decoding variant 0 and augmentation 0 at
  the primary profile, matched by name, else by latency in milliseconds; without such cells the `primaryProfile`
  check fails. When `gates.yaml` names no golden sets, the eval's golden sets in the project's locales are targets and
  the rest replay; an eval without a target golden set fails the target check. A dry run computes the verdict without
  recording it; a significance different from the eval's recomputes the deltas. The verdict is stored on the eval with
  `gatesSha` (empty for the defaults) and announced as `eval.gated` on `entity.eval.{id}`.
- Gate integrity (audit 2026-10-02): the verdict fails a `baseline` check when the eval's baseline is neither the
  project's `@baseline` nor its default base model (a baseline the request chose proves nothing the project agreed
  on), and fails a target or replay check for every golden set `gates.yaml` names — resolved to the project's
  adopted version — that the eval did not score at that version ("not in this eval; run evals.new with it"). Leaving
  a regressing replay set out of an eval therefore fails the gate instead of passing it.
- A `passed` verdict of the checkpoint's latest gated eval is what `models.register` needs (02 "Model versions"): an
  explicit `evalId` may name an older passed eval only while the latest gated eval (by `gated_at`) passed too, else
  `gate-not-passed`.

**Significance (R54).** Every WER delta gets a paired bootstrap interval: 1 000 resamples (`eval.bootstrap_samples`)
at 95 % (`eval.confidence`) with a fixed seed (`eval.bootstrap_seed`), computed in Go from the `scores` rows of the
candidate and baseline cells. Resampling is blockwise over the golden set's `groups` — whole calls, else speakers,
else utterances — because utterances of one call are correlated (Liu & Peng, arXiv:1912.09508); Bisani & Ney's
1 000-sample bootstrap (ICASSP 2004) stays the method, only the resampling unit changes. Deletion and insertion rates
get intervals the same way.

Windows: Eval report, Golden set, Diff, Audio, Inspector, Lineage, Library, Language pack ("test a phrase"), Transcription (a manual test on a file, the microphone or an utterance span, shown live, nothing stored; R47–R50).

Agent tools (R1 names): `goldenSets.list`, `goldenSets.get`, `goldenSets.freeze` (approval), `normalizers.list`, `normalizers.get`, `evals.new`, `evals.get` (cells, deltas with intervals, worst-N utterances, filters), `evals.list`, `evals.gate`, `gates.get`, `gates.edit`, `models.register` (approval for agents), `models.list`, `models.get`, `langpacks.list`, `langpacks.get`, `langpacks.edit`, `boost.edit`, `registry.lineage`, `aliases.set` (name `baseline`, approval). Not a tool: `transcriptions.new` (tag `media`; agents test models through evals).

Gates: freezing a golden set and changing the baseline need a person; a new normalizer version forces a new baseline; runs cannot reference golden sets and no training input may overlap one by fingerprint (`golden-set-leakage`), so checkpoint selection can only use validation splits — the leakage the Kenyan Nemotron study reported is impossible by construction.

Emission delay (phase 4, stream L; R51, R54): `align_reference@1` (runtime `omni`, the adopted `auxiliary/omniasr-ctc-1b`)
gives a golden set word timings for its reference once, by hand (`pipelines/align-reference.yaml`); the newest
`alignment` artifact of the golden set's dataset is attached to it (`golden_set.aligned`). Evals then feed it to
`latency_score@3`, which reports emission delay PR50 and PR90 beside latency to final — or `n/a` with the reason
(no alignment, a language the aligner does not cover) and never an invented number. Reported, not gated.

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

Agent tools: `models.register` (phase 3, Block 3), `models.export`, `models.parity`, `models.benchmark`, `deployments.``promo``te` (shadow directly; canary and production as approval requests), `deployments.rollback` (approval request).

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
| Latency to final | Time from utterance end (per-channel VAD, else the aligned reference; a frame-VAD model in phase 3) to the final that covers it, per latency profile, with audio fed at real-time pace; p50 and p95 (R54) | Streaming eval with timestamps |
| Emission delay | Time from a word's aligned end to its first appearance in a partial, as PR50 and PR90 (Yu et al., FastEmit, ICASSP 2021; R54) | Streaming eval keeps partials; aligned references |
| Partial stability | Unstable partial word ratio: the share of words shown in partials that the final changed or dropped (Shangguan et al., Interspeech 2020); edits per second reported beside it (R54) | Streaming eval keeps partials |
| End-of-utterance | Delay and false end-of-utterance rate against the VAD reference | Per-channel VAD on the golden set |
| Throughput | RTF and maximum concurrent streams within the latency budget | The benchmark step on the staging card |

All are scorer step kinds with versions; definitions and the phase each arrives in are in 03 "Scorers and metrics". The phase-3 gate uses WER (04 "Block 3"); entity accuracy for the target locale was to join it when `entity_score@1` exists — it exists since phase 3 (stream R) and is reported, not gated, like latency to final, until `gates.yaml` grows the check (07 "Open questions"); the rest are reported. Sources: the metrics beyond WER are Cadence recommendations, re-checked against the first production shadow.

## Annotation workflow

A telephone golden set is built from the project's own calls in batches with double annotation on a sample and adjudication; call recordings are stereo with one party per channel, which makes speaker separation free and the bot's own channel self-labelling.

1. Sampling: a policy stratifies calls by campaign, month, duration bucket and production confidence; the caller's channel is the annotation target; the agent's channel carries the bot's TTS text, which is attached automatically and used to detect crosstalk and overlap.
2. Batch: a fixed number of utterances, a guidelines version (a help article), a due date, an annotator (a reviewer invited with an annotation batch).
3. Annotate: the Triage panel in Annotate mode — audio with a channel switch, a transcript prefilled by the best available hypothesis, tags (noise, crosstalk, foreign language, unintelligible), keyboard-first; each item is done, skipped or flagged.
4. Double annotation: 10% of every batch plus all flagged items go to a second annotator blind.
5. Adjudication: disagreements above a threshold are resolved by the admin or a third reviewer; the inter-annotator WER of the batch is recorded, target ≤ 5%; a batch above target is not frozen.
6. Freeze: accepted items become a golden set version in the registry with the guidelines version in its card; training data from the same flow needs single annotation only.

Windows: Annotation batch (document: progress, agreement, adjudication queue), Triage in Annotate mode, Golden set. Agent tools: `batches.new`, `batches.get`, `batches.freeze` (approval). Entities: Annotation batch, Guidelines (a help article version).

As built (phase 4, stream A; `internal/annotation`, migration 0038; R27; help `guides/annotation`):

- Entities: batches `anb_` (`annotation_batches`), items `bit_` (`annotation_items`), annotations `ann_` (one per
  person and item). Guidelines are not a help article but Markdown in the project repository,
  `annotation/guidelines/<name>.md` (`annotation.guidelines`, default `default`); a batch pins the repository's head
  commit at creation and the golden-set card cites path and commit. Reviewers see the path and commit, not the text
  (not served yet).
- `batches.new` (`POST /projects/{p}/batches`, dry run first) samples from a frame — a dataset version's segments
  (usually the draft `pipelines/data-ingest` ended in) or a `segments` artifact — over one role
  (`annotation.target_role`, `caller`), stratified by campaign, month, duration (`annotation.duration_edges_s`) and
  confidence (`annotation.confidence_edges`) with largest-remainder shares, at least one per stratum, reproducible
  with `seed`; `annotation.batch_size` (200), `due_days` (14). Purpose `golden-set` (double annotation and
  adjudication) or `training` (single annotation). Each item keeps its window (segment ± `annotation.context_s`, 2 s,
  every channel), the other party's turns, a prefill (the best hypothesis the frame has) and the end-of-utterance gap
  from per-channel voice activity (the caller's last speech end to the bot's next speech start).
- Annotate (`annotations.new`, people only): done, skipped or flagged, with text, tags (noise, crosstalk, foreign,
  unintelligible) and entity spans (character offsets, for `entity_score`). Annotators pull work in their own hashed
  order; `annotation.double_share` (10 %) of the items plus every flagged item get a second annotator, blind — an
  annotator cannot revise once another transcript is in. After `annotation.max_skips` (2) skips without a transcript
  an item is excluded; agreed items tagged foreign or unintelligible are excluded too.
- Agreement: two transcripts are compared after a neutral fold (case-folded, punctuation stripped; not the project's
  scoring normalizer); a WER above `annotation.adjudicate_wer` (0: any word difference) makes the item `disputed`.
  Item states `pending → agreed | disputed → adjudicated | excluded`; `batchItems.accept` (admin or an invited
  adjudicator) sets the final transcript, tags and spans or excludes it. The batch's inter-annotator WER (first two
  transcripts of double items) must be ≤ `annotation.max_iaa_wer` (0.05) for a golden-set batch to freeze
  (`annotation-agreement-low`); every item must be resolved (`batch-incomplete`).
- `batches.freeze` is an approval the admin decides for everyone (preset rule `annotation-batch-freeze`): the accepted
  items become a draft dataset version (human transcripts, spans, the target channel cut from the mount) which is cut
  into the content store; a golden-set batch's version (`dataset/<goldenSet>-annotated`) is then frozen as
  `golden-set/<goldenSet>` through the phase-3 `goldenSets.freeze` path, its card carrying the guidelines commit and
  the inter-annotator WER. The ingest draft must be eval-only for a golden set from calls. Freezing revokes the
  batch's reviewers. Events `annotation_batch.created|item_annotated|item_adjudicated|reviewer_invited|freezing|
  frozen|failed` on `entity.annotation_batch.{id}`.
- Reviewers (06 "Authentication and access"): `invitations.new|list` (admin) mint a `cri_` link; `auth.accept`
  redeems it for a session that reaches that batch's document, items and their audio windows only.
- The same Triage panel resolves disputed pseudo-labels (`triage.accept|correct|reject`) into human transcripts.
- Not built: peaks at ingest and freeze (computed on first view), the tile pyramid job, end-of-utterance in the
  dataset manifest, serving the guidelines text, a Playwright journey (`ROADMAP.md` "Phase 4 notes").

## Experiments and sweeps

An experiment groups the runs that answer one question; a sweep generates those runs from a parameter grid under a budget; the Experiment document compares them in one table and names the best by the validation metric.

- Experiment: a project entity with a question, a fixed mix and base, and the runs that belong to it; every run created from it inherits the tag.
- Sweep: a declared grid or random set over recipe parameters (peak LR, warmup, replay share, augmentation profile), a run count and a GPU-hour cap; runs are queued sequentially on the project's slot; a sweep stops when the cap is reached.
- Comparison: parameters × metrics table with the departures from defaults highlighted; Compare N replaces Compare 2 inside an experiment; the best run is marked by validation WER and its checkpoint can be registered with `models.register` once an eval of it passed the gate (R22 moves registration into phase 3).
- Windows: Experiment (document). Agent tools: `experiments.new`, `experiments.get`, `experiments.list`, `sweeps.run` (dry run first; the GPU-hour cap is enforced). IDs `exp_` and `swp_`, events `entity.experiment.{id}` (phase 3, plan stream X). Playbooks may open an experiment instead of a single run.
- As built (phase 3, stream X; help `guides/experiments`):
  - Tables `experiments` (`exp_`) and `sweeps` (`swp_`), and `runs.experiment_id`, `runs.sweep_id` (migration 0027); a sweep whose estimate exceeds its cap answers `sweep-over-cap`.
  - The experiment pins a mix revision and a base model version; its runs carry `experimentId` (and `sweepId`). `runs.new` with `experiment` inherits both and refuses others; runs start from the base model (a stage from a checkpoint is not an experiment run).
  - `sweeps.run` (`POST /experiments/{id}/sweeps:run`, If-Match the experiment's revision): grid (every combination, `runs` cuts it) or random (`runs` points drawn with `seed` from values or a min–max range, linear or log); parameters are the train step's own, checked by the engine against their x-cadence ranges, plus `replayShare`, rendered into the run's mix artifact while the revision stays. Defaults `sweeps.*` (mode, `max_runs` 16, `random_runs` 8, `gpu_hour_cap` 8, seed).
  - Every point is prepared as a run (`runs.new`'s path); the sum of the estimates must fit the cap (`sweep-over-cap`) and is what the policy weighs (`gpu-spend`). The approval of a sweep covers its runs: later runs are started by the server without another policy check, even on a later day, so the cap (not the daily budget) is the bound on what a sweep spends.
  - One sweep runs per project at a time. A run that ends (done or failed) starts the next point when the GPU-hours the sweep's runs used plus the next run's fresh estimate fit the cap; otherwise the sweep stops (`stopped`, the rest `skipped`). A running run is never cut by the cap. Cancelling the running run cancels the sweep. The recipe is read at the commit the first point read.
  - `experiments.get` compares the runs: swept parameters, then any train-step parameter a run departs from defaults with; per run the best validation WER and its checkpoint, GPU-hours and the latest eval with its verdict; `best` (lowest validation WER) says whether `models.register` accepts its checkpoint now. Charts: parameter × validation WER scatter and parallel coordinates over the swept parameters (R53).

## Consistency check

Every step now has a window, a palette command, an API operation, an agent tool and an event; two passes found seventeen gaps, all closed.

| Step | Window | Command | API | Agent tool | Event |
| --- | --- | --- | --- | --- | --- |
| Register source | Source, Library | New source; Edit source (clear for training) | `POST /registry/sources` (`sources.new`); `sources.list|get|edit|archive` under `/registry/sources` | `sources.new`, `sources.list`, `sources.get`, `sources.edit` (approval) | `entity.source.{id}` |
| Add, scan, check a mount | Storage | Add mount (approval); Rescan; Check health | `POST /mounts` (`mounts.new`, 202 approval for everyone); `GET /mounts`, `GET /mounts/{id}`; `POST /mounts/{id}:scan`, `:verify` | `mounts.list`, `mounts.get`, `mounts.scan`, `mounts.verify` (`mounts.new` waits for the admin) | `mount.{id}` (`mount.created|scanned|health`), `job.{id}` |
| Cache and quotas | Storage | — | `GET /storage` (`storage.get`) | `storage.get` | `entity.artifact.{hash}` (`artifact.evicted|restored`) |
| Ingest, pseudo-label | Pipeline run, Recipe, Logs | Run pipeline | `POST /projects/{p}/pipelines/{name}:run` (`data-ingest`, `pseudo-label`) | `pipelines.run` | `pipeline_run.{id}` |
| Auxiliary models | Library, Inspector | Adopt into project (approval) | `auxiliaries.list|get` under `/registry/auxiliaries`; `projects.adopt` | `auxiliaries.list`, `auxiliaries.get`, `projects.adopt` (approval) | `entity.project.{id}`, `approvals` |
| Resolve a disputed pseudo-label | Triage, Audio | Accept / correct / reject | `GET /projects/{p}/triage` (`triage.list`); `POST /triage/{id}:accept`, `:correct`, `:reject` | `triage.list` (resolving is a person's) | `triage.new` (`triage.item_added`), `entity.triage_item.{id}` |
| Preview filters and split | Dataset version (draft) | Preview dataset | `POST /registry/datasets:preview` (`datasets.preview`, body names the version) | `datasets.preview` | — |
| Freeze | Dataset version | Freeze dataset version | `POST /registry/datasets:freeze` (`datasets.freeze`, body `{version}`; 202 with the cut job) | `datasets.freeze` | `entity.dataset_version.{id}` (`dataset_version.frozen`), `pipeline_run.{id}` |
| Search utterances | Dataset version, Source, Palette | — | `GET /registry/utterances:search` (`utterances.search`) | `utterances.search` | — |
| Materialise, evict | Dataset version, Storage | Materialise; Evict | `POST /registry/datasets:materialize`, `:evict` (body `{versionId}`) | `datasets.materialize`, `datasets.evict` | `job.{id}`, `entity.artifact.{hash}` |
| Export | Dataset version | — (no export UI yet) | `POST /registry/datasets:export` (`datasets.export`; Hub: 202 approval); `GET /projects/{p}/exports`, `GET /exports/{id}` | `datasets.export`, `exports.list`, `exports.get` | `entity.export.{id}` (`export.started|done`), `job.{id}` |
| Archive a registry version | Library, Dataset version | Archive version | `POST /registry/versions:archive` (`versions.archive`, admin) | — (agents never archive, `no-deletes`) | `entity.<kind>.{id}` |
| Annotation batch | Annotation batch, Triage (Annotate) | New batch (dry run first); Freeze batch (approval) | `POST /projects/{p}/batches` (`batches.new`), `GET /projects/{p}/batches`, `GET /batches/{id}`, `POST /batches/{id}:freeze` | `batches.new`, `batches.list`, `batches.get`, `batches.freeze` (approval) | `entity.annotation_batch.{id}`, `approvals` |
| Annotate, adjudicate | Triage (Annotate), Audio | Done / skip / flag; Adjudicate | `GET /batches/{id}/batch-items[/{item}]`; `POST …/batch-items/{item}/annotations` (`annotations.new`); `POST …/batch-items/{item}:accept` (`batchItems.accept`) | `batchItems.list`, `batchItems.get` (annotating is a person's) | `entity.annotation_batch.{id}` |
| Invite a reviewer | Annotation batch | Invite reviewer | `POST /batches/{id}/invitations` (`invitations.new`, admin), `GET …/invitations`; `POST /auth:accept` (tag `auth`) | — (admin only) | `entity.annotation_batch.{id}` (`annotation_batch.reviewer_invited`) |
| Audio tracks | Audio, Triage | — | `GET /registry/utterances/{id}/tracks` (`tracks.get`, tag `media`) | — (agents read no raw audio) | — |
| Compose mix | Mix | Save mix as version | `POST /projects/{p}/mixes` | `mixes.``new`, `mixes.preview` | `entity.mix.{id}` |
| Calibrate batch | Run, Queue & GPU | Calibrate run | `POST /projects/{p}/runs:calibrate` | `runs.calibrate` | `pipeline_run.{id}`, `job.{id}` |
| Launch run | Run, Queue & GPU | New run from mix | `POST /projects/{p}/runs` with `dryRun` | `runs.new` | `run.{id}.status` |
| Watch training | Metrics, Logs, Checkpoints | — | `GET /metrics/{runId}`; `GET /runs/{id}`; `GET /projects/{p}/checkpoints` | `metrics.get`, `runs.get`, `checkpoints.list` | `run.{id}.metrics`, `run.{id}.checkpoints`, `job.{id}.log` |
| Control a job | Queue & GPU | Pause / resume / cancel | `POST /jobs/{id}:pause`, `:resume`, `:cancel` | `jobs.*`, `jobs.wait` | `queue`, `job.{id}` |
| Continue | Checkpoints, Run | Resume; New stage | `POST /runs/{id}:resume`; `POST /runs/{id}:stage` | `runs.resume`, `runs.stage` | `run.{id}.status` |
| Average checkpoints | Checkpoints | Average checkpoints | `POST /runs/{id}/checkpoints:average` | `checkpoints.average` | `pipeline_run.{id}`, `run.{id}.checkpoints` |
| Experiment and sweep | Experiment | New experiment; Run sweep (dry run first) | `experiments.new`, `experiments.get`, `experiments.list`; `sweeps.run` | `experiments.*`, `sweeps.run` | `entity.experiment.{id}`, `run.{id}.status` |
| Golden set | Golden set, Library | Freeze golden set (approval) | `goldenSets.freeze` (registry scope); `goldenSets.list`, `goldenSets.get` under `/registry/golden-sets` | `goldenSets.freeze`, `goldenSets.list`, `goldenSets.get` | `entity.golden_set.{id}`, `approvals` |
| Scoring normalizer | Golden set, Language pack, Library | — (versions are seeded or frozen, R21) | `normalizers.list`, `normalizers.get` under `/registry/normalizers` | `normalizers.list`, `normalizers.get` | — |
| Eval run | Eval report | Run eval matrix | `POST /projects/{p}/evals` (`evals.new`, with `dryRun`) | `evals.new` | `eval.{id}.progress`, `entity.eval.{id}`, `pipeline_run.{id}` |
| Inspect results | Eval report, Diff, Audio, Inspector | — | `GET /evals/{id}` (`evals.get`); `GET /projects/{p}/evals` (`evals.list`) | `evals.get`, `evals.list` | — |
| Try a model by hand | Transcription, Audio | Try a model (file, microphone) | `POST /projects/{p}/transcriptions` (`transcriptions.new`, tag `media`); audio and words over `GET /api/transcriptions/{id}/stream` (WebSocket, 06 "Media") | — (a person speaks or listens; agents use `evals.new`) | `job.{id}` |
| Listen to an utterance | Audio, Diff, Eval report | Play; loop a span | Utterance audio and peaks (tag `media`, R25; 06 "Media") | — (agents never read raw audio) | — |
| Gate | Eval report, Recipe | Evaluate gate; Edit gate | `POST /evals/{id}:gate` (`evals.gate`); `gates.get`, `gates.edit` (the project's `gates.yaml` through the recipes service) | `evals.gate`, `gates.get`, `gates.edit` | `entity.eval.{id}`, `recipe.{path}` |
| Baseline | Eval report, Library | Set eval baseline (approval) | `PUT /projects/{p}/aliases/baseline` (`aliases.set`, R8) | `aliases.set` | `entity.project.{id}`, `approvals` |
| Register model | Eval report, Model, Experiment | Register model version | `POST /projects/{p}/models:register` (`models.register`, writes a registry version; needs a passed gate, approval for agents); `models.list`, `models.get` under `/registry/models` | `models.register`, `models.list`, `models.get` | `entity.model.{id}` |
| Lineage | Lineage, Library | — | `GET /registry/{id}:lineage` (`registry.lineage`; `direction`, `depth`, `limit`) | `registry.lineage` | — |
| Language pack, boost lists | Language pack | Edit language pack; Edit boost list | `langpacks.list`, `langpacks.get`, `langpacks.edit`, `boost.edit` (`/projects/{p}/langpacks/{locale}[/boost/{domain}]`, files in `lang/<locale>/`) | `langpacks.list`, `langpacks.get`, `langpacks.edit`, `boost.edit` | `recipe.{path}` |
| Export, parity, benchmark | Model, Queue & GPU | Export; Parity check; Benchmark | `POST /models/{id}:export`, `:parity`, `:benchmark` | `models.*` | `job.{id}` |
| Shadow, canary, production, rollback | Model, Shadow, Approvals | Promote; Roll back | `POST /projects/{p}/deployments`; `…:rollback` | `deployments.``promo``te`, `deployments.rollback` | `deploy.{id}`, `shadow.{deployment}`, `approvals` |
| Capture and signals | Shadow, Triage queue | — | `GET /projects/{p}/samples`, `/signals` | `samples.query`, `signals.list` | `triage.new` |
| Triage (flywheel, phase 5) | Triage queue, Diff, Audio | Accept / correct / reject | `POST /triage/{id}:accept`, `:correct`, `:reject` (built in phase 4 for pseudo-labels) | `triage.next`, `triage.``accept, triage.correct, triage.reject` | `entity.triage_item.{id}` |
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
3. Gate thresholds were editable in the Eval report but had no API or tool — `gates.update` added (now `gates.edit` over `gates.yaml`, R1).
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
18. Phase 4 as built (2026-10-04): the data rows follow the contract — registry-level paths for sources and dataset
    actions (`/registry/sources`, `/registry/datasets:<verb>` with the version in the body), `mounts.verify` for the
    planned `mounts.health` (`health` is not a vocabulary verb), and new rows for the cache, auxiliary models,
    pseudo-label triage, annotation batches, reviewer invitations, archive and the audio tracks. Export has an API and a
    tool but no window yet (`ROADMAP.md` "Phase 4 notes").
