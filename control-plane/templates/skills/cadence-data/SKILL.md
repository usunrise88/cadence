---
name: cadence-data
description: Cadence block "data" workflow: mounts, sources and licences, ingest from a mount (data-ingest) or pseudo-labelling untranscribed audio (pseudo-label), preview, freeze with the leakage check, imports and exports — which MCP tools to call in what order, the approvals to respect, and what to report. Load when bringing a corpus into a Cadence project or preparing a dataset version.
---

# cadence-data

## Contract
- Act only through Cadence MCP tools; never edit the database, a mount or production.
- Every GPU-consuming command (`pipelines.run` of `pseudo-label` decodes on the card): call with dryRun first,
  compare with the project budget, then run the same request for real.
- Report results as entity references (`@dataset_version:ver_…`, `@pipeline_run:plr_…`, `@source:src_…`).
- Mounts, a source's training clearance and auxiliary models are a person's decision: you ask (the call answers an
  `approvalId`), say what the admin must decide, and wait. Never work around an approval or a licence.
- No licence, no ingest: a source without a usable licence is refused (`source-unlicensed`). Never invent a licence;
  read it from the corpus's `SOURCE.yaml` or ask.

## 1. Where the audio is: mounts
- `mounts.list`, `mounts.get` — registered mounts (`corpora`: `/cadence/corpora`, read-only; `exports`: writable).
  Corpora live at `mount://corpora/<source>/<revision>/` with a `SOURCE.yaml` (licence, url, revision).
- `mounts.scan` — what files a path holds (formats, durations, sidecars). `mounts.verify` — health.
- A new location: `mounts.new` (`name`, `kind` local/nfs/smb/s3/hf, `root`, `readOnly: true`) — always 202 with an
  `approvalId`; the admin decides. `mount-unhealthy` means the path is unreachable: report it.

## 2. Sources and licences
- `sources.list`, `sources.get` — every corpus Cadence knows, its licence, `trainingCleared` and its ingest history.
- `sources.new` (`name`, `licence` as an SPDX id, `kind`, `languages`, `url`) before any ingest.
- Training needs `trainingCleared: true`: ask for `sources.edit` with it (an approval) and wait. A dataset version
  registered while its source is not cleared is eval-only for good — clear first, then ingest.

## 3. Ingest: transcribed audio (`data-ingest`)
- `pipelines.list` — the project's pipelines. `pipelines/data-ingest.yaml`: `sdp_ingest` (index in place: segments with
  `mount://` URIs and canonical hashes, no audio copied) → `text_normalise` → `manifest_filter` →
  `speaker_disjoint_split` → `dataset_freeze` (a **draft** version).
- `pipelines.run name=data-ingest` with `params` per step: `index: {source, path: mount://corpora/<source>/<rev>,
  language}`, `normalise:` the training style of `langpacks.get` (`normalizer.yaml` training: `transliterate`,
  `casefold`, `punctuation`, `mappings`; `itn.yaml`), `draft: {name}`. Dry run first: report the plan, its departures
  from defaults and its warnings. Transcripts are `<stem>.txt` beside each file; calls carry a `<stem>.cadence.json`.
- A pre-segmented corpus (one utterance per file, as FLEURS) takes `index: {segmentation: file}`: each file stays one
  segment instead of being cut at its pauses. The index step leaves a corpus's `test/` split out (`exclude`): golden
  sets come from it; never point `path` at it. A draft that still holds a golden set's audio — even re-cut — is
  refused at freeze (golden-set-leakage).
- `pipelineRuns.wait` until done; on failure `pipelineRuns.get` names the step, `jobLogs.list` its log.

## 4. Ingest: untranscribed audio (`pseudo-label`)
- `pipelines/pseudo-label.yaml`: `sdp_ingest` → `segments_cut` (segments without text as a dataset) → members
  `nemotron_transcribe` (the base model), `whisper_transcribe`, `oasis_transcribe` (optional) and `lid_classify` →
  `pseudolabel_ensemble` (keeps a text when two members agree within `pseudolabel.max_pairwise_wer` and LID agrees;
  the rest is `pseudo-label:disputed`) → normalise → filter (drops disputes) → split → draft.
- Inputs: `model` (the base model's artifact, `baseModels.get`) and `normalizer` (the scoring normalizer,
  `normalizers.get`).
- The members name auxiliary models: `auxiliaries.list`; the project adopts each with `projects.adopt` (an approval
  the admin decides after the licence check, R26; `auxiliary-licence-refused` is final).
- OASIS is a service Cadence never starts. `auxiliaries.get` of `auxiliary/oasis` says `reachable`. When false, ask
  the person to start it on the host (`scripts/serve.sh ensemble no-300m` in the OASIS checkout) or to go on without
  it: the step is optional, the dry run warns `auxiliary-unavailable` and the run uses two members.
- Serbian: `transliterate: sr-Cyrl-Latn` on whisper, oasis and text_normalise (Whisper writes Cyrillic); the base
  model has no Serbian prompt, so nemotron's `target_lang: hr-HR`. OASIS writes lowercase without punctuation and
  wins when it agrees: look at the texts in the preview.
- `triage.list pipelineRun=<plr_…>` — the disputed segments; report how many and why (`reason`).

## 5. Preview and freeze
- `datasets.list` (the draft has `dataset.frozen: false`), `datasets.get` — quality checks, statistics, the card.
- `datasets.preview version=<ver_…>` (filters: duration, characters per second, languages, origins, splits) — hours
  per language and split. Report them with the quality warnings.
- `datasets.freeze version=<ver_…>` — dryRun runs the leakage check only; for real it cuts the segments into the
  content store (202 with a job; `jobs.wait`). `golden-set-leakage` lists the overlapping golden sets: stop and
  report — never filter a golden set's audio out by hand.
- Only frozen versions are mixed, trained on, exported or adopted (`dataset-not-frozen`).
- `utterances.search` — find utterances by text, source, speaker, origin, duration, split.

## 6. Imports, exports, storage
- Corpora already cut into utterances (NeMo manifest, Lhotse, HF dataset, folder + CSV, a Cadence bundle):
  `pipelines.run name=import` (`dataset_import`), frozen at import.
- `datasets.export` (`shar`, `nemo-manifest`, `cadence-bundle` to `mount://exports/…`; `hf-hub` is an approval),
  `exports.list|get`. `storage.get` — cache use and quotas; `datasets.materialize|evict` move shards.

## 7. Playbooks
- **Adapt a new language** (`adapt-new-language`) runs sections 1–5, then mix → calibrate → train → eval → gate.
- **Try Cadence** (`try-cadence`) imports two hours of FLEURS and runs the whole loop in about one GPU-hour.
- In a playbook session the plan ticks on the server; steps marked "a person" wait for one — say what they must do.

## What to report
- The source and its licence, the pipeline run, hours per split after filters, disputed pseudo-labels, quality
  warnings, the leakage result and the frozen version — then the next step (a mix, or what a person must decide).
