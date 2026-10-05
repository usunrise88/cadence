# Cadence spec — UI: first run, search, help, panel catalogue, workspaces, commands, backend contract, build order, risks

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## First run and progressive disclosure

A newcomer reaches a running training job from three fields and a button; everything else is a default they can see, question and change later, never a prerequisite.

### The recommended path

1. First launch shows one screen: "Try Cadence" (the smoke project) or "New project". Nothing else until a project exists.
2. The wizard opens in Recommended mode: three fields — project name, language, where the call recordings are (a mount to attach, or skip). Base model, agent, instructions template, repository (internal), storage and budgets are filled from `defaults.yaml` and shown as a collapsed summary with "Customise".
3. Bootstrap runs; the Project home opens with the five-block checklist and one highlighted button: the "Adapt a new language" playbook, with its estimate.
4. The playbook runs as an agent session in the Chat panel; the user watches the loop happen in the same panels they will use later.
5. Every finished step ends on a next-step bar; the checklist ticks itself from events.

### Progressive disclosure

- Every form has a Recommended and an Advanced view; Advanced fields show description, default, source and safe range from the step schema, and a value outside the range is a warning, not a block.
- "Why this default?" is a popover on every field, rendered from the same schema.
- Departures from defaults appear as chips on the entity header and in Compare.
- "Reset to recommended" on every form, pipeline and gate.
- Empty states name the next loop step and offer Ask agent with a prefilled prompt.
- Ask agent buttons are never blank: the prompt includes the entity reference and the intent ("evaluate this checkpoint on the phone golden set").
- A first-launch tour is three steps, dismissible, and never returns; help is otherwise inline.
- Guardrails are safe by default: nothing spends GPU without a dry run the user has seen, and nothing touches production without a modal.

### Windows

Project home (the Project document): checklist of the five blocks with counts and the next playbook; Getting started (tool, shown until the first gate passes): setup steps and their state; the wizard is a modal flow, not a panel.

## Search

One search box finds anything Cadence knows — entities, utterance text, logs, notes, help — with free text plus typed qualifiers, the same query language for people and agents, and results that open where the work is.

### Entry points

| Where | Keys | What it searches |
| --- | --- | --- |
| Palette, plain text | Ctrl/Cmd+K | Entities across the registry and the current project; `>` prefix switches to commands, `?` to help (the VS Code convention) |
| Library filter bar | — | The same query language, as a persistent list with saved views |
| Find in panel | Ctrl/Cmd+F | Inside the focused panel: log lines, utterances in a Diff or Triage list, rows in a table |
| Agent and CLI | `projects.search` tool (R1; `cadence projects search`) | The same index and grammar; the tool description carries the qualifier list |

### Query language

Free text plus qualifiers, autocompleted as chips: `kind:run status:running`, `lang:he-IL`, `wer<10`, `actor:agent`, `updated:>2026-09-01`, `project:hebrew` or `scope:all`, `tag:telephony`, `alias:@production`, `signal:disagreement`, `golden:phone-v1`, `dur:2..8`. Text over transcripts is normalised per locale before matching — for Hebrew niqqud is stripped and spelling variants (ktiv male and haser) are folded — so a phrase from a call is found however it was written.

### Behaviour

- Results are grouped by kind, ranked by recency within the current project first; Enter opens the document, Space previews in Inspector, Ctrl/Cmd+Enter opens Compare with the pinned selection.
- "Open as list" turns any search into a Library view; saved searches are per user per project and appear in the palette.
- The index is fed from the event stream, so a frozen dataset or a finished run is searchable within seconds.
  The indexer keeps its own cursor over the outbox (`event_cursors`), reloads each entity an event names and
  upserts its document; a restart resumes after the last committed batch. A kind joins the index by one row in
  the registration table (`internal/search.Sources`); help articles are indexed at start.
- Scope follows the credential: without a scope qualifier a search covers the current project, the registry (with
  registry read) and help; `project:<slug>` searches the named projects, `scope:all` every project the credential
  reaches — a project-bound token never sees another project's work, and registry hits need registry read. An
  unknown qualifier is an `invalid-query` error listing the qualifiers, never free text.
- Logs are structured JSON lines, so log search filters by field (`level:error step:train`), not only by substring.
- Version 1: Postgres full-text search with trigram matching for typos and identifiers; version 2 adds embedding search over transcripts and notes ("utterances like this one") with pgvector, still self-hosted.

## Help

Help is a first-class part of the product: the same markdown that teaches people teaches the agents, every panel, field and error links to its page, and the pages that describe fields are generated from the schemas so they cannot go stale.

### Where help appears

| Trigger | What opens |
| --- | --- |
| `?` with a panel or field focused (outside text inputs) | The Help panel at that panel's or field's section |
| `?` prefix in the palette | Search over help articles |
| Hover on a glossary term (dotted underline) | Popover with the definition and a link |
| An error | Its `type` URI in the problem+json body points at the help page for that error (RFC 9457) |
| Ctrl/Cmd+/ | Keyboard shortcut sheet, generated from the command registry |
| "Explain this" on any entity | A read-only agent session with the help page and the entity attached |

### The Help panel

A tool panel that follows the focused panel unless pinned. Every article has the same shape: what this is · where it sits in the loop · fields and defaults (rendered from the step schema: description, default, source, range) · commands (live buttons, greyed with the reason when disabled) · related playbooks · sources.

### Authoring and sources of truth

- Articles are markdown in the Cadence repository (`docs/help/`), versioned and shipped inside the binary; no external calls.
- Field documentation is not written by hand: it renders from `x-cadence` in the step schemas, the same text the MCP tool descriptions carry.
- Every panel manifest declares `help`, every step kind declares `help`, every error type has a page; CI fails when one is missing.
- The same `docs/help/` is referenced by the Cadence skills, so an agent explaining a screen and a person reading it get one answer.
- "What's new" per release and the decision log of the spec are help articles too.

