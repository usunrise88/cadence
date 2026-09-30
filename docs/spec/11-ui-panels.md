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

Version 1 has 33 panels: 13 documents that open in the centre and 20 tools that follow the active document or are pinned to an agent session. Notifications are part of the chrome, not a panel.

| Panel | Kind | Shows | Main actions | Live topic |
| --- | --- | --- | --- | --- |
| Run | Document | Config diff against the parent run, status, stage timeline, final metrics | Pause, resume, stop; resume from checkpoint; new stage from checkpoint with an explicit peak LR | `run.{id}.``status`, `run.{id}.``metrics` |
| Eval report | Document | Matrix of languages and golden sets × latency profiles (`80ms`, `160ms`, `1120ms`; R43); WER/CER with delta to production and gate colour; charts: WER deltas with confidence intervals, S/D/I, duration and SNR buckets, latency to final, WER against latency (R53) | Open a cell in Diff; re-run; set as baseline; edit gate thresholds | `eval.{id}.progress` |
| Dataset version | Document | Fingerprint, hours by language and source, applied filters, lineage; statistics charts: duration, characters per second, level and SNR, sample rates, speakers (R53) | Diff two versions; export to Shar | `entity.dataset_version.{id}`, `job.{id}` |
| Mix | Document | Groups, weights, temperature, replay share; preview of hours per language; agents' drafts (dashed outline, diff on hover) and "agent editing" presence | Save (a new revision; 412 conflict notice with reload / reapply); accept or revert an agent draft; launch a run with this mix (phase 2) | `entity.mix.{id}` (drafts arrive here) |
| Triage queue | Document | Disputed production utterances: audio with a channel switch, hypotheses from several models, consensus, the item's signals; an Annotate mode for annotation batches with an editable transcript, tags and a keyboard-first flow | Accept, correct, reject; send to the next dataset version | `triage.new` |
| Model | Document | Checkpoint → ONNX → Triton repository; stage: shadow, canary, prod | Export; promote; roll back | `deploy.{id}` |
| Recipe | Document | A recipe file (SDP config, pipeline, mix, training or eval YAML, augmentation profile) with its commit history; open session branches and their diffs against `main`; agent edits stream in as a live diff | Edit; accept or revert an agent draft; accept or discard session changes (three-way diff on conflict); commit | `recipe.{path}` |
| Source | Document | Licence, languages, kind, ingest history and utterance counts of a corpus | Clear the licence for training; run ingest | `entity.source.{id}` |
| Golden set | Document | Frozen test set: languages, domain, normalizer version, size, lineage | Propose or approve a freeze; set the normalizer | `entity.golden_set.{id}` |
| Library | Tool | The registry (sources, dataset versions, golden sets, models, normalizers, templates) and the project's work (runs, mixes, recipes) with search, tags and a this-project / all filter | Open as document; compare two; adopt into project; set alias | — |
| Queue & GPU | Tool | Job queue per card; GPU memory and compute; who holds the training slot | Reorder, pause, cancel | `queue`, `gpu` |
| Metrics | Tool | Loss, validation WER, LR, gradient norm, throughput and GPU memory of the active run by step, epoch, wall time or GPU-hours; checkpoint marks; pinned runs overlaid (R53) | Pin a run; change smoothing, x-axis and scale; show as table | `run.{id}.metrics` |
| Checkpoints | Tool | Checkpoints of the active run with validation WER | Evaluate; export; new stage from here | `run.{id}.metrics` |
| Logs | Tool | Streaming log of the active job | Follow, search, copy | `job.{id}.log` |
| Diff | Tool | Reference vs hypothesis for the selected utterance; substitutions, deletions and insertions marked | Step through utterances; copy | — |
| Audio | Tool | The audio view (R51) of the selected utterance or span: waveform, spectrogram, model input and emissions, reference and hypothesis word tracks, streaming timeline; renderer `always`; floating by default | Play, loop a span, change speed, zoom, choose tracks and colormap, attach the span to Chat, export TextGrid, CTM or WebVTT | — |
| Transcription | Tool, floating by default | A manual test (R47–R50): a file, the microphone or an utterance span; one to three targets (checkpoint, model version, staging deployment) at chosen latency profiles; live partial and final words per target with level meter, latency and real-time factor; then the audio view with every target's words and the streaming timeline; nothing is stored | Choose the input; go live; finalize; stop; add a target; blind compare; type a reference (WER on the page); copy the text | `job.{id}` |
| Inspector | Tool | Properties of the current selection: config values, manifest row, metadata | Copy a value; open its source | — |
| Shadow | Tool | Divergence between the production and candidate models on live calls | Open an utterance in Diff; mark it for triage | `shadow.{``deployment``}` |
| Chat | Tool, one per agent session | Streaming transcript: replies with entity links, plan checklist (a playbook's chain is the initial plan), tool-call cards with dry-run estimates and diffs, shell cards, approval cards; header chip with the session kind (interactive, playbook, scheduled, read-only) and state; budget meter for turns, tokens and GPU-hours | Send; stop; attach the selection; approve or deny; pause or resume; merge session changes | `agent.session.{id}` |
| Agent sessions | Tool | All sessions of the project by kind and state (running, waiting approval, paused, done, failed) with driver, model, budget use, pending approvals and merge state of the session branch; scheduled runs included | New session (Claude Code or opencode; interactive or from a playbook); open its Chat; pause; resume; merge or discard session changes | `agent.sessions` |
| Approvals | Tool | Pending requests from agents, automations and registry actions: action, estimate, requester, scope (project or registry), context; decisions taken from Telegram appear here with their channel | Approve once or for the session; deny; open the requesting Chat | `approvals` |
| Storage | Tool | Mounts with health and free space; cache use; pinned dataset versions; where each shard lives | Add mount (approval); rescan; materialise or evict a dataset version | `mount.{id}` |
| Pipeline run | Tool | Any pipeline run: steps with status, inputs and outputs, per-step logs; parameters rendered from the step schema | Retry a step; open an output; open the pipeline file in Recipe | `pipeline_run.{id}` |
| Project | Document | Overview of one project: locales, base model revision, repository and branch, agent profile, budgets and today's use, gates, mounts, decision log | Edit any wizard choice; open Agent settings; archive | `entity.project.{id}` |
| Agent settings | Tool | The project's agent profile: driver, model, permission preset with a preview of the rendered `.claude/settings.json` and `opencode.json`, auto-merge policy for session branches, draft policy per entity kind; raw config editors with schema validation; `AGENTS.md` editor | Save (commits to the project repository); reset to template; test-launch a session | `entity.project.{id}` |
| Lineage | Tool | Graph around the selection: sources → dataset versions → mix and recipe SHA → run → checkpoint → model version → deployments; "used by" for registry entries | Open any node as document; copy version id | — |
| Settings | Tool, admin only | Registry-level configuration: compute (hosts, cards, memory caps, allowed job kinds), agents (the Claude Code subscription token and opencode providers: write-only values, status, expiry, Verify, default opencode model; 2026-09-30), secrets (names only, write-only values), catalogues (base models, agent models, instruction templates, permission presets), policies (retention, PII redaction, default budgets, cache quotas), notification rules and the Telegram bot, credentials (API keys, active sessions, invitations), backup status, the audit log (read-only; filter by actor, operation, project) | Edit; add secret; create API key; revoke; set, verify and archive agent credentials; sync templates across projects | `compute.{id}`, `entity.agent_credential.{id}` |
| Getting started | Tool, until the first gate passes | Setup checklist with state: mount attached, project created, first dataset frozen, first run done, first gate passed; each step with its playbook or command | Run the step; dismiss | `entity.project.{id}` |
| Help | Tool, follows focus unless pinned | The article for the focused panel, field or error: what it is, its place in the loop, fields and defaults from the schema, live commands, playbooks, sources | Search help; pin; Explain this (agent) | — |
| Language pack | Document | One locale of the project: normalizer, inverse normalisation, transliteration, LID config, boost lists with weights, golden-set recipe, README; commit history | Edit; test a phrase with and without boosting; sync from the shipped pack | `recipe.{path}` |
| Annotation batch | Document | Progress per annotator, double-annotation sample, inter-annotator WER, adjudication queue, guidelines version | Invite an annotator; adjudicate; freeze as golden set (approval) | `entity.annotation_batch.{id}` |
| Experiment | Document | Question, fixed mix and base, sweep grid and cap, parameters × metrics table of its runs with departures from defaults highlighted, best run | Run the sweep; compare N; register the best | `entity.experiment.{id}` |

Topics follow the event model in the Cadence system tab; the backend contract below lists the endpoints.

## Default workspaces

Five workspaces ship by default, and Chat sits in the right column of every one, so the agent is always one glance away from the work it is discussing.

| Workspace | Centre (documents) | Left | Right | Bottom | Floating |
| --- | --- | --- | --- | --- | --- |
| Training | Run, Mix, Experiment | Library | Chat, Checkpoints, Getting started (until dismissed) | Metrics, Logs | — |
| Eval | Eval report, Annotation batch | Library | Chat, Inspector | Diff | Audio |
| Data | Dataset version, Source, Recipe, Language pack | Library | Chat, Inspector, Pipeline run | Logs | Audio |
| Triage | Triage queue | — | Chat, Diff | Inspector | Audio |
| Ops | Model | Queue & GPU | Chat, Approvals, Storage | Shadow, Logs | — |

> **Figure:** Eval workspace wireframe · Library, Eval report, Diff, Chat, Inspector, floating Audio — see the drawing in the Claude Doc "Cadence — spec v0.2".

Selecting a matrix cell attaches it to the next Chat message; an eval run the agent starts fills the matrix live, and its approval requests appear in the same column.

- Agent sessions and Approvals also open from status-bar badges, floating by default.
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
| Run eval matrix | — | `POST /``projects/{p}/``evals` |
| Set eval baseline | — | `PATCH /``projects/{p}/baseline — approval` |
| New mix / Edit mix | — | `POST /projects/{p}/mixes` (`mixes.new`); `PATCH /mixes/{id}` (`mixes.edit`) — R13: a mix is saved as a revision, not a version |
| Export dataset version to Shar | — | `POST /``projects/{p}/datasets/{id``}:export` |
| Accept / correct / reject triage item | Enter / E / Backspace in Triage queue | `PATCH /triage/{id}` |
| Export model to ONNX | — | `POST /models/{id}:export` |
| Promote to shadow / canary / prod | — | `POST /`projects/{p}/deployments — approval; confirm modal for production |
| Roll back deployment | — | `POST /`projects/{p}/deployments/{id}:rollback — approval, confirm modal |
| New agent session (Claude Code or opencode) | — | `POST /agent-sessions` |
| Ask agent about the selection | Ctrl/Cmd+I | Focus Chat and attach the selection (client); Send is `agentMessages.new` (`POST /agent-sessions/{id}/agent-messages`) with references |
| Stop the agent's turn | Ctrl/Cmd+. | `agentSessions.cancel` (`POST /agent-sessions/{id}:cancel`) |
| Approve / deny a request | Enter / Backspace on a focused approval card | `POST /approvals/{id}:approve`, `:deny` |
| Accept / revert an agent draft | — | `POST /drafts/{id}:accept`, `:revert` |
| New source | — | `POST /projects/{p}/sources` |
| Add mount / Rescan mount | — | `POST /mounts` (approval), `POST /mounts/{id}:scan` |
| Run pipeline | — | `POST /projects/{p}/pipelines/{name}:run` |
| Preview dataset / Freeze dataset version | — | `POST /projects/{p}/datasets:preview`, `POST /projects/{p}/datasets/{id}:freeze` |
| Materialise / Evict dataset version | — | `POST /projects/{p}/datasets/{id}:materialize`, `:evict` |
| Calibrate run (OOMptimizer) | — | `POST /projects/{p}/runs:calibrate` |
| Average checkpoints | — | `POST /runs/{id}/checkpoints:average` |
| Propose / approve golden set freeze | — | `POST /projects/{p}/golden-sets/{id}:freeze` — approval |
| Evaluate gate / Edit gate thresholds | — | `POST /evals/{id}:gate`, `PATCH /projects/{p}/gates/{id}` |
| Register model version | — | `POST /projects/{p}/models` |
| Parity check / Benchmark model | — | `POST /models/{id}:parity`, `:benchmark` |
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
| Edit language pack / Test boosting | — | worktree edit; `POST /projects/{p}/boost:evaluate` |
| Try a model (file, microphone) | — | `POST /projects/{p}/transcriptions` (tag `media`, not an MCP tool); audio and words over the WebSocket it returns |
| New annotation batch / Adjudicate / Freeze batch | — | `POST /projects/{p}/batches`; `PATCH /batches/{id}/items/{i}`; `POST /batches/{id}:freeze` — approval |
| New experiment / Run sweep | — | `POST /projects/{p}/experiments`; `POST /experiments/{id}/sweeps:run` (dry run first; cap enforced) |
| Preview augmentation / Robustness matrix | — | `POST /projects/{p}/augment:preview`; `POST /projects/{p}/evals` with profiles |
| Import dataset / Export to Hub | — | `POST /projects/{p}/pipelines/import:run`; `POST /registry/{kind}/{id}:export` — approval |
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
| Mounts and materialisation | `GET` / `POST /mounts`; `POST /mounts/{id}:scan`; `POST /projects/{p}/datasets/{id}:materialize`, `:evict` | Health on mount.{id}; materialisation progress on job.{id} |
| Project bootstrap and agent profile | `POST /projects/{p}:bootstrap` (job: repository, files, worktree, workspaces); `PATCH /projects/{p}/agent-profile`; `GET /catalog/base-models`, `GET /catalog/agent-models`, `GET /catalog/instruction-templates` | The wizard reads the catalogues; bootstrap progress on job.{id}; profile changes commit to the repository |
| Registry | `GET /registry/{kind}` with tag filters; `GET /registry/{kind}/{id}/versions`; `GET /registry/{kind}/{id}/lineage`; `POST /projects/{p}/adoptions`; `PUT /projects/{p}/aliases/{name}` | Registry events carry no projectId; the Library shows them by reference. Versions are immutable; only aliases change |
| Settings | `GET` / `PATCH /compute/{id}`; `POST /secrets` (values never returned); `GET /agent-credentials`, `PUT /agent-credentials/{id}`, `POST /agent-credentials/{id}:verify|archive`, `GET /agent-providers` (values never returned); `GET /catalog/*`; `GET` / `PATCH /policies` | Admin only; compute health on compute.{id} |
| Search and help | `GET /search?q=&scope=` (query language above; grouped results); `GET /help/{slug}`, `GET /help/context?panel=&field=&error=`; `PUT /me/projects/{p}/views/{name}` | The index is fed from the outbox; help articles ship with the binary; problem+json type URIs resolve to help pages |
| Audio and live audio | `GET /utterances/{id}/audio` (R25); `GET …/peaks`; `POST /projects/{p}/transcriptions` and `GET /transcriptions/{id}/stream` (WebSocket, R48) | Tag `media`: exempt from the verb rule and MCP; ranges and signed segments for audio; single-use ticket and Origin check for the socket; message schemas are contract components |
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