### Windows

Help (tool, new); search has no panel of its own — it lives in the palette, the Library filter bar and Find in panel.

## Panel catalogue

Version 1 has 33 panels: 12 documents that open in the centre and 21 tools that follow the active document or are pinned to an agent session (Triage became a tool in phase 4). Notifications are part of the chrome, not a panel.

| Panel | Kind | Shows | Main actions | Live topic |
| --- | --- | --- | --- | --- |
| Run | Document | Config diff against the parent run, status, stage timeline, final metrics | Pause, resume, stop; resume from checkpoint; new stage from checkpoint with an explicit peak LR | `run.{id}.``status`, `run.{id}.``metrics` |
| Eval report | Document | One eval (`evals.get`): the matrix of golden sets × latency profiles (`80ms`, `160ms`, `1120ms`; R43) × decoding, filling live; per cell WER, CER and `werNoPunct` with the delta to the baseline, its 95 % interval and the gate colour (glyph as well as colour); cached cells marked; the gate verdict with each check, its numbers and the `gates.yaml` SHA. Charts through `@/shell/charts` (R53): matrix heatmap, forest plot of WER deltas with intervals, S/D/I stacked bars, WER by duration bucket, per-utterance WER ECDF, partial stability; latency to final, entity accuracy, the robustness matrix and WER against latency as their scorers land. An utterance table (worst first, filters) whose rows open in Diff and Audio | Run the gate; open a cell in Diff; open a row in Audio; re-run missing cells; register the model (passed verdict); set the baseline (approval); open `gates.yaml` | `eval.{id}.progress`, `entity.eval.{id}` |
| Dataset version | Document | Draft, frozen or archived; the freeze's leakage result and pipeline run; what it holds (size, languages, sources, licence, split rule, recipe, splits); quality checks; statistics charts from `datasets.get` (R53): hours by language and split, duration with p5/p50/p95 and the preview's bounds, characters per second, level, source sample rates, hours by origin and channel role, and end-of-utterance gaps of call recordings (`stats.eou`: the gap histogram with p50/p90 marks, how many segments had a gap and how many overlapped; only when the version has gaps measured); shards with location and pin; used by; utterances (`utterances.search`) | Preview with filters; freeze (dry run first); adopt (licence and locale checks); archive (admin) | `entity.dataset_version.{id}` |
| Mix | Document | Groups, weights, temperature, replay share; preview of hours per language; agents' drafts (dashed outline, diff on hover) and "agent editing" presence | Save (a new revision; 412 conflict notice with reload / reapply); accept or revert an agent draft; launch a run with this mix (phase 2) | `entity.mix.{id}` (drafts arrive here) |
| Triage | Tool (centre of the Triage workspace, renderer `always`) | Queue mode: the project's disputed pseudo-labels (`triage.list`; production samples from phase 5) with the reason, every member's text and mean WER, the segment's window of its call; Annotate mode: an open annotation batch one item at a time — the call window with a channel switch, level and voice-activity lanes, the other party's turns (the bot's TTS script), a prefilled transcript, tags and entity spans, keyboard-first | Accept, correct, reject (people only); annotate: done, flag, skip | `triage.new`, `entity.triage_item.{id}`, `entity.annotation_batch.{id}` |
| Model | Document | A model version: the checkpoint it publishes, gate verdict and eval, model card, lineage, "used by"; from phase 5 its exports per latency profile with parity and benchmark verdicts (latency per concurrency level as a chart, R53), deployments per target and slot with their stage, the target's promotion chain and a pending delivery's receipt box | Adopt as base model; set as baseline (approval); export, parity check, benchmark; deploy to shadow; promote (confirm modal), roll back; download the delivery bundle; confirm delivery (paste the receipt) (phase 5) | `entity.model.{id}`, `deploy.{id}` |
| Recipe | Document | A recipe file (SDP config, pipeline, mix, training or eval YAML, augmentation profile) with its commit history; open session branches and their diffs against `main`; agent edits stream in as a live diff | Edit; accept or revert an agent draft; accept or discard session changes (three-way diff on conflict); commit | `recipe.{path}` |
| Source | Document | Licence, kind, languages, URL, training clearance; ingest history (`ingests`: each version with step kind, draft or frozen, utterances, hours); utterances (`utterances.search`); the clearing history on the Activity tab | Clear for training or make eval-only (`sources.edit`; an agent's call waits for a person); archive (admin); open `pipelines/data-ingest` | `entity.source.{id}` |
| Golden set | Document | A golden set version: locale, domain, the dataset version and scoring normalizer version it pins, utterances, hours, resampling unit (`groups`), leakage check result, "used by" (projects, gates, evals), lineage; a sample of utterances that open in Audio | Freeze from an eval-only dataset version with a normalizer (approval, admin); adopt into the project; open the normalizer | `entity.golden_set.{id}`, `approvals` |
| Library | Tool | The registry (sources, dataset versions, golden sets, models, normalizers, templates) and the project's work (runs, mixes, recipes) with search, tags and a this-project / all filter | Open as document; compare two; adopt into project; set alias | — |
| Queue & GPU | Tool | Job queue per card; GPU memory and compute; who holds the training slot | Reorder, pause, cancel | `queue`, `gpu` |
| Metrics | Tool | Loss, validation WER, LR, gradient norm, throughput and GPU memory of the active run by step, epoch, wall time or GPU-hours; checkpoint marks; pinned runs overlaid (R53) | Pin a run; change smoothing, x-axis and scale; show as table | `run.{id}.metrics` |
| Checkpoints | Tool | Checkpoints of the active run with validation WER | Evaluate; export; new stage from here | `run.{id}.metrics` |
| Logs | Tool | Streaming log of the active job | Follow, search, copy | `job.{id}.log` |
| Diff | Tool | Reference vs hypothesis for the selected utterance from the cell's `scores` rows (`ops`), after the scoring normalizer, with the raw texts on request; substitutions, deletions and insertions marked by glyph as well as colour; Hebrew right to left with bidi isolation; the utterance's audio view with the hypothesis word track | Step through the cell's utterances (worst first); open in Audio; copy | — |
| Audio | Tool | The audio view (`@/shell/audio`, R51, R52) of the selected utterance or span, played through signed segments (R25): waveform, spectrogram, model input and emissions, reference and hypothesis word tracks (as built: the reference track from the golden set's newest alignment, `words.get?goldenSet=`; an unaligned reference as text), streaming timeline; long audio from stored multi-level peaks and the server tile pyramid built on first view; renderer `always`; floating by default; play-only for reviewers | Play, loop a span, change speed, zoom, choose tracks, spectrogram preset and colormap, attach the span to Chat, export TextGrid, CTM or WebVTT | — |
| Transcription | Tool, floating by default | A manual test (R47–R50; 06 "Media"): a file, the microphone (device picker, level meter with clipping mark, raw-microphone toggle) or an utterance span; one to three targets (checkpoint, model version, base model; staging deployment from phase 5), each with latency profile, boost list and language; telephony simulation; live partial and final words per target, Hebrew right to left, with p50/p95 time to final and the real-time factor; the queue place while waiting for a card; then the audio view with every target's words and the streaming timeline; nothing is stored | Choose the input; go live; finalize; stop; add a target; blind compare; type a reference (WER and diff on the page); copy the text | `job.{id}` |
| Inspector | Tool | Properties of the current selection: config values, manifest row, metadata | Copy a value; open its source | — |
| Shadow | Tool | Divergence between the production and candidate models on live calls. As built (phase 5 · D4): follows the active deployment or the active Model document's shadows (else a picker); the comparison model, hours replayed against `deploy.shadow_min_hours`, the next replay; divergence per night with its interval (a chart); the nights with why one was skipped or failed and when its texts left after retention; the selected night's most divergent segments | Replay now (`shadowReplays.new`, dry run first); open a segment in Diff (candidate against the comparison model's text) and Audio (by its `b3:` hash, while the night's texts are kept); mark it for triage (stream F2) | `shadow.{``deployment``}`, `deploy.{id}` |
| Chat | Tool, one per agent session | Streaming transcript: replies with entity links, plan checklist (a playbook's chain is the initial plan), tool-call cards with dry-run estimates and diffs, shell cards, approval cards; header chip with the session kind (interactive, playbook, scheduled, read-only) and state; budget meter for turns, tokens and GPU-hours | Send; stop; attach the selection; approve or deny; pause or resume; merge session changes | `agent.session.{id}` |
| Agent sessions | Tool | All sessions of the project by kind and state (running, waiting approval, paused, done, failed) with driver, model, budget use, pending approvals and merge state of the session branch; scheduled runs included | New session (Claude Code or opencode; interactive or from a playbook); open its Chat; pause; resume; merge or discard session changes | `agent.sessions` |
| Approvals | Tool | Pending requests from agents, automations and registry actions: action, estimate, requester, scope (project or registry), context; decisions taken from Telegram appear here with their channel | Approve once or for the session; deny; open the requesting Chat | `approvals` |
| Storage | Tool | Mounts with kind, root, health (reachable, free space, throughput), last scan, utterance URIs and blob copies; the cache against its water marks; per-project quotas; dataset versions least recently used first — cached or evicted, mount copies, why pinned (`storage.get`) | Add mount (approval, admin decides); rescan; check health; evict or materialize a dataset version | `mount.{id}`, `entity.artifact.{hash}`, `storage` |
| Pipeline run | Tool | Any pipeline run: steps with status, inputs and outputs, per-step logs; parameters rendered from the step schema | Retry a step; open an output; open the pipeline file in Recipe | `pipeline_run.{id}` |
| Project | Document | Overview of one project: locales, base model revision, repository and branch, agent profile, budgets and today's use, gates, mounts, decision log | Edit any wizard choice; open Agent settings; archive | `entity.project.{id}` |
| Agent settings | Tool | The project's agent profile: driver, model, permission preset with a preview of the rendered `.claude/settings.json` and `opencode.json`, auto-merge policy for session branches, draft policy per entity kind; raw config editors with schema validation; `AGENTS.md` editor | Save (commits to the project repository); reset to template; test-launch a session | `entity.project.{id}` |
| Lineage | Tool | Graph around the selection from `registry.lineage`, both ways: sources → dataset versions → mix and recipe SHA → run → checkpoint → model version → deployments; golden set → dataset version and normalizer; "used by" for registry entries (golden sets, normalizers, models, dataset versions) | Open any node as document; copy version id | — |
| Settings | Tool, admin only | Registry-level configuration: compute (hosts, cards, memory caps, allowed job kinds, availability windows; a table per host, each card edited in its own dialog), agents (the Claude Code subscription token and opencode providers: write-only values, status, expiry, Verify, default opencode model; 2026-09-30), secrets (names only, write-only values), catalogues (base models, agent models, instruction templates, permission presets), policies (retention, PII redaction, default budgets, cache quotas), notification rules and the Telegram bot, credentials (API keys, active sessions, invitations), backup status, the audit log (read-only; filter by actor, operation, project), deployment targets (phase 5 · D4, read-only: staging targets with health and served models, delivery targets with what they serve, slots and chain head, the instance's public signing key to copy) | Edit; add secret; create API key; revoke; set, verify and archive agent credentials; sync templates across projects | `compute.{id}`, `entity.agent_credential.{id}` |
| Getting started | Tool, until the first gate passes | Setup checklist with state: mount attached, project created, first dataset frozen, first run done, first gate passed; each step with its playbook or command | Run the step; dismiss | `entity.project.{id}` |
| Help | Tool, follows focus unless pinned | The article for the focused panel, field or error: what it is, its place in the loop, fields and defaults from the schema, live commands, playbooks, sources | Search help; pin; Explain this (agent) | — |
| Language pack | Document | One locale of the project (`langpacks.get`): the scoring normalizer it references (a registry version, R21) and the training text style, inverse normalisation, transliteration, LID config, boost lists with weights, golden-set recipe, README; commit history | Edit (`langpacks.edit`); edit a boost list (`boost.edit`); test a phrase with and without boosting (a one-utterance eval, then a two-target transcription once Transcription lands); sync from the shipped pack | `recipe.{path}` |
| Annotation batch | Document | Sample and strata, progress by item state, inter-annotator WER against the target, end-of-utterance gaps, reviewers and invitations, the adjudication queue, guidelines (path and pinned commit), the freeze | Invite a reviewer (admin; link shown once); adjudicate (`batchItems.accept`); check, then freeze into a golden set or training data (approval) | `entity.annotation_batch.{id}` |
| Experiment | Document | Question, fixed mix and base, sweep grid or random set and GPU-hour cap, parameters × metrics table of its runs with departures from defaults highlighted, best run by validation WER; charts: parameter against metric scatter, parallel coordinates for sweeps (R53) | Run the sweep (dry run first); compare N; evaluate the best; register it (`models.register`, passed gate) | `entity.experiment.{id}`, `run.{id}.status` |

As built (phase 3, stream U; help `panels.eval`, `panels.diff`, `panels.golden-set`, `panels.model`,
`panels.lineage`, `panels.language-pack`):

- Eval report: the selection is `cell:<evc_id>` or `cell:<evc_id>/utt:<index>`; Ask agent attaches
  `@eval:<id>#cell:…/utt:…`. Deltas are shown in percentage points (the API's rates are fractions); a cell's tone
  comes from its interval (better, worse, inconclusive), with a glyph beside the colour. Charts: delta heatmap, forest
  plot of deltas with intervals, S/D/I bars, WER by duration bucket, the per-utterance WER ECDF (from the `worst` rows,
  at most 200 per cell: the whole set up to 200 utterances, else the labelled tail) and entity accuracy of the
  selected cell; a folding "Streaming" section for the selected cell's golden set (WER against latency per model, the
  primary profile marked and the subject's interval taken as the baseline's WER plus the delta's interval; latency to
  final as p50/p95/max, with each cell's unavailable reason; partial stability) and a folding "Robustness" section
  (degradation per golden set × augmentation × model × profile). Both start folded while empty. Not drawn: confusion
  pairs, WER by SNR, bandwidth or speaker, emission delay (`evals.get` has no such data).
- Golden set: the leakage row states the rule (the freeze checked it; nothing to recompute per view).
- Language pack: Commit is enabled only for exactly the text that passed Check (the server's dry run).
- Built after the waves: opening an utterance row of the Eval report or Diff in Audio (stream A); evaluation setup
  from the UI (audit fix F4): Adopt into project on the Golden set document and its Library row (`projects.adopt`;
  the card dry-runs, so the leakage refusal shows with the overlapping dataset versions), Set as baseline on the Model
  document and on a model or base model row in the Library (`aliases.set` `baseline`, approval-gated: the approval
  id is shown), the Project home's Gate section (`gates.get|edit`: the effective gate with its departures, Check then
  Commit to main with If-Match the file's commit or `defaults`; the Eval report's Gate section links to it), and one
  Run eval form (`evals.new`: adopted golden sets, the family's profiles, decoding variants with boost lists and a
  weight, augmentation profiles, the languages map prefilled from a run's `target_lang`, the baseline; Plan, then
  Start eval) behind Checkpoints' Evaluate, the Experiment's Evaluate best and the Eval report's Run eval… and Re-run
  missing cells (filled with the eval's axes). The Project home's blocks count mixes, runs, adopted golden sets and
  evals with the newest verdict and `@baseline`; Getting started derives its run and gate steps from `runs.list` and
  `evals.list` and retires once a gate passed.
- Not built: "test a phrase" in the Language pack, and the Playwright smoke of the Eval workspace.

As built (phase 4, streams M, R, A; help `panels.storage`, `panels.source`, `panels.dataset-version`,
`panels.triage`, `panels.annotation-batch`):

- Storage: a singleton tool in the Ops workspace's right column; every action dry-runs first and shows what blocks it
  (pins, shards on no mount). The Settings panel keeps its "Content store" section (phase 2) beside it.
- Source and Dataset version are documents of the Data workspace, opened from the Library (sources are listed with
  Adopt and the this-project / all filter), a Source's ingest history, a Mix and links. Charts and the utterance
  table come from `@/shell/data` (R53 charts over `dataset.stats`, `UtteranceSearch`), which panels use instead of
  each other. The dataset card renders as Markdown (phase 4 tail): `texts.get` (`GET /registry/texts/{hash}`) serves
  a blob of the content store only when a registry version names it as text to read — today a dataset version's
  `card.hash` — capped at 256 KiB, and `@/shell/markdown` renders it static and sanitised (raw HTML dropped, links in
  a new tab without the opener). The Exports section lists the version's exports in the open project
  (`exports.list`, live on `entity.export.*`: format, state with its pipeline run, target or Hub link, files, bytes,
  mount copies) and **Export…** (header command `datasets.export`) opens a card: format, target (default
  `storage.export_mount`, the content store, or a writable path mount with a directory) or the Hub repository and
  visibility; **Plan** is the dry run, **Export** sends exactly the planned request, a Hub push answers an approval.
- Triage is a tool, not a document: it follows the project, not an entity. Queue keys: `Enter` accept, `E` correct
  (`Ctrl+Enter` saves), `Backspace` reject, `↑`/`↓` move. Annotate keys (no browser-reserved keys): `Ctrl+Enter` done,
  `Ctrl+Shift+Enter` flag, `Alt+S` skip, `Alt+P` play/pause, `Alt+R` replay the segment, `Alt+C` switch channel,
  `Alt+1`…`Alt+4` tags, `Alt+E` mark the selected words as an entity. A reviewer's invitation opens the same view
  restricted to its batch; your queue is your own, and double items reach a second annotator blind.
- Annotation batch is a document of the Eval workspace (opens from the Triage panel, links and `batches.new`).
  The Annotate view and the Annotation batch document show the guidelines' text (phase 4 tail): **Guidelines**, a
  collapsed pane above the item, reads `guidelines.get` (`GET /batches/{id}/guidelines`) — the file at the commit the
  batch pinned, not main — rendered by `@/shell/markdown`. A reviewer's session may call it for its own batch only;
  nothing else of the repository is served to it.
- Needs materialize (phase 4 tail): a dry run of `runs.new`, `runs.stage` or `pipelines.run` whose training step
  would read a dataset version the cache evicted answers a `needs-materialize` plan warning (version, bytes and shards
  to copy back, mounts) instead of refusing; the real call is refused (`artifact-missing`). `pipelineRuns.get` lists
  the same (`needsMaterialize`) for a run not done. The notice (`@/shell/data` `NeedsMaterialize`) shows in the Mix's
  launch card and the Run's stage form (from the estimate), on a Run not done and in the Pipeline run panel (from the
  pipeline run), with **Materialize** (`datasets.materialize`); it says "Back in the cache" on the artifact's restore
  event, and the estimate is asked again.
- Getting started's "Attach the call recordings" step ticks once a mount exists; a playbook's person step (an admin approving
  `mounts.new`) shows in Chat's plan.
- Not built: peaks computed at ingest or freeze (on first view), the tile pyramid as a job, the reference word track
  in the audio view, the Playwright smoke of the annotation flow.

Topics follow the event model in the Cadence system tab; the backend contract below lists the endpoints.

## Default workspaces

Five workspaces ship by default, and Chat sits in the right column of every one, so the agent is always one glance away from the work it is discussing.

| Workspace | Centre (documents) | Left | Right | Bottom | Floating |
| --- | --- | --- | --- | --- | --- |
| Training | Run, Mix, Experiment | Library | Chat, Checkpoints, Getting started (until dismissed) | Metrics, Logs | — |
| Eval | Eval report, Golden set, Annotation batch (phase 4) | Library | Chat, Inspector, Lineage | Diff | Audio |
| Data | Dataset version, Source, Recipe, Language pack | Library | Chat, Inspector, Pipeline run | Logs | Audio |
| Triage | Triage (tool) | — | Chat, Diff | Inspector | Audio |
| Ops | Model | Queue & GPU | Chat, Approvals, Storage | Shadow, Logs | — |

The Floating column is a slot, not a panel the workspace opens: a default workspace is built with nothing floating, and
the Audio panel floats there the first time something opens it (an Eval report or Diff row, a chat reference, a search
hit; phase 4 tail, 2026-10-04 — an empty Audio float covered the lower sections of the Annotation batch, Dataset
version and Eval report documents). Workspace schema 3 takes an Audio float out of layouts saved before (10
"Persistence"); its target never survives a reload, so it was always empty there.

> **Figure:** Eval workspace wireframe · Library, Eval report, Diff, Chat, Inspector, floating Audio — see the drawing in the Claude Doc "Cadence — spec v0.2".

Selecting a matrix cell attaches it to the next Chat message and fills Diff with the cell's utterances, worst first; a row opens in the floating Audio. An eval run the agent starts fills the matrix live (`eval.{id}.progress`), its gate verdict appears in the report header, and its approval requests appear in the same column.

- The status bar's Branches badge counts the project's branches waiting for a person — ahead of main and not a live
  session's (a template sync, another branch, an ended session's unmerged changes); it blinks like Approvals, its popup
  lists them and a click opens the branch in the Recipe document. `branch.waiting` (topic `branches`) announces a new
  one as an outcome notification, and the daily digest lists them.
- The status bar's Agents, Approvals and Queue badges behave like its notification history: a small popup with the
  short list (live sessions; pending requests; step jobs running and waiting, a click shows the job's pipeline run) and
  a button that opens Agent sessions, Approvals or Queue & GPU as a floating window. The Approvals badge blinks while a
  request waits (a still tint under reduced motion); approval requests and failures play a short tone while the tab
  is open (user menu → Notification sound, per browser).
- The status bar's GPU badge reads each card's memory used/total and utilisation (seeded from `compute.list`, live on
  `gpu`; a reading older than two minutes greys out) with a popup per card and the Cadence cap; it expands into Queue
  & GPU. The Queue badge counts running/queued step jobs of the current project.
- The user menu holds full screen (`view.toggleFullScreen`) next to Settings, two-factor and sign-out.
- Each workspace remembers which agent session its Chat is pinned to.

## Commands

Commands are the only way the UI changes anything: menus, buttons, shortcuts and the palette all run the same registry entry, and each mutating command is one backend call that an agent makes directly through the matching MCP tool.

| Command | Default keys | Backend call (sketch) |
| --- | --- | --- |
| Command palette | Ctrl/Cmd+K | — |
| Switch workspace | Ctrl/Cmd+Shift+1 … 5 | — |
| Save workspace / Reset to default | — | `PUT /me/``projects/{p}/``workspaces/{name}` |
| Toggle snapping | Ctrl/Cmd+Shift+; | — (user setting) |
| Float / Dock / Pop out / Maximize | See Accessibility | — (client only) |
| New run from mix | — | `POST /``projects/{p}/``runs` |
| Resume run from checkpoint | — | `POST /runs/{id}:resume` |
| New stage from checkpoint | — | `POST /``projects/{p}/``runs` with `initFrom` and a required `peakLr` |
| Pause / resume job | — | `POST /jobs/{id}:pause`, `:resume` |
| Cancel job | — | `POST /jobs/{id}:cancel` — inline confirm |
| Run eval matrix | — | `evals.new` (`POST /projects/{p}/evals`, dry run first) — one command wherever an eval starts (Checkpoints, Experiment, Eval report) |
| Set eval baseline | — | `aliases.set` on `baseline` (`PUT /projects/{p}/aliases/baseline`) — approval (R8, R23) |
| New mix / Edit mix | — | `POST /projects/{p}/mixes` (`mixes.new`); `PATCH /mixes/{id}` (`mixes.edit`) — R13: a mix is saved as a revision, not a version |
| Export dataset version | — | `datasets.export` (`POST /registry/datasets:export`; Lhotse Shar, NeMo manifest, Cadence bundle to a mount or the content store; the Hub needs an approval) — the Dataset version document's Export card (plan, then export) |
| Accept / correct / reject triage item | Enter / E / Backspace in Triage | `triage.accept`, `triage.correct`, `triage.reject` (`POST /triage/{id}:accept`, `:correct`, `:reject`) — people only |
| Export model | — | `models.export` (`POST /projects/{p}/models:export`, body `{version, profiles}`; dry run first) |
| Deploy to shadow | — | `deployments.new` (`POST /projects/{p}/deployments`, the staging target) |
| Promote to canary / production | — | `deployments.promote` (`POST /deployments/{id}:promote`) — confirm modal showing the checks and the record to be signed; an agent's call is an approval |
| Roll back deployment | — | `deployments.rollback` (`POST /deployments/{id}:rollback`) — approval, confirm modal |
| Confirm delivery | — | `promotions.verify` (`POST /promotions/{id}:verify`, the receipt line; people only) |
| New / edit deployment target | — | `deploymentTargets.new`, `deploymentTargets.edit` (Settings → Deployment targets) — approval, admin |
| New agent session (Claude Code or opencode) | — | `POST /agent-sessions` |
| Ask agent about the selection | Ctrl/Cmd+I | Focus Chat and attach the selection (client); Send is `agentMessages.new` (`POST /agent-sessions/{id}/agent-messages`) with references |
| Stop the agent's turn | Ctrl/Cmd+. | `agentSessions.cancel` (`POST /agent-sessions/{id}:cancel`) |
| Approve / deny a request | Enter / Backspace on a focused approval card | `POST /approvals/{id}:approve`, `:deny` |
| Accept / revert an agent draft | — | `POST /drafts/{id}:accept`, `:revert` |
| New source / Edit source | — | `sources.new` (`POST /registry/sources`); `sources.edit` (clearing is an approval for an agent) |
| Add mount / Rescan / Check health | — | `mounts.new` (`POST /mounts`, approval, admin); `mounts.scan`, `mounts.verify` (`POST /mounts/{id}:scan`, `:verify`) |
| Run pipeline | — | `POST /projects/{p}/pipelines/{name}:run` |
| Preview dataset / Freeze dataset version | — | `datasets.preview`, `datasets.freeze` (`POST /registry/datasets:preview`, `:freeze`, body `{version}`) — inline confirm after the dry run |
| Materialise / Evict dataset version | — | `datasets.materialize`, `datasets.evict` (`POST /registry/datasets:materialize`, `:evict`, body `{versionId}`) |
| Archive registry version | — | `versions.archive` (`POST /registry/versions:archive`) — admin, inline confirm |
| Calibrate run (OOMptimizer) | — | `POST /projects/{p}/runs:calibrate` |
| Average checkpoints | — | `POST /runs/{id}/checkpoints:average` |
| Freeze golden set | — | `goldenSets.freeze` (registry scope) — approval, admin |
| Evaluate gate / Edit gate | — | `evals.gate` (`POST /evals/{id}:gate`); `gates.edit` (the project's `gates.yaml`) |
| Register model version | — | `models.register` (`POST /projects/{p}/models:register`, a registry version; needs a passed gate; an agent's call waits for approval) — inline confirm; one command for the Eval report, Model and Experiment (from the Experiment header it opens the experiment document, which confirms the best run's checkpoint) |
| Parity check / Benchmark model | — | `models.parity`, `models.benchmark` (`POST /projects/{p}/models:parity`, `:benchmark`; a benchmark takes the card alone) |
| Package correction batch | — | `POST /projects/{p}/corrections:package` |
| New schedule | — | `POST /projects/{p}/schedules` — approval |
| Switch project | Ctrl/Cmd+Shift+P | — (client; loads that project's workspaces) |
| New project (wizard) | — | `POST /projects`, then `POST /projects/{p}:bootstrap` (job) |
| Edit agent settings | — | `PATCH /projects/{p}/agent-profile` |
| Adopt into project / Set alias | — | `POST /projects/{p}/adoptions`; `PUT /projects/{p}/aliases/{name}` |
| Edit compute / Add secret | — | `PATCH /compute/{id}`; `POST /secrets` (write-only) |
| Sync templates and skills | — | `POST /projects/{p}:syncTemplates` — result is a draft commit |
| Add project note | — | `POST /projects/{p}/notes` |
| Search everything / Find in panel | Ctrl/Cmd+K (plain text) / Ctrl/Cmd+F | `GET /search?q=&scope=` / — (client) |
| Help for this / Shortcut sheet / Explain this | ? / Ctrl/Cmd+/ / — | `GET /help/context?panel=&entity=`; `POST /agent-sessions` (read-only, with the article attached) |
| Save search as view | — | `PUT /me/projects/{p}/views/{name}` |
| Edit language pack / Edit boost list / Test boosting | — | `langpacks.edit`; `boost.edit`; `evals.new` with a `decoding` axis (R1, R24) |
| Try a model (file, microphone) | — | `transcriptions.new` (`POST /projects/{p}/transcriptions`, tag `media`, not an MCP tool); audio and words over the WebSocket it returns |
| New annotation batch / Annotate / Adjudicate / Freeze batch | Annotate keys in Triage | `batches.new` (`POST /projects/{p}/batches`, dry run first); `annotations.new` (`POST /batches/{id}/batch-items/{item}/annotations`); `batchItems.accept`; `batches.freeze` (`POST /batches/{id}:freeze`) — approval |
| Invite a reviewer | — | `invitations.new` (`POST /batches/{id}/invitations`, admin); the link opens `auth.accept` |
| New experiment / Run sweep | — | `experiments.new`; `sweeps.run` (dry run first; cap enforced) |
| Preview augmentation / Robustness matrix | — | `POST /projects/{p}/augment:preview`; `evals.new` with an `augmentation` axis |
| Import dataset / Export to Hub | — | `pipelines.run` on `pipelines/import` (`dataset_import@4`); `datasets.export` with format `hf-hub` — approval |
| Create API key / Revoke credential | — | `POST /credentials`; `POST /credentials/{id}:revoke` |
| Start playbook | — | `POST /projects/{p}/playbooks/{name}:run` (shows the estimate first) |
| Pause / resume agent session | — | `POST /agent-sessions/{id}:pause`, `:resume` |
| Merge / discard session changes | — | `agentSessions.accept`, `agentSessions.revert` (`POST /agent-sessions/{id}:accept`, `:revert`) — the session paused or ended; a conflict blocks accept |

Rules:

- Each registry entry has an id, title, icon, keys, `enabled(ctx)` returning a reason when disabled, and `run(ctx)`. Disabled commands stay visible in the palette with their reason.
- Long operations return a job id at once; progress arrives on `job.{id}` and completion lands in notification history.
- Confirm modals only for destructive or production-affecting commands, per the standing interruption rules.
- Routes above are a sketch; the backend spec owns the final paths.

## Backend contract

The shell needs fourteen things from the Go control plane, all in the OpenAPI 3.1 contract that also generates the TypeScript client and the MCP tools.

| Need | Endpoints | Behaviour |
| --- | --- | --- |
| Workspaces | `GET` / `PUT /me/``projects/{p}/``workspaces/{name}` | Versioned layout JSON; saves carry `If-Match` |
| Live events | `GET /events?topics=…` (server-sent events) | Global `seq` as the event id; resume with `Last-Event-ID`; topic wildcards such as `run.123.*` |
| Commands | `POST` / `PATCH` on resources; actions as `POST /{resource}/{id}:{verb}` | `Idempotency-Key` header, `If-Match` revision, `?dryRun=true` returns an estimate; long work returns `202 Accepted` with a job id |
| Agent sessions | `POST /``projects/{p}/``agent-sessions` (kind, driver, model, prompt, references); `GET /agent-sessions/{id}/transcript`; `POST /agent-sessions/{id}/messages`; `:cancel`, `:pause`, `:resume`, `:merge`; `POST /projects/{p}/playbooks/{name}:run` | Messages carry text plus entity references; updates, state changes and budget use arrive on `agent.session.{id}`; session branches on `recipe.{path``}` |
| Approvals and drafts | `GET /approvals?state=pending`; `POST /approvals/{id}:approve` or `:deny`; `GET /drafts?entityKind=&entityId=`; `POST /drafts/{id}:accept` or `:revert` | Approval scope: once or for the session. A gated command returns `202 Accepted` with the approval id and runs when approved |
| Errors | Every endpoint | `application/problem+json`; a stale revision returns `412` with the current revision in the body |
| Projects | `GET /projects`, `POST /projects`; every project-scoped path lives under `/projects/{p}/…`; workspaces at `PUT /me/projects/{p}/workspaces/{name}` | Events carry projectId; the client filters the stream to the current project |
| Mounts and materialisation | `GET` / `POST /mounts`; `GET /mounts/{id}`; `POST /mounts/{id}:scan`, `:verify`; `GET /storage`; `POST /registry/datasets:materialize`, `:evict` | Health and scans on `mount.{id}`; eviction and restore on `entity.artifact.{hash}`; progress on `job.{id}` |
| Project bootstrap and agent profile | `POST /projects/{p}:bootstrap` (job: repository, files, worktree, workspaces); `PATCH /projects/{p}/agent-profile`; `GET /catalog/base-models`, `GET /catalog/agent-models`, `GET /catalog/instruction-templates` | The wizard reads the catalogues; bootstrap progress on job.{id}; profile changes commit to the repository |
| Registry | `GET /registry/{kind}` with tag filters; `GET /registry/{kind}/{id}/versions`; `GET /registry/{id}:lineage` (`registry.lineage`, phase 3; the id's prefix names the kind); golden sets, normalizers and models under `/registry/golden-sets`, `/registry/normalizers`, `/registry/models`; `POST /projects/{p}/adoptions`; `PUT /projects/{p}/aliases/{name}` | Registry events carry no projectId; the Library shows them by reference. Versions are immutable; only aliases change |
| Settings | `GET` / `PATCH /compute/{id}`; `POST /secrets` (values never returned); `GET /agent-credentials`, `PUT /agent-credentials/{id}`, `POST /agent-credentials/{id}:verify|archive`, `GET /agent-providers` (values never returned); `GET /catalog/*`; `GET` / `PATCH /policies` (budgets, timezone); `GET /notification-rules`, `PATCH /notification-rules/{id}`; `GET` / `PATCH /notification-settings`; `PUT /telegram-bot` (write-only token), `POST /telegram-bot:verify`; `GET` / `POST /backups`, `GET /backups/{id}`, `POST /backups/{id}:verify` (phase 2) | Admin only; compute health on compute.{id}; backup and restore-test outcomes on `backups` |
| Search and help | `GET /search?q=&scope=` (query language above; grouped results); `GET /help/{slug}`, `GET /help/context?panel=&field=&error=`; `PUT /me/projects/{p}/views/{name}` | The index is fed from the outbox; help articles ship with the binary; problem+json type URIs resolve to help pages |
| Audio and live audio | An utterance's `…/audio?channel=&start=&end=` and `…/peaks` (R25); `POST /projects/{p}/transcriptions` and `/api/transcriptions/{id}/stream` (WebSocket, R48) | Tag `media` (06 "Media"): exempt from the verb rule and MCP, unreachable with an agent token; range requests and short-lived signed URLs for audio, play-only for reviewers, every play audited; single-use ticket (60 s) and Origin check for the socket; `LiveClientMessage` and `LiveServerMessage` are contract components |
| Credentials and notifications | `POST /login`, `POST /logout`; `GET` / `POST /credentials`, `POST /credentials/{id}:revoke`; `GET /invite/{token}`; `GET` / `PATCH /notification-rules` | Session cookie plus custom header on mutations, or Bearer; tokens hashed at rest; Telegram inline actions carry single-use signed tokens |

Standards basis:

- Error bodies follow RFC 9457, Problem Details for HTTP APIs (IETF).
- Conditional writes, `412 Precondition Failed` and `202 Accepted` follow RFC 9110, HTTP Semantics (IETF).
- The event stream uses the server-sent events format of the WHATWG HTML Living Standard.
- `Idempotency-Key` follows the IETF HTTPAPI working-group draft; it is not an RFC yet, so its exact semantics are pinned in our contract.
- OpenAPI 3.1 schemas are JSON Schema 2020-12.

## Build order

The shell comes first, the agent loop second, then the blocks in the order a first fine-tune needs them; each phase ends on a gate, not a date.

> **Figure:** Build order · 6 phases, a gate closing each — see the drawing in the Claude Doc "Cadence — spec v0.2".

Each block's endpoints and MCP tools land before its panels, so from phase 1 the agent can already drive blocks whose windows arrive later — panels catch up with what the API can already do. Generic panels (Library, Inspector, Help) belong to phase 0; the project wizard, Getting started, Settings and Approvals ship with the agent loop in phase 1; Storage, Pipeline run and Lineage come with their blocks.

## Risks, spikes and open questions

The main risk is that snapping leans on Dockview internals; four short spikes settle the unknowns before the shell is built.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Snapping depends on internal overlay bounds and header pointer capture | Breaks silently on a Dockview upgrade | Single adapter module, contract tests, pinned version; fallback to the `"titlebar"` drag handle |
| Base UI portals (menus, tooltips, popovers) render into the main document | Invisible inside popout windows | Portal container = the panel's `ownerDocument`, provided by the shell |
| Our header drag and Dockview's drag-and-drop compete for the same pointer events | Docking back from a float stops working | Capture only on empty header space; tabs untouched; covered by spike S1 |
| Needed feature turns out to be Enterprise-only | Licence cost or rework | Check the MIT/Enterprise split before designing around any Dockview feature |
| Many live panels | Jank, memory growth | Visibility-scoped subscriptions, throttling, budgets in Performance |
| Agent changes land on an entity the user is editing | Lost or confusing work | Agent drafts with Accept / Revert, an "agent editing" presence marker, revision conflicts instead of silent overwrites |
| A session branch conflicts with edits made on main from the UI | Agent work lost or overwritten | Session changes are merged as a whole with a three-way diff; nothing is auto-merged unless it applies cleanly and the preset allows it |

Spikes, 1–2 days each, on the pinned Dockview version:

- [ ] S1: floating drag capture and per-frame bounds writes; check for jitter at 60 fps and that tab docking still works
- [ ] S2: popout window hosting a shadcn/Base UI popover, tooltip and the command palette
- [ ] S3: custom Slate theme for Dockview, drop overlays included, light and dark
- [ ] S4: 20-panel workspace restore timing against the 300 ms budget

Open questions:

- [x] Accent scale: indigo, blue or iris? — Indigo
- [x] Standalone product or Cadence's UI? — Cadence's UI, sharing its Go backend, datasets and auth
- [x] Working name — Cadence
- [x] Snapping to docked-group boundaries in v1, or v2 as planned? — v2
- [x] Single user only, or shared workspaces for a team? — single admin in v1, plus an external reviewer role limited to triage batches
