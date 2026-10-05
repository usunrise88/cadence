# Cadence spec — Coverage audit, risks, open questions, sources

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Coverage audit

Twelve blocks were missing at the audit of 2026-09-29; eleven are now specified in the sections above, and data governance under immutability is deliberately deferred to a later phase.

| Block | What is missing | Why it matters | Priority, phase |
| --- | --- | --- | --- |
| Authentication and access | How the admin logs in (passkey or password), session lifetime, how reviewer invitations work (link, expiry), how MCP session tokens and CLI keys are issued and revoked | Everything else assumes an identity; the reviewer role needs invitations | Must, phase 1 |
| Security and threat model | Agent sandbox specifics (container, filesystem limited to the worktree, egress allowlist), prompt injection through data an agent reads (transcripts, notes, help), secret handling end to end, TLS and reverse proxy, dependency pinning and SBOM | Agents read production transcripts and run shell; an injected instruction must not become a command | Must, phase 1 |
| Operations of Cadence itself | Install and upgrade, database and data migrations, backup and restore, disaster recovery, failure handling (worker crash, stuck queue, OOM retry with a smaller batch), release versioning against the NeMo container | A single-user tool still has to survive its own upgrades and a bad night | Must, phase 2 |
| Hot words and language packs | Boost lists (names, terms, brands) as a project asset applied at decode and evaluated; per-locale resources as registry assets: normalizer, inverse text normalisation, transliteration (Serbian Cyrillic and Latin), language-ID config, golden-set recipe | Hot words were a first-line requirement and appear only inside one playbook; every new locale needs the same resource set | Must, phase 2 |
| Task metrics and streaming metrics | Entity accuracy for names, numbers, dates and addresses on annotated golden sets; latency to final, partial-hypothesis stability, end-of-utterance quality; RTF and streams per card | A voice agent fails on a wrong phone number, not on average WER | Must, phase 3 |
| Annotation workflow | Building a golden set from own calls: sampling, guidelines, double annotation, adjudication, inter-annotator agreement; the reviewer role today only triages | Without it the telephone golden set cannot be produced | Should, phase 3 |
| Testing and quality strategy | Unit, contract and end-to-end layers; the smoke project as the e2e; fixtures for pipelines on tiny datasets; evals for skills and playbooks against both agents; Playwright for the shell | Spikes and contract tests exist; nothing says what CI runs every day | Should, phase 1 onward |
| Notifications | Channels beyond the in-app history (Telegram, email), routing by event class, quiet hours, digests; approvals while the admin is away | A nightly flywheel needs to reach a person | Should, phase 2 |
| Experiments and sweeps | Grouping runs into an experiment, parameter sweeps over a mix, comparison tables across many runs | Compare covers two entities; tuning needs ten | Should, phase 3 |
| Augmentation policy | Telephony band-limiting, codec simulation, noise and speed perturbation as step kinds with defaults and sources | The 8 kHz gap is measured; the fix is named only in a playbook | Should, phase 2 |
| Data governance under immutability | Deleting a caller's audio from immutable versions (tombstones, partial versions), access audit for who listened to what, data-subject requests | Immutability and deletion rights collide; the collision needs a rule | Could, phase 4 |
| Interoperability | Import of NeMo manifests, Lhotse cuts, Hugging Face datasets; export of datasets and models to the Hub or another project | Public data arrives in three formats; models may need to leave | Could, phase 4 |

Also unspecified but small: compute availability windows (pausing jobs in business hours on a shared card), the `cadence` CLI surface, capacity assumptions for the database and the search index, and decoder extensions such as end-of-utterance or speaker-gender tokens as a later training feature.

## Risks and open questions

The biggest unknowns are how complete the Claude Code ACP adapter is and how rough Nemotron 3.5 fine-tuning still is in NeMo; both get a spike before anything is built on them.

| Risk | Impact | Mitigation |
| --- | --- | --- |
| The Claude ACP adapter lags the native Claude Agent SDK | Missing hooks or permission detail for Claude sessions | Driver interface; native Claude driver for named gaps only |
| Agent runaway: loops, spend, GPU queue flooding | Cost, blocked card | Budgets, turn limits, inactivity timeout, one training slot per card |
| Nemotron 3.5 tooling moves fast (NeMo main vs release, prompt-key bugs) | Broken runs after upgrades | Pin the NeMo Speech container; smoke-test fine-tune and export on each upgrade |
| A TypeScript sidecar breaks the single-binary shape | More to deploy | A separate service in the same compose file, health-checked by the control plane; one version for all services |
| Licence mistakes in training data | Commercial use blocked later | Licence is a required source field; exports check every member's licence |
| Test leakage | Inflated scores, bad promotions | Runs cannot reference golden sets; fingerprint exclusion at freeze |
| Framework seams erode while NeMo is the only framework | A second framework later needs control-plane rewrites and migrations | The CPU toy pack runs the conformance suite on every pull request (R45); spike F1 before a real second pack |
| A second framework's stack rots (icefall changes little now; k2 must match PyTorch exactly) | Broken pack after an upgrade | One runtime image per pack, pinned by digest; conformance suite on every upgrade; sherpa-onnx (active) for serving exports |
| Live sessions share the staging card with training | OOM, slower training, latency numbers that lie | Interactive memory reservation under the card cap, never beside a benchmark (R49); A5 measures |
| Audio views in many panels | Blank views at the WebGL context limit, memory growth, jank | One renderer per window, peaks and tiles for long audio, context-loss handling; S5 sets the budgets |
| People's voices in manual tests | Personal data kept by accident | Nothing is stored: session audio lives in the worker's temporary directory until the socket closes, and a sweep removes leftovers within an hour (R47) |

Spikes:

- [x] A1 (done): ACP client against `opencode acp` and `claude-agent-acp`: chat, tool-call diffs, permission round trip, cancel, resume
- [x] A2 (done): Cadence MCP server with ten generated tools; both agents create a mix and launch a dry-run
- [ ] A3 (partial): Nemotron 3.5 fine-tune on the staging card under a 24 GB cap, then ONNX export and a parity check — training, streaming eval and ONNX parity pass; Triton serving waits for phase 5
- [x] A4 (done): Outbox → SSE → cache patching with an agent editing a mix while the Mix panel is open
- [x] A5 (partial, enough for phase 3): live microphone → WebSocket relay → Nemotron checkpoint in the worker and back, beside a training job (before phase 3's live mode) — the 160 ms budget holds beside training; Firefox, Safari and Caddy are left to the owner
- [x] S5 (done with caveats): one audio view with a 30-minute call, spectrogram and word tracks at 60 fps across popouts (before phase 3's Audio panel)
- [ ] F1: k2/icefall through the framework seams without changes outside its pack (deferred with the packs beyond NeMo)

Open questions:

- [ ] K (2026-09-30, playbooks): agents cannot start playbook sessions — `playbooks.run` joined the preset rule
  `sessions-are-for-people` (05 lists it among the agent tools; an agent starting a session contradicts "sessions are
  started by people"); `playbooks.get` gives an agent the estimate.
- [ ] K: the dry-run rule is per operation and used up by the real call (two training runs need two dry runs); it is
  enforced only in playbook sessions, not in interactive ones.
- [ ] K: "budget: exceeded" stops a playbook when the session pauses on its turn, token or project token budget; an
  over-budget GPU spend waits for its approval as in any session, and stops the playbook only when denied.
- [ ] K: playbooks run from the bundled templates; the copies in a project's `playbooks/` are for reading and editing,
  and project overrides of a playbook are not read yet.
- [ ] K: the watch step ticks from `runs.get` answering the run with an ended status (a run's pipeline has several
  step jobs, so `jobs.wait` only marks it running). Before the session the calibrate step's estimate is its hint
  (0.1 GPU-hours): `runs.calibrate` plans over a mix, which exists only once the session made it. The later
  playbooks' continuation stages ask for the parent run and `peakLr` (no defaults.yaml key for a stage's peak LR).
- [ ] U (2026-09-30, panels): the worker reports one memory number per card, so Queue & GPU estimates Cadence's share
  (used − the last reading taken while no Cadence job held the card; without one, the job's cap) and labels it
  "estimated"; per-process memory from the worker would make it exact.
- [ ] U (2026-09-30, panels): jobs and pipeline runs are not documents, so "the active job" (Logs) and the pipeline
  run Pipeline run follows are a small shell focus store set by Queue & GPU, Pipeline run and (later) Run, not the
  selection bus.
- [ ] U (2026-09-30, panels): the Mix document's "launch a run" sends the project's base model (the wizard's choice;
  runs.new alone defaults to the instance's), shows the dry-run estimate and starts the run only on confirmation.
  Metrics and Checkpoints follow the active Run document, then the last run shown, then the project's newest run.
- [ ] U (2026-09-30, panels): the Recipe document had no write path; `recipes.new` / `recipes.edit` commit one text
  file to main as the person (If-Match: the commit that last changed the file). The default preset forbids both for
  agents, who change files on their session branch. The augmentation profile file shape (`augment/<name>.yaml`) and
  its recommended values (`defaults.yaml` `augment.*`) are recorded in 03 "Augmentation".

- [x] R (2026-09-30, runs): the base model's `familyId` names the family collection `model-family/<familyId>`; the
  seeded Nemotron base model says `nemo.fastconformer-rnnt.cache-aware`, so the NeMo pack must publish its family
  descriptor under that name (or the base model fixture must change with it). — The NeMo pack publishes it (stream N).
- [ ] R: train role kinds take the step budget as `steps`, the seed as `seed` and a stage's peak learning rate as
  `peak_lr` (else `learning_rate`, else `lr`); a request setting a parameter the kind lacks is `recipe-mismatch`.
- [ ] R: a `dataset` input of a run's recipe takes the mix's only dataset (the CPU toy pack trains on a dataset, not
  a mix); a mix of several datasets needs a recipe whose kinds read the `mix` artifact.
- [ ] R: calibrations are cached by (base model collection, card class, memory cap, precision); the card is the one
  the estimate picks (compute.ForJob), not the card the lease got, and the newest calibration of any bucket
  configuration answers. A calibrate step's meta gives `secondsPerStep` and optionally `plusMinus` | `spread` |
  `secondsPerStepStd`, `batchSizes`, `bucketConfig`.
- [ ] R: people are not gated by GPU budgets (the phase-1 policy engine's rule); only agents and automation keys are.
  Calibration and averaging count as spending (`runs.calibrate`, `checkpoints.average` joined the `gpu-spend` rule)
  with their kinds' published estimate.
- [ ] R: `runs.resume` continues only failed or cancelled runs, from the newest training-state a released lease of
  the run carries (a paused job resumes by itself with `jobs.resume`); pause, resume and stop in the Run panel act on
  `currentJobId` through `jobs.pause|resume|cancel` — no `runs.pause|cancel` operations.
- [ ] R: a dry run of `runs.new`, `runs.stage` or `runs.calibrate` writes the rendered mix and base-model blobs into
  the content store (content-addressed, not indexed); nothing else.
- [ ] Y (2026-09-30, worker harness): a directory artifact is recognised by `meta.layout: dir|file`, which the worker
  adds to every output it releases; an input without it is sniffed (a blob that parses as exactly the manifest shape
  and whose files are all present is a directory). The control plane should keep `layout` in stored artifact meta.
- [ ] Y: an input name may receive several artifacts as `<name>.0`, `<name>.1`, … (checkpoint averaging declares
  `consumes: {checkpoints: checkpoint}`); the pipeline and `checkpoints.average` pass them that way.
- [ ] Y: a CPU runtime (the toy worker) claims with an empty `cards` list; the scheduler must lease `gpu: false` steps to
  a worker without cards, and the lease's `card` is then ignored (`CUDA_VISIBLE_DEVICES` is empty for CPU steps).
- [ ] Y: a lease claimed while the worker is stopping is handed back at once as `failed` with error type `lost`,
  `retryable: true`; a worker stopping mid-step releases it `cancelled` with its training state (message "the worker
  is stopping"). A heartbeat answered with a 4xx other than 408/429 means the lease is gone: the step is stopped and
  not released.
- [ ] Y: the `dataset` artifact the toy pack reads is JSON lines whose utterance lines carry `audio` (a b3 hash of a
  16 kHz WAV in the content store), `text` and optionally `split`; other lines (the header) are skipped. Steps read
  such referenced blobs read-only through `ctx.blob(hash)`. Stream D's final format must keep those keys.
- [ ] Y: pack defaults live under `packs.<pack>.<key>` in defaults.yaml (toy: `packs.toy.*`); the worker reads the
  control plane's file (`CADENCE_DEFAULTS_FILE`, copied unchanged into each image), never a copy of its values; the
  contract's `Defaults` gained `packs` (a map of sections) for it.
- [ ] Y: a transcribe-role kind takes a `profile` parameter naming one of its family's latency profiles, and a
  train-role kind resumes from `overrides.resumeFrom` up to its total `steps` — the conformance suite relies on both.

- [ ] N (2026-09-30, NeMo pack): no `checkpoint_register` step kind — the control plane's checkpoint hook already
  registers every `checkpoint` output of a run and keeps the top k; a pipeline that needs an explicit registration can
  add the kind later. `nemotron_finetune` writes two checkpoint outputs, `checkpoint` (the end) and `checkpoint_best`
  (the best validation pass of the lease; the same artifact when it was the last).
- [x] N: "a checkpoint and training state every 20 minutes" becomes a training state every `state_every_minutes`
  written into the lease's scratch: a stop releases the newest (a fresh one when the last save fits the stop grace), but
  a lost lease (worker or host crash) loses them, because the worker protocol releases outputs only at the end. Interim
  artifacts (a `workerArtifacts` put during the lease) would close that gap.
  Closed 2026-10-01: the NeMo pack publishes each periodic state mid-lease (`workerOutputs.new`, hard links of the
  files just written), and the lost-lease retry resumes from the step's newest published state that is not evicted.
- [x] D (2026-10-01, test stand): a fine-tune that ends normally still writes its final training state (7.3 GB for
  the 0.6B model), and the retention rule makes it evictable at once — one day of short fine-tunes filled 88 GB of
  states on the stand's disk. Proposed: write the final state only when asked (a `keep_state` parameter, default
  off) or evict a finished run's states automatically after a person approved it once per project. Built meanwhile:
  Settings → Content store (the dry run, **Evict…**) and the `storage.low_space` warning below `cache.store_low_free`.
  A second trap: with `CADENCE_BACKUP_DIR` set, a state is evictable only once the backup mirror holds its blobs, and
  the mirror (`<backups>/cas`) copies every blob it lacks — on the stand both live on the same disk, so a backup would
  double the store before anything could be freed, and without one nothing is evictable. Proposed: training states
  skip the mirror (they are only ever read to resume) and are evictable without it; or the mirror must live on
  another filesystem, checked at start.
  Decided by the owner 2026-10-01: training states skip the mirror and are evictable without it (an eviction is
  permanent). A finished fine-tune writes no final state any more: `nemotron_finetune` declares `state` an optional
  output (`StepKindDescriptor.optionalOutputs`), released only when the step stops early.
- [ ] N: the augmentation profile is the finetune step's `augmentation` parameter (default `packs.nemo.augmentation`,
  the telephony chain: 8 kHz band-limit with G.711 μ-law/A-law or GSM, gain, speed 0.95–1.05); a project's
  `augment/*.yaml` file is not read yet (no artifact type or recipe convention carries it to the step). AMR-NB and
  Opus need ffmpeg, which the NeMo Speech image lacks; the noise bank joins in phase 4.
- [ ] N: `max_duration` defaults to 20 s (spike A3, the shared-card cap), not the Key defaults' 40 s; bucket batch
  sizes for 20 s clips are 1 under the 22 GB cap.
- [ ] N: the lease's memory cap counts the whole process (nvidia-smi); the NeMo steps give PyTorch's allocator the cap
  minus `packs.nemo.cuda_context_reserve_mb` (1024 MiB). Other packs sharing a card should do the same.
- [ ] N: the NeMo steps disable Lightning's SIGTERM handler (it raises at the end of the step and exits before a
  training state is written) and make dataloader workers ignore SIGTERM (the harness signals the process group); the
  harness's stop event alone drives the stop. Lightning's checkpoint IO is replaced by a direct write (its in-memory
  copy made a 7.7 GB state take 45 s; now about 15 s).
- [ ] N: training text ends with the locale tag (` <he-IL>`, spike A3's assumption); validation references carry no
  tag and decoding strips tags, so `val_wer` is on raw punctuated text without the tag.
- [ ] `audit.list` for scoped credentials (2026-09-30, for the evals on the staging stand): an API key or agent token
  of one project reads that project's audit rows only (narrowed like `approvals.list`); a key without a project is
  refused; the admin's session still reads everything. Agents may therefore read their own project's audit
- [x] Add Chat, Agent sessions, Recipes, Sources, Golden sets and Approvals to the UI shell's panel catalogue
- [x] Stress marking and TTS datasets from the July concept: later phase, or dropped? — deferred to a later phase, out of v1
- [x] Default model for opencode sessions? — chosen per project in the wizard, editable in Agent settings
- [ ] Where production samples may be captured, and the retention limit — from call recordings on a mount, 90 days proposed, awaiting confirmation
- [x] Single user, or a team with roles? — single user in v1, roles deferred
- [ ] Projects need a state template the spec does not list: `container` (active → archived). Phase 0 adds it beside registry / work / promotion; confirm or fold projects into one of the three
- [ ] Client-only commands (float, dock, theme, palette) are not API operations; phase 0 names them `view.<name>`, exempt from the verb vocabulary like the `me`/`auth` tags (R1). Confirm the namespace
- [ ] Unknown `/api` path answers 404 `not-found`, wrong method 405 `method-not-allowed` (a tenth error type with its article); the review proposed 400 for both
- [x] Training from scratch or a second framework (k2/icefall) in v1? — Only the seams, in phase 2; training from scratch, packs beyond NeMo and spike F1 are deferred (owner, 2026-09-29; R44, R45)
- [x] Keep uploads and microphone recordings? — No: a transcription is a manual test and stores nothing (owner, 2026-09-29; R47)
- [ ] `project://summary` needs the connection's project, which the spec does not say how to name. Phase 1 reads a
  `Cadence-Project: <slug>` header that the agent host sends next to `Authorization` in ACP `session/new` →
  `mcpServers` (R2); once `cst_` tokens are project-bound, the token's scope decides and the header must agree.
  `project://{p}/summary` reads any project the token may see. Confirm
- [ ] MCP tool names keep the dotted `<entity>.<verb>` on the wire, but both agents rewrite them: Claude Code shows
  `mcp__cadence__projects_new`, opencode `cadence_projects_new` (model APIs allow only `[a-zA-Z0-9_-]` in tool names).
  Spike A2 checks whether agents still map them to the names in help, errors and skills, or whether the vocabulary
  should use `_` in tool names
- [ ] Automation keys (`cdk_`) follow the agent rules of their scope's preset (default deny), like agent sessions; people are allowed everything except the `everyone` rules (phase 1, stream C). Confirm, or treat a person's API key as the person
- [ ] `jobs.wait` is a read verb (a `GET` action, no Idempotency-Key): waiting changes nothing. `api/vocabulary.yaml` now says `class: read` for `wait`; the spec table groups it with run/pause/resume
- [ ] A dry run of a gated command runs as a dry run and answers `Cadence-Policy: approval; rule=<id>` instead of creating an approval, so an agent can see the estimate first (R7 is silent)
- [ ] The phase-1 fixture gates `projects.archive` for agents (`archive-project` rule); the Guardrails table says "delete anything: not allowed" — once real gated commands exist, move `projects.archive` to `no-deletes`
- [ ] Until phase 2 meters GPU use, the budget check uses a fixed 8 GPU-hours per day (`policy.StubBudget`); the value should come from `defaults.yaml` budgets (R11)
- [ ] opencode names MCP tools `<server>_<tool>` with other characters replaced by `_` (`cadence_projects_get`); the rendered `permission` block assumes it — verify in spike A1 with both drivers
- [ ] Registry API shape (phase 1): versions are served per kind (`baseModels`, `datasets`, `templates` under
      `/registry/<kind>`, so `datasets.materialize` and `models.export` fit later) and collections generically
      (`collections.list|get`); a collection name in a path escapes its slash (`dataset%2Ffleurs-he-smoke`). Adoption is
      `POST /projects/{p}:adopt` (`projects.adopt`, If-Match on the project) instead of `POST /projects/{p}/adoptions`.
      Confirm
- [ ] Adoption checks: phase 1 accepts any frozen version; the licence and locale checks of 02 "Registry" wait for
      project locales (wizard) and a licence policy — which licences may a project adopt without a person?
- [ ] An alias may point only at a version the project adopted (enforced by a foreign key); versions and the staging
      card's class (`blackwell-96gb`, from spike A3's "96 GB" and the Blackwell toolchain note) are assumptions until
      the staging host is inventoried — card class resolved 2026-09-30: RTX PRO 5000 Blackwell 48 GB, `blackwell-48gb`,
      cap 24 GB = fraction 0.5 (00 decision log)
- [ ] An alias may point only at a version the project adopted (enforced by a foreign key). The staging card was
      inventoried in phase 2: an RTX PRO 5000 Blackwell 48 GB (`blackwell-48gb`, cap 24 GB beside the resident vLLM
      service), no longer the `blackwell-96gb` assumption; installs seeded before phase 2 correct it with compute.edit
- [ ] Secrets: no rotation or archive yet (`secrets.new` refuses a taken name); the master key defaults to
      `$CADENCE_DATA_DIR/master.key`, generated on first start, until the compose secret of R9 is wired
- [ ] Identity (phase 1) assumptions, confirm: login throttling counts failed attempts only (5/min, 20/h per address and per username, in memory); `X-Forwarded-For`/`-Proto` are trusted from loopback and private peers (the host's Caddy, Docker's gateway); the TOTP secret lives in the `users` row, not the R9 file store (it is a sign-in factor, not a secret handed to jobs); passwords need 12+ characters; first start may rename the admin account; out-of-scope reads answer 403 `forbidden` rather than hiding the entity behind 404; a credential without a project may not open the event stream
- [ ] Search (phase 1, wave 2): `projects.search` is `GET /projects/{p}:search` (a read action; `/projects/{p}/search`
      fails R1's path rule). Without a scope qualifier it covers the current project, the registry (with registry
      read) and help; instance-wide work (jobs and approvals without a project) only for full scope. `project:<slug>`
      replaces the current project (each checked against the credential); `scope:all` never widens a project-bound
      token beyond its project. Unknown qualifiers are a 400 `invalid-query`, never text. Confirm
- [ ] Search typo tolerance uses pg_trgm word similarity above 0.4 (one transposition in a six-letter word); the
      Hebrew fold is niqqud stripping plus a ten-word ktiv male/haser stub list until the Hebrew language pack
      (phase 3) brings a lexicon
- [ ] Saved searches belong to the actor id (a person, or an API key acting as itself), like workspaces; there is no
      way to remove one yet (removal would be an `archive` verb under `me`)
- [ ] CLI (R34): generated commands exclude the exempt tags (`auth`, `me`) and planned operations; `cadence help`
      is both the help command and the `help` entity, so `cadence help get|search` call `help.get|search` and
      `cadence help <entity>` describes an entity
- [ ] The palette's text box keeps focus, so Space previews a hit in the Inspector only after the highlight was moved
      with the arrow keys (typing resets it); Enter opens a hit's document where its kind has one, else the Inspector
      (10 "Every list" assumes list focus, not a text box)
- [ ] Phase-1 panels (wave 2), confirm: the audit log is a Settings section (11 names no panel of its own);
  Getting started sits in the Training workspace's right column (Training is where a newcomer lands; 11 lists it
  in no workspace) and "Dismiss" is remembered per browser, not per user, until a user-preferences entity exists;
  "first dataset frozen" counts a dataset version frozen by a person or an agent (bundled fixtures do not);
  Settings opens floating and shows to any signed-in person (v1 has one admin; the reviewer role hides it later);
  "Approve for this session" is offered only for requests carrying an agent session id; stored Ops workspaces are
  not migrated to add the Approvals panel (the stored shape did not change; the status-bar badge reaches it) — only
  "Reset to default" and new workspaces get it
- [ ] Mixes and drafts (phase 1, mix stream) assumptions, confirm:
      the draft policy per kind is the project's agent profile `draftPolicy` (Agent settings; the wizard fills it
      from `agent.draft_policy`), and `defaults.yaml` `drafts.mix` only for a project without a profile; an agent never accepts a draft (`drafts-are-for-people`
      forbidden rule in `guardrails-default`) — auto-accept is the `direct` policy, not an agent's choice; one open
      draft per author (actor + session) and entity, whose later edits update it (the draft's own `rev` is the
      If-Match of accept and revert); an agent's edit on a newer revision carries its open draft over field by field
      (top-level fields the draft changed win); accepting a draft whose base is no longer current answers `412
      draft-stale` (a new error type) with the entity's revision instead of merging; presence is the authors of open
      drafts plus an agent's direct edit for `drafts.presence_seconds` (30 s), sent whole as `presence.changed`;
      `drafts.list` is `GET /drafts?entityKind=&entityId=` (one operation for every draftable kind) rather than
      `GET /{kind}/{id}/drafts`
- [ ] Mix shape (R13), confirm: groups of frozen dataset versions (`ver_…`, `@alias` or a collection name, stored
      resolved) with a weight and a replay flag; a group is sampled with probability ∝ weight^(1/temperature) among
      its kind and replay groups together get `replayShare`; a dataset version belongs to one group; names are unique
      per project; the preview uses each version's train split hours and splits a multi-locale version evenly.
      Mixes use the `container` state template (active) like projects; "Save mix as version" is `mixes.new` (save)
- [ ] U0 (charts, R53), confirm: the eight categorical hues are crimson, violet, bronze, plum, lime, sky, orange,
      teal; in light mode lime/sky take step 11 and orange/teal step 10 (step 9 is under 3:1 on slate-2), because the
      only eight step-9 hues passing 3:1 away from the status and accent hues include bronze/gold/brown, 3 ΔE apart;
      tritanopia keeps neighbours ≥ 6 ΔE (light lime/sky are 6.7), the others ≥ 8. Histogram marks sit at the bin
      that contains them; the Pareto front is drawn by the browser from the returned points
- [ ] Chromium's offline emulation does not drop an open event stream, so spike A4 drops it in the page and the
      shell's own reconnect resumes with `?after=`; EventSource's native retry with `Last-Event-ID` is covered by the
      control plane's integration test only
- [ ] Projects (phase 1, wave 2) assumptions, confirm:
  - The bare repository on the control plane is the canonical copy for every repository kind; GitHub and linked
    repositories get `main` mirrored after each change (never force-pushed; a failed push is recorded on the project
    as `repository.pushError`). Changes pushed to GitHub directly are not pulled back yet
  - Agent hosts clone from and push to `/git/<slug>.git` with the session's `cst_` token, which may push only
    `refs/heads/session/<session id>` (R3 "the host clones from the control plane"); `main` moves only by merge
  - Linking an existing repository uses its `main`; a repository whose default branch has another name gets a new
    `main` from the bootstrap commit. SSH remotes are refused (https only)
  - Creating a GitHub repository is tested against a fake of the REST API only, not against GitHub itself
  - The default opencode model is `minimax/MiniMax-M3` (models.dev naming of the MiniMax provider, R6) — check the
    exact id against the Token Plan; Claude Code models are the aliases `sonnet` (default), `opus`, `haiku`
  - The per-project agent budget is in tokens per day (`budgets.agent_tokens_per_project_per_day`, 20 M), since money
    counts only in API-key mode (R6); the draft policy defaults are mix, gate and language pack as drafts, notes direct
  - A failed bootstrap leaves the project in state `failed` with the reason; retrying needs a new project (no
    `projects.retry` yet), and its slug stays taken while the repository directory exists
  - The Agent settings live topic is `entity.project.{id}` (11-ui-panels); profile changes are `agent_profile.edited`
    events there, with entity kind `agent_profile`
  - The default workspaces are created by the bootstrap as placeholders (an empty layout) that the web shell fills
    with its code-defined default layout on first open
- [ ] Agent sessions (phase 1, wave 2) assumptions, confirm:
  - Ending an interactive session is `agentSessions.cancel` with `{"end": true}` (the vocabulary has no end/close
    verb); the session is `done`, `cancelled` only when it never started
  - `agentSessions.accept|revert` need the session paused or ended; a paused one ends. Accepting a running session
    would race the host's last commit
  - The session token is minted when a host claims the session, not at `agentSessions.new`, and re-minted (the old
    one revoked) when another host takes the session over; nothing stores it in plaintext
  - What a claim hands the host is taken at most once: a host that crashes between claim and delivery loses those
    messages (the user sends again); its sessions resume on the next host after 90 s of silence
  - A message to a paused session resumes it unless it paused on its budget; read-only sessions take one message
  - Every pause writes a note to NOTES.md on `main` as Cadence (R5 "writes a note"), the person's own pauses included
  - Budgets: 10 M input+output tokens per session and 1 M per turn (cached reads not counted; mid-turn the host uses
    the growth of the context window, which underestimates) — Cadence recommendations until measured
  - The idle clock runs from the last user message (or the start) and only while no approval is pending and no turn
    runs; waiting for a person never pauses
  - An agent-permission approval shows its ACP request as the approval's `request` (method `ACP`, path
    `session/request_permission`) so existing Approvals cards render it; `kind` and `permission` tell them apart
  - Development without the agent-credentials volume uses the agents' default login under the host user's HOME and
    no per-session users; the image always isolates
  - claude-agent-acp raises a permission request even for MCP tools the rendered settings allow (A2); the preset
    answers them without a person — resolved 2026-09-30: Claude Code ignores a project file's allow rules; the host
    pre-allows the preset's Cadence tools (`HostStart.allowedTools` → `allowedTools`)
- [ ] Chat, Agent sessions and the context bridge (phase 1, chat stream) assumptions, confirm:
  - "One Chat per agent session": the workspace's Chat (instance `chat`, in the right column of every default
    workspace) is pinned to a session through the selection bus pins, stored as `panels.chat.pinnedTo =
    agent_session:<id>`; a session opened while that Chat shows another one gets its own Chat (`chat:agent_session:<id>`)
    in the same group. A message in a Chat without a live session starts a new interactive session with the
    profile's driver and model
  - Workspace schema 2 adds Chat to the right-column group of layouts stored before Chat existed (the stored shape
    of `panels` did not change; the layout did)
  - The header meters turns and tokens only: a session carries no GPU-hours of its own (05 and 11 name GPU-hours in
    the meter); GPU spend stays with the project budget and its approvals
  - Enter sends and Shift+Enter starts a new line; Ctrl/Cmd+. and Ctrl/Cmd+I act on the Chat that last had focus
  - "Explain this" (document headers and help articles) starts a read-only session whose prompt asks what the thing
    is, where it sits in the loop and what to do next, with the entity and its panel's help article (`@help:<id>`)
    attached; "Ask agent" prefills `<intent> (<references>)` and never sends by itself
  - References in agent Markdown become links for `@<kind>:<id>[#part]` outside code; a kind without a document opens
    its preview in the Inspector, `@session:` a Chat and `@help:` the Help panel
  - The finished turn (busy → idle), pauses, failures and ends reach the polite live region from the shell's
    `agent.sessions` subscription; pauses, failures and sessions ending with changes also go to the notification
    history. Agent sessions has no column in the default workspaces: it opens floating from the status bar's Agents
    badge (11 "Default workspaces")
- [ ] Agent credentials (Settings → Agents, 2026-09-30) assumptions, confirm:
  - Claude's `claude setup-token` token is assumed valid for about a year from when it is set here: the expected expiry
    is set + 365 days and the UI warns 30 days before. Anthropic does not state the lifetime in the CLI; a 401 on
    Verify is the real signal
  - Verify is explicit (a button, `agentCredentials.verify`); nothing verifies automatically after a key changes, so
    opencode's model list and the default-model choice appear only after the first Verify. An automatic check after
    each write would spend one tiny request per change
  - The verification requests use cheap models from the catalogue (Claude `haiku`; `minimax/MiniMax-M3`,
    `anthropic/claude-haiku-4-5`, `openai/gpt-5-nano`, `openrouter/anthropic/claude-haiku-4.5`,
    `deepseek/deepseek-v4-flash`); model ids follow models.dev as bundled with the pinned opencode and will drift
  - The static base allowlist (`CADENCE_EGRESS_ALLOW`) still lists `api.minimax.io` and `api.minimaxi.com` although
    configured providers now reach the proxy dynamically; dropping them would make MiniMax reachable only once it is
    configured in Settings → Agents
  - A custom (OpenAI-compatible) provider's models come from its `GET <baseUrl>/models`; a server without that
    endpoint fails Verify and has no models. A hand-typed model list is not built
  - A custom base URL's host is allowed as `host:port` when the URL names a port; an IP literal (a vLLM on the LAN)
    is allowed only when listed exactly — the proxy still refuses addresses matched by any wildcard. This lets an
    agent reach a LAN address the admin configured
  - The proxy polls `egressHosts.list` every 15 s, so a newly configured provider can be refused for up to 15 s; a
    push from the control plane was not built
  - Disconnecting Claude Code removes the whole `claude/` directory of the volume (an interactive login made with the
    CLI included); removing an opencode provider drops it from `auth.json` and `opencode.json` only
  - claude.ai connectors in Claude sessions under `setup-token` mode are still unchecked (open from phase 1)
  - A claimed credential task that is not acknowledged is offered again after 2 minutes; the transit copy of its value
    stays in the secret store until then (and is swept once a newer value supersedes it)
- [ ] Agent-host restarts and opencode attribution (phase 1 punch list) assumptions, confirm:
  - The host's shutdown call is `hostSessions.release` (tag `host`, exempt from the vocabulary like claim, report and
    ask; `release` is the plain word for giving work back). It releases every live session of the host at once
  - A turn a restart interrupts is not run again: its message stays delivered, the next host tells the agent before
    its next prompt that the turn was interrupted (and which permission requests were withdrawn), and the person
    sends the next message. Messages the host took but no turn started go back to pending and are delivered again
  - Agent-permission requests pending at a restart (or when a silent host is taken over) are denied with the note
    "interrupted by an agent-host restart; nobody declined it" rather than kept for the next host: the agent's
    request died with its process
  - A Stop (`cancel`) still queued when a host takes a session over is dropped with a transcript notice: the turn it
    meant ended with the old host. Pause and end wait for the new host as before (the host carries them out once
    the session runs)
  - A host silent past the lapse (90 s) is shown as `lost` by the 30 s sweep, so the Chat can say so before another
    host takes the session; the Chat does not guess earlier. Sessions keep their silent host until another host
    claims them, so a host that answers again keeps them
  - ACP has no message field on a permission outcome, so the reason a request was cancelled (Stop, pause, end,
    Cadence's clocks) or rejected without a person (the control plane unreachable) reaches the agent as a
    `[Cadence] …` line before its next prompt, and people as a transcript notice
  - opencode attribution matches the host's completed Cadence tool call to the oldest command of the same operation
    by the same session within 2 minutes that still carries the MCP server's synthetic id (`mcp:<session>/<rpc id>`);
    parallel calls of one operation could swap ids. The outbox events of those commands are rewritten in place
    (causedBy and payload), the one exception to an append-only outbox, and `mix.attributed` tells open panels that
    the current revision's cause changed
- [ ] Worktree watcher and three-way Session changes (phase 1, closing the roadmap line) assumptions, confirm:
  - The watcher runs only while a turn runs (the worktree changes only then) and reports paths, status, size and
    line counts, never content: `hostSessions.report` `working` (at most 200 files, `truncated` beyond). The live view
    is "which files, how much"; the diff itself appears when the turn commits. Carrying content (or a patch) would make
    every keystroke of a large file travel and land in the outbox
  - Ignore rules are git's own (`.gitignore`, `.git/info/exclude`; the host sets no global excludes file), so the
    watcher shows exactly what the turn's commit would take; the spec's "whatever the session's config excludes" has
    no Cadence-specific list yet
  - Working changes are events of type `recipe.working` on the existing `recipe.{path}` topic (payload `working:
    true`, `sessionId`, `branch`, `status` — `clean` when a file leaves the set) rather than a new topic; the session
    keeps the last report as `AgentSession.working` (so a Chat opened mid-turn sees it) and bumps its `rev` when it
    changes. Ending the session clears it
  - The three-way comparison is its own read, `branches.compare` (`GET /projects/{p}/branches/{name}:compare`, the
    `compare` verb), not a larger `branches.get`: texts travel only when asked for, 128 KiB per version and 1 MiB per
    response. Whether a file conflicts is git's merge (`merge-tree`); the hunks are Cadence's own diff3 over Myers line
    diffs, so a whitespace-only or end-of-line conflict can show hunks that git would call differently
  - Conflicts are resolved on the branch (a new turn, or a push), not in the UI: the three-way view is read-only and
    Accept stays disabled while a conflict remains. Editing a resolution in the browser would need a commit command
    on a session branch, which agents' tokens own
- [ ] Workspace layout saves and the audit log (polish, 2026-09-30), confirm: the web saves a layout 2 s after the
      last change, skips a save equal to the stored layout, and saves at once on page hide and workspace switch
      (10 said "debounced 1 s"); committed `workspaces.set` runs get no audit row at all (a preference, not a domain
      command: the log filled with a row per window move), chosen over "one row per user and workspace per N
      minutes" because a throttled row says nothing the workspace's `rev` does not; refused attempts (412, denied)
      are still recorded. The exemption list lives in `internal/audit` (`Recorded`); saved searches (`views.set`) stay
      audited because a person names them on purpose
- [ ] Asleep sessions (polish, 2026-09-30), confirm: only an idle pause (`pauseReason.code = idle`) wakes on
      `agentMessages.new`; a pause by a person (`user`), a stuck turn, a runaway, a budget or a lost host now refuses
      the message with `409 conflict` and the reason (before, every pause but a budget one resumed on a message).
      A person's own pause could arguably wake on their message too — kept explicit (Resume) for now. An asleep
      session gets no notification and no unread dot, only a polite live-region line; the reason text reads
      "no message for 30 min" (was "30m0s")
- [ ] Pre-allowed Cadence tools (polish, 2026-09-30), confirm: only Cadence MCP tools are pre-allowed for Claude
      sessions (from the preset through the control plane, not from the repository file an agent can edit); the
      file's allow rules for shell commands, reads and edits are still not applied by Claude Code, so those keep going
      through the host's permission request and the preset (a round trip each, no person). Pre-allowing them the same
      way would need the host to trust the preset's shell rules without the per-call check
- [ ] D · Clearing a source for training (phase 2, R18), confirm: imported sources start eval-only; any change by
      an agent through `sources.edit` is gated (preset rule `registry-changes`, approval), a person edits directly,
      `sources.archive` is the admin's only (agents: `no-deletes`). Clearance is read when a mix or run is checked,
      so clearing a source makes its existing versions trainable without a re-import; the collection tag `eval-only`
      written at registration is informative and is not removed when the source is cleared
- [ ] D · Utterance identity and audio format (phase 2), confirm: `dataset_import` stores audio as 16-bit PCM WAV,
      mono, 16 kHz (`data.sample_rate`), written byte for byte the same by the pure-Python and NumPy paths, so an
      utterance's BLAKE3 identity does not depend on the host (FLAC would halve the bytes but its encoder output
      varies with libsndfile versions). Resampled sources (Common Voice MP3 at 48 kHz) hash per resampler (SciPy
      polyphase when present, linear otherwise). The first source that imports an audio owns the utterance; a
      re-import must carry the source's registry licence and kind, else the step fails
- [ ] D · Dataset version identity (phase 2), confirm: the fingerprint of an imported `dataset_version` is the sha256
      of the sorted `[audio hash, split, transcript text]` tuples (`registry.RegisterInput.Fingerprint`), not of its
      payload, so the same content re-imported into the same collection returns the version already there with its
      first lineage. A dataset artifact names audio by path inside the artifact (the plan's `audio: b3 hash` became
      a path so the artifact is self-contained; the path's blob hash is the utterance hash)
- [ ] D · Replay (R17) in phase 2, confirm: FLEURS covers 34 of the base model's 39 other locales with a
      configuration of its own; en-GB, es-ES, fr-CA, pt-PT and nn-NO share their sister variant's configuration
      until the Common Voice re-freeze. `dataset/replay-base` is one multi-language version (≈ 1 h per locale from
      FLEURS train, `all-train`); replay golden sets are dataset versions `dataset/replay-golden-<locale>` (FLEURS
      test, ≤ 300 utterances, `evalOnly`, tags `golden`, `replay`, `eval-only`) until the Golden set entity arrives
      in phase 3, which will adopt or re-register them. Without speaker ids (FLEURS) the speaker-disjoint split
      groups by transcript
- [ ] D · Step help slugs (phase 2): help slugs allow only dashes, so a step kind's help article is
      `docs/help/steps/<name with _ as ->.md` (`steps.dataset-import`); the worker's registry publishes that slug
- [ ] P (phase 2): `pipelines.run` answers `201` with the pipeline run (`plr_…`, its steps and their step job ids),
      not `202 {jobId}` — a run has one job per step attempt, so there is no single job to follow; clients follow
      `pipeline_run.{id}` or `pipelineRuns.wait` (added, read verb `wait`). `202` stays for approvals. Its `If-Match`
      is the pipeline's version (the commit that last changed the file, like `branches.accept` takes a head), `*`
      accepts any. Confirm
- [ ] P (phase 2): output hooks also run for reused outputs (a step reused by input hash in a new pipeline run), so a
      run's facade sees its outputs; hooks must therefore be idempotent per artifact hash. A failing hook fails the
      step and rolls back its writes and the output's index row (savepoint)
- [ ] P (phase 2): a directory artifact's `size` is the sum of its files' sizes (the manifest blob's own size is
      accepted when a worker declares that); an artifact row is written once by its first producer, other projects
      are linked in `artifact_projects`, and a credential scoped to a project reads artifacts linked to it
- [ ] P (phase 2): a step is `running` only once the worker protocol calls `pipelines.Engine.Leased` when it grants
      the lease; until then (and in a control plane without it) a step goes `queued → done`. Step jobs run in their
      own River queue (`steps`, 100 workers) because each waits on its lease for the whole step. A step whose job
      ended without an outcome (control plane restarted while waiting) is swept as `lost` (one retry); the worker's
      old lease must then stop at its next heartbeat (stream W)
- [ ] P (phase 2): the starter pipelines move to the plan's format (`in` wiring, outputs from the kind, only
      departures in `params`); `data-ingest` and `eval-matrix` name step kinds of phases 3–4 and fail validation until
      those exist; a new `echo` starter checks that workers run steps end to end
- [ ] W · The step queue (phase 2, confirm): a `step` job's River handler waits in `steps.Leases.Await` on its own
      River queue (`jobs.QueueSteps`, 500 waiting handlers) so it never starves the default queue; the queue itself is
      the `step_jobs` table, filled when Await is first called. The job mirror stays `running` while its step waits
      for a card (the Queue shows `waiting | paused | running | stopping`); priority and pause live on the job
      (`jobs.edit`, `jobs.pause|resume`); a cancelled running step ends at once for the pipeline while its card frees
      only on the worker's release or reaping
- [ ] W · Card slots (confirm): a job reserves its declared `memoryGb`, or the card's whole remaining cap when it
      declares none (so a training step that declares no memory takes the card alone); an idle card must also show
      that much free memory in the worker's telemetry, with 1 GB of slack for driver bookkeeping. A step with
      `gpu: false` takes no card and may go to a worker without cards. Only training is stopped at a window close;
      an unknown estimate, or a step resuming from a training state, starts whenever its window is open (it is
      paused at the close anyway), so a resumed run is never locked out by its original estimate
- [ ] W · Workers (confirm): one worker row per runtime and host (a restart re-registers it; a new `instance`
      reaps the old process's leases at once); a framework step kind `name@version` may be published by one runtime
      only and neutral kinds must agree on their schema hash (`step-kind-conflict`); a `cwk_` token names its host
      (the credential's subject), `CADENCE_WORKER_TOKEN_FILE` is issued for `CADENCE_WORKER_HOST` (default
      `staging`). Secret names become environment variables in upper case with `-`/`.` as `_` (`hf-token` →
      `HF_TOKEN`); a step whose secret is missing fails at lease time with error type `input`. Job logs are kept in
      files only (the global search index of `warn`+ lines from R15 is not built yet)
- [ ] O · Notifications and operations (phase 2, stream O), confirm: the instance timezone is a policy
      (`policies.timezone`, default `defaults.yaml` `operations.timezone` = UTC) that quiet hours, the digest and the
      backup schedule follow; a rule's timing applies to Telegram only (the in-app history is always immediate, its
      checkbox only switches a class off); quiet hours *drop* Telegram messages (they stay in-app and the digest lists
      open approvals) rather than deferring them; a Telegram press decides as the one admin account (`usr_admin`)
      with `actor.channel = telegram` (a contract field) through `approvals.approve|deny`; the bot answers nothing to
      chats outside the allowlist but lists them in Settings; the bot token is the secret `telegram-bot-token`
      (kind `telegram`), replaced through `telegramBot.set`; backup sets carry the sealed secret values but never the
      master key; the content-store mirror is never pruned; the set taken on the restore-test weekday is the weekly
      set; failed sets keep no files. Event types other streams should emit for the routing table (or add to
      `internal/notify/classify.go` and `web/src/shell/notifications/classes.ts`): `mount.unhealthy` (failure);
      `gate.verdict`, `deployment.promoted`, `schedule.finished`, `batch.closed` (outcome); `checkpoint.saved`,
      `triage.item_added` (progress) — on a non-entity topic. Step outcomes are `pipeline_run.step_changed` and host
      loss `compute.health` (classified by payload, F); `pipeline_step.done` was dropped (nothing emits it) and
      `compute.card_closed` waits for per-card health
- [ ] S · Job logs (phase 2), confirm: a job's log is one NDJSON file `$CADENCE_DATA_DIR/job-logs/<jobId>.ndjson`
      (lines `{t, level, msg, fields}`), not a content-store blob, read through `jobLogs.list` and tailed on
      `job.{id}.log`; a daily chore deletes files untouched for 14 days (hard-coded, not a `defaults.yaml` key)
- [ ] S · Retention of training data (phase 2), confirm: v1 deletes no content-store blob (the backup mirror is never
      pruned either) and never deletes a metric point — points in `metric_points` live as long as their run, and no
      code path deletes either
- [ ] S · Availability windows (phase 2, R19), confirm: windows are per card and job kind (training, eval, shadow,
      export, data), each a set of weekdays with `start`/`end` `HH:MM` (an end at or before the start closes the next
      day, `24:00` is midnight) and its own IANA `timezone`; a window naming none follows the instance's
      `policies.timezone` (like quiet hours, the digest and backups), resolved at check time (F, phase 2 — it
      defaulted to UTC in wave 1); no windows means always open; only training is stopped at a
      close; an unknown estimate or a resumed step starts whenever its window is open
- [ ] S · Playbook format (phase 2, R16), confirm: a playbook input is taken from the project with `from: project`
      (the base-model input: the project's default base model) or from `defaults.yaml` with `defaultRef`, and a chain
      step not built yet carries `phase: <n>` so the estimate lists and skips it (03 "Playbooks"; stream K builds it)
- [x] F · resolved (phase 2): the toy pack reads only the `dataset` directory artifact of 02 "The dataset artifact"
      (`dataset.json` with `format: cadence.dataset/1`, `manifest.jsonl` with `audio` as a path inside it) and names an
      utterance by the BLAKE3 hash of its audio file; its fixtures are a `folder-csv` import folder (`metadata.csv`),
      and the conformance suite starts with a `dataset_import` stage, so import → calibrate → train → … runs end to end
- [x] F · resolved (phase 2): the project's queue priority is `budgets.queuePriority` (−100…100, default
      `budgets.queue_priority_per_project` = 0, set by `projects.new`/`projects.edit`); the claim and `queue.list`
      order by it, then the job's priority, then first come. It is read live from the project in the claim query
      rather than copied into the step spec by the pipelines engine, so an edit reorders jobs already waiting
- [x] F · resolved (phase 2): `pipelines.run` (dry run included) answers `eval-only-dataset` when a training step
      (`resources.jobKind` training or unset) reads, straight from `$inputs.<name>`, a `dataset` artifact that an
      eval-only version registers (`payload.artifact.hash`, `data.Trainable`) or a `mix` artifact referencing one;
      inputs only eval, data or export steps read may be eval-only. Assumptions: a mix artifact names its dataset
      versions in `meta.datasets` or in its JSON content's `groups[].datasets` / `datasets` (stream R renders it);
      a dataset artifact no version registers passes; only direct reads are checked, not outputs derived from it
- [x] F · resolved (phase 2): notifications classify the events that exist — `pipeline_run.step_changed` on
      `pipeline_run.{id}` (step `done` → progress, `failed`, i.e. no retry left → failure) and `compute.health` on
      `compute.{id}` (`unreachable` → failure); a step job's own `job.state_changed` is no longer noticed (the step
      event tells it once per step, not per attempt). `pipeline_step.done` and `compute.card_closed` left the routing
      table; `TestClassTableMatchesWeb` keeps `classify.go` and `classes.ts` equal
- [ ] gap (phase 2): card health is per host (`unknown | healthy | unreachable` from heartbeats); nothing closes a
      card's slot for an unhealthy card, so nothing emits `compute.card_closed` (it rejoins the routing table as a
      failure when it does). Not small: the worker's card telemetry carries no health field today (NVML errors, a
      card missing from the report), `card_slots` has no closed state, and the claim and reopening need it (06
      "Notifications", "Failures")
- [ ] gap (phase 2): "a checkpoint and training state every 20 minutes" (03 "Key defaults") has no `defaults.yaml` key
      and no step kind implements it yet; the NeMo pack's train kind must, with the interval from `defaults.yaml`
- [x] gap (phase 2): `metrics.get` (series binned for charts, R53) is not in the contract yet; `internal/telemetry.Get`
      is ready for stream R to expose — resolved: `metrics.get` is in the contract and served (stream R)
- [ ] gap (later): job-log field search and the global search index of `warn`+ lines (R15) are not built; remote
      workers have an upload path (`workerArtifacts.set`) but no download path
- [ ] deferred (later, decided 2026-09-30): per-kind MCP tool descriptions — step kinds' parameter schemas are not
      rendered into a tool description per kind; agents read `stepKinds.get` (schema with `x-cadence`) and the
      `pipelines.run` dry run (resolved parameters, departures), which covers phase 2. Revisit when playbooks or
      agents show they need it; no code in phase 2
- [ ] H (2026-09-30, noise bank): a noise bank is its own registry kind `noise_bank` (collection `noise-bank/<name>`),
      not a `dataset_version` tagged `noise-bank`: it is produced by `dataset_import` with `purpose: noise` (clips
      without transcripts; the `dataset` hook registers the source and the version, and writes no utterances or
      transcripts), so noise never lands in the utterance store, search or a mix. Spec 02 "versioned like a dataset"
      holds (frozen, fingerprinted by content, licence from the source). Applying it on the fly is the NeMo pack's
      augmentation (it joins when the pack reads `noise-bank/*`); the MUSAN import runs on the stand in the gate wave
- [ ] H: the toy model gained a layer norm (it converged too late: WER near 1 at 300 steps) under the same
      `toy_train@1`; the toy is CI-only, and checkpoints written before load without it
- [x] G2 · resolved (2026-10-01 gate rehearsal): every validation's checkpoint is registered — steps publish
      intermediate outputs during a lease (`workerOutputs.new`, `ctx.publish`, 06 "Worker protocol"); the NeMo kind
      saves a `.nemo` per validation (seconds of each lease, not measured on the card yet) and links `checkpoint_best`
      to the published one
- [x] G2 · resolved in part: estimates add a per-lease overhead (calibration `leaseOverheadSeconds`: model load timed,
      one `.nemo` save timed, state save ≈ 3 of those; else `estimates.lease_overhead_seconds` 120 s); the ± covers the
      steps only
- [ ] G2 · open: the calibration's seconds per step is still compute only (0.38 s vs 0.53–0.56 s in the loop:
      validation passes, metric cadence, dataloader); validations × validation time is not in the estimate (the
      calibration runs no validation and the estimate does not read `val_every`); one pass on the stand would give the
      ratio to fold in
- [x] G2 · resolved by stream E (2026-10-01): content-store retention is built as designed (06 "Artifacts, metrics
      and logs", Retention, "As built"): `artifacts.evict`, approval-gated for everyone and forbidden to agents, the
      `artifact_files` index (migration 0020, backfilled at start), `cas.Store.Delete`, the approval-decided job,
      `artifact.evicted` and an audit entry with the bytes freed
- [ ] E · assumption: a blob deleted by an eviction can race a new artifact that lists the same file blob while the
      job deletes it (the writer's `Put` sees the blob and skips writing; the job then deletes it). Training-state
      files (optimiser state) are not shared with anything in practice, so the job does not lock writers; file blobs
      listed only by unindexed lease outcomes (outputs of stopped steps other than training states) are not
      protected either. Revisit when retention reaches other artifact types
- [ ] E · assumption: with backups configured, eviction keeps any state whose blobs the backup mirror does not hold
      yet (rather than copying them there itself), so the nightly backup must run before space comes back;
      `backups.new` makes it immediate
- [x] G2 · resolved (2026-10-01 gate rehearsal): the NeMo step's `val_wer` scores the validation manifest's raw text
      (looked up by the tokenizer round trip of NeMo's decoded reference), both sides normalised as evaluations are
      (NFKC, case-folded, no punctuation) — assumed the right normalisation for model selection, so val_wer and eval
      WER are comparable; a speaker-disjoint import holds out at least `data.min_validation_utterances` (100)
      utterances, moving whole speakers (or transcripts) in split-fraction order, never past half the import
- [x] G2 · resolved: progress reports no longer change a job's `rev` (only state, priority, pause and cancel do), so
      `jobs.pause` with a revision read before some heartbeats succeeds; `job.progress` events carry the progress
- [x] G · resolved: a control-plane stop interrupts (snoozes) a step job waiting on a worker instead of failing it; the
      next start waits for the same lease (`jobs.ErrInterrupted`); a step kind that sizes itself to the lease's cap
      declares no `memoryGb` (the NeMo kinds declared 24, above the staging cap)
- [ ] H: `TestProjectQueuePriority`'s one failure did not reproduce (20 runs alone, 8 beside `TestPipelinesOverHTTP`,
      two full integration runs); nothing changed there. The cancel flake was a real race (fixed in `internal/jobs`)

Phase 3 (stream S, 2026-10-02). Answered by the owner-delegated decisions of `docs/review/2026-10-02-phase-3-plan.md`
"Decisions taken for phase 3" (decision-log rows in `00-overview.md`; folded into 02, 03, 04, 06, 10 and 11):

- [x] S3 · answered (decision 1, R23): the base model's existing `base_model` registry version is the baseline model
      version; the `baseline` alias points at a `base_model` or a `model` version; unset means the default base model
- [x] S3 · answered (decision 2): `baselines.set` is `aliases.set` with name `baseline` (approval-gated, R8)
- [x] S3 · answered (decision 3): gates are the project repository's `gates.yaml`; `gates.get|edit` go through the
      recipes service; an eval's verdict records the file's SHA
- [x] S3 · answered (decision 4, R22): eval records are a global table keyed by `modelKey × goldenSetVersionId ×
      normalizerVersionId × decodingHash × scorer@version`; `modelKey` = weights hash, or `base:<versionId>` for a base
      model version
- [x] S3 · answered (decision 5): the punctuation-insensitive companion score is `werNoPunct` (the scoring normalizer
      plus punctuation removal)
- [x] S3 · answered (decision 6): base models are evaluated through the family role `materialize` (`base_model` →
      `checkpoint`); evals always transcribe a checkpoint
- [x] S3 · answered (decision 7): `goldenSets.freeze` takes a frozen eval-only dataset version and a normalizer version
      and registers `golden-set/<name>` (registry approval); the replay golden datasets and FLEURS he are frozen at the gate

Found while folding the plan into the spec; answered by the streams as built (stream S2, 2026-10-02):

- [x] S3 · toy materialize kind: answered — the toy pack publishes its own kind `toy_checkpoint_from_base@1` (03
      "Runtimes, model families and latency profiles")
- [x] S3 · leakage at `goldenSets.freeze`: answered — the set is compared with every dataset version not registered
      eval-only and every version a run trained on, in any project, counting only their train and validation splits
      (02 "Leakage and training exclusion", as built)
- [x] S3 · `gates.yaml` collections: answered — they resolve to the project's adopted versions (newest per
      collection); a collection the project has not adopted is `gate-config-invalid` (04 "Block 3", as built)
- [ ] S3 · new normalizer versions: still no operation to freeze one (seeds only); a pack references a collection and
      golden sets pin the version they were frozen with. Phase 4 (the ITN and project-specific scoring rules)
- [ ] S3 · gate checks the spec names but `gates.yaml` lacks (entity recall on boosted terms, entity accuracy, maximum
      degradation under `telephony`): still reported, not gated; boosted-term recall has no scorer yet (03 "Hot words")
- [x] S3 · robustness cells: answered — the augmentation (kind, profile hash, seed) enters the decoding hash, so only
      augmented cells change key (03 "Scorers and metrics", stream R)
- [x] S3 · utterance audio path, signed URL lifetime and the audited event: answered by stream A as built —
      `audio.get|sign`, `peaks.get`, `spectrogram.get`, `words.get` under the `media` tag; signed links live
      `media.signed_link_ttl_s` (300 s); `audio.sign` and every play that starts write an audit row (06 "Media")
- [x] S3 · `transcriptions.new` as a command under the `media` tag: answered by stream T as built — a single-use
      ticket (`transcriptions.ticket_ttl_s`) opens the live socket; the `interactive` job kind (06 "Transcriptions and
      the live channel as built")
- [x] S3 · `models.register` approval: answered — a passed gate for everyone, and a registry-scope approval for an
      agent's call (preset rule `registry-changes`); people register without one (02 "Model versions", as built;
      00 decision log)
- [x] S3 · agents and gates: answered as written — an agent's `gates.edit` is approval-gated (`evaluation-gates`), its
      worktree edit reaches main only through accepted session changes (04 "Block 3", as built)
- [x] S3 · the `boost_list` artifact: answered — the control plane renders a pack's `boost/<domain>.txt` (`# weight:`
      header, one term per line) as JSON `{terms, weight}` (03 "Artifact types")
- [x] S3 · R42's `eval report` artifact: confirmed not produced; `evals.get` is the report

Phase 3 waves 1–2, assumptions the streams made (owner-delegated: answered by default as built; the owner may
overrule; stream S2 folded them into the spec, 2026-10-02):

- [x] G · a golden set is frozen only from a dataset version *registered* `evalOnly`; one that is eval-only only because
      a source is not cleared is refused (02 "Golden sets", as built)
- [x] G · one locale per golden set; the normalizer's locale must share the dataset's primary language subtag unless
      `*`; `groups: call` is refused until datasets carry call ids
- [x] G · the freeze is an approval for everyone, people included (`golden-set-freeze`, `everyone: true`), not only for
      agents (05 "Guardrails" said agents; R8's reasoning for baselines applies to golden sets)
- [x] G · "trained on" = the dataset versions of the mix revisions a project's runs used plus the `dataset`/`mix` inputs
      of its training pipeline steps; `goldenSets.get` "used by" lists adopting projects only (the rest through lineage)
- [x] Gate finding (c8d9285) · leakage checks count only the training side's train and validation splits (00 decision
      log)
- [x] L · an agent's `langpacks.edit`/`boost.edit` follows the draft policy `language_pack`: `draft` → a branch
      `langpack/<locale>-<date>` accepted with `branches.accept`; a person's edit commits to main
- [x] L · the search index folds with the scoring normalizer's character steps but keeps punctuation
- [x] L · Serbian scores with `normalizer/basic` (no transliteration step); its golden sets are imported transliterated
- [x] Y · the scores `group` is all-or-nothing per dataset (call, else speaker, else audio hash); alignment ties go to
      fewer substitutions; CER counts spaces; partial stability is positional (03 "Scorers and metrics", as built)
- [x] Y · NeMo phrase boosting is the GPU boosting tree on the greedy label-looping decoder, `packs.nemo.boost_weight`
      0.5 (measured: over-boosting from ≈ 0.7)
- [x] E · `evals.new` answers `201` with the eval (00 decision log); estimate `eval.gpu_hours_per_audio_hour` 0.025,
      calibrated on the stand (2026-10-02: the pipeline decoder at batch 8 runs at RTF 0.0165, plus a 1.5× margin)
- [x] E · the gate reads decoding 0 (and augmentation 0) at the primary profile, matched by name then latency; with no
      golden sets in `gates.yaml`, the project's locales are targets and the rest replay; no target → the target check
      fails
- [x] E · an explicitly named golden-set collection the project has not adopted falls back to its latest version in
      `evals.new`; patterns match adopted versions only
- [x] E · `models.register` defaults the collection to `model/<project slug>`, takes the base model's licence and
      `locale:` tags, and adopts the new version into the project
- [x] X · approving `sweeps.run` covers all its runs, also on later days (the GPU-hour cap is the bound); a failed run
      does not stop the sweep; experiment runs start from the base model; sweep parameters are the train step's own;
      every point reads the recipe at the first point's commit
- [x] R · entity accuracy and latency to final are reported, not gated; latency uses simulated real-time pace; the VAD
      model (NVIDIA Frame-VAD Multilingual MarbleNet v2.0, NVIDIA Open Model License, passes R26) is pinned in
      `packs.nemo.vad_*`, not the registry; augmentation runs on target golden sets only; a failed metric step fails
      the eval
- [x] U · one `models.register` and one `evals.new` command in the web for every entry point (00 decision log);
      Lineage sits in the Eval workspace's right column; deltas are shown in percentage points
- [x] S5 · waveform, regions, timeline and minimap are Cadence code (wavesurfer.js rejected); peaks are 720 KB per
      channel-hour; the spectrogram FFT is JavaScript in a Web Worker; the 150 MB budget is the renderer process's (10
      "Audio view and charts"; 08 R51, R52 annotated)

Still open after waves 1–2:

- [x] A5 · **owner:** the primary cell `160ms` (`[56,1]`) is not a look-ahead Nemotron 3.5 was trained at (80, 320, 560,
      1120 ms are); keep `160ms` (R20, its WER sits between its neighbours) or move the primary cell and the gate's
      `primaryProfile` to `320ms`? Proposal: a `trained` flag on the family's latency profiles either way
      (`docs/spikes/A5-live-transcription.md` "Result", surprise 2 and proposal 3). Not answered by default
      **Answered 2026-10-03:** the owner chose `80ms` (`eval.primary_profile`, `packs.nemo.profile`; 00 decision log); no `trained` flag yet
- [x] L/Y · boost weight defaults disagree: a new boost list starts at `langpacks.boost_weight` 1.0 and `evals.new`
      passes the list's weight to the step, while the measured NeMo optimum is 0.5 (`packs.nemo.boost_weight`) and 1.0
      already over-boosts (WER 0.466 → 0.490). Proposal: `langpacks.boost_weight` 0.5
      **Answered 2026-10-03:** `langpacks.boost_weight` 0.5 and the starter packs' headers 0.5 (owner)
- [ ] E · not built: eval records and their `scores` are not protected from eviction (an evicted cell shows a delta
      error; superseded: protected by the phase-3 audit, kept by age since 2026-10-03, below); `evals.new` has no playbook estimator (the playbook uses a 0.5 GPU-hour hint). Resolved: the GPU-hours
      factor is calibrated (0.025, above); `playbooks.CurrentPhase` is 3 (audit fix F4)
- [ ] R · GSM-FR, AMR-NB and Opus are left out of `augment_dataset@1`'s draw (reported per cell); the frame-VAD's card
      names no Hebrew
- [x] A5 · resolved: live and eval use one decoder — `nemotron_transcribe@3` decodes through NeMo's streaming
      pipeline (03 "Runtimes, model families and latency profiles"); the training augmentation's telephone stage
      resamples polyphase (`nemotron_finetune@2`, `cadence_worker.resample`)
- [ ] U · not built: "test a phrase" in the Language pack, the Eval workspace's Playwright smoke. Resolved: Audio
      opens from Eval report and Diff rows (stream A); the Eval report draws the Robustness, entity accuracy and
      Streaming (latency) sections (stream U2); Set as baseline, Adopt into project, the gate editor and the Run eval…
      form are built (audit fix F4; 11 "Panel catalogue", as built)
- [x] Contract text: resolved — `normalizers.list`'s description in `api/openapi.yaml` names `normalizer/he-il`

Phase-3 audit (2026-10-02; four read-only auditors, fixes F1–F4 merged into the phase-3 PR). Fixed: the training
guard reads artifact types from the index and checks resolved inputs; agent branches that touch `gates.yaml`,
`lang/`, `project.yaml`, `.claude/` or `opencode.json` wait for a person; the verdict requires the project's
baseline and every gates.yaml set; registration uses the latest gated eval; spending commands never run unweighed;
the primary profile resolves by latency; the decoding hash covers resolved transcribe params; CER drops spaces
(`wer_score@2`); reported-only metric steps are optional; per-chunk latency timing; normalizer parity Go ↔ Python;
stale step-kind pins are refused at planning; gate verdicts notify; media is people-only with bounded conversions,
range-proof audit and hardened ffmpeg input; eval artifacts are protected from eviction. Open, answered by default:

- [x] Eval artifacts (hypotheses ≈ 280 MB, scores ≈ 27 MB per 76-cell eval on the stand) are never evicted and the
      backup mirror copies them: a retention policy is needed before evals run nightly (default meanwhile: keep all).
      **Owner (2026-10-03): by age** — the per-utterance artifacts go `eval.artifact_retention_days` (30) after the
      record's last use unless a registered model's eval or an unfinished eval links them; summaries, deltas and
      verdicts stay for ever (06 "Artifacts, metrics and logs", Retention; decision log). Built the same day
- [ ] Assumptions of the eval retention (2026-10-03): a record's "last use" includes evals that linked it from the
      cache, not only its computation; evicted records are computed again rather than linked, and the refreshed
      record keeps its first summary (a GPU decode may differ by a word, so a refreshed record's rows can disagree
      slightly with its summary); the sweep runs without an approval but, with a backup mirror, only after the mirror
      holds the blobs. Open: the mirror itself is never pruned, so eval artifacts accumulate there (≈ 307 MB per
      76-cell eval); pruning mirror blobs evicted before the oldest kept backup set would make those evictions
      permanent — owner to decide
- [ ] The media span cache is bounded by size only; the play-audit dedupe is in memory (a restart audits a play
      again); the conversion bound is global, not per user (default: 2 conversions)
- [ ] A failed gate notifies as an outcome, not a failure (a gate saying no is the system working)
- [ ] Step-kind versions a runtime stops publishing are not marked `deprecated` (that state is one-way and would block
      an image rollback); planning reads the workers' latest registrations instead
- [ ] `batch_size` stays in the eval decoding hash: at 80 ms, batch 1 and batch ≥ 2 differ by a word in 1 of 10 clips
      (GPU numerics); an OOM retry at 0.75× batch can still change words without changing the key
- [ ] The eval record cache reset once with `wer_score@2` (CER without spaces for every language, the FLEURS/Whisper
      convention) and the fuller decoding hash; earlier records stay but are not read
- [ ] Latency to final paces each chunk by the step's wall time divided by the streams in it, a lower bound for a lone
      stream; the measured numbers in the latency_score help predate it
- [x] **owner:** the NeMo GPU tests and the nightly NeMo conformance never run in CI: a self-hosted runner on the GPU host
      is not installed because the repository is public (a pull request from a fork could run code on the host).
      Options: a runner restricted to the default branch and `workflow_dispatch`, or a host cron that runs the
      conformance suite and reports to Telegram. Not answered by default
      **Answered 2026-10-03:** a cron job on the GPU host runs them nightly from `main` and reports to Telegram (`scripts/nightly-gpu.sh`; owner)
- [x] The base model has no usable Thai (empty output with every decoder and prompt): drop `replay-golden-th-th` from
      the replay sets or keep it as a documented always-empty set (default: keep, it cannot regress)
      **Answered 2026-10-03:** Thai leaves the replay corpus and golden sets (owner)

Phase 4 (stream S, 2026-10-04). The owner-delegated decisions of `docs/review/2026-10-03-phase-4-plan.md` "Decisions
taken for phase 4" are answered as built (decision-log rows in `00-overview.md`; folded into 02, 03, 04, 05, 06, 10
and 11):

- [x] S4 · answered (decision 1): the first mount is `corpora` (`local`, read-only, registered with root
      `/mnt/corpora`, the path every container sees; the host directory is `/cadence/corpora`); `exports`
      (`/mnt/exports`, writable) takes exports and the backup mirror
- [x] S4 · answered (decision 2): the cache is the content store; pins, LRU eviction at 85 → 70 %, project quotas of
      200 GB over the versions a project froze
- [x] S4 · answered (decision 3): index in place; the canonical hash is the BLAKE3 of the 16 kHz PCM16 mono WAV of the
      segment (header included), the utterance identity imports already use
- [x] S4 · answered (decision 4): ingest ends in a draft version; `datasets.preview` and `datasets.freeze`; only frozen
      versions are trained on, exported or adopted
- [x] S4 · answered (decisions 5–8): registry kind `auxiliary`, the ensemble, LID and alignment as built (deviations
      below)
- [x] S4 · answered (decision 9): synthetic calls stand in for real calls; the real-call telephone golden set waits

Phase 4 streams, product decisions Claude made while building (owner-delegated: answered by default as built; the
owner may overrule):

- [ ] D · Is the frozen cut Lhotse Shar? **Now:** no — `cadence.dataset/1` with one canonical WAV per utterance
      (`audio/<b3[:2]>/<b3>.wav`) and Lhotse MonoCut manifests in shards of `data.shard_utterances` (2000) cuts;
      real Shar tars are an export (`shar_export@1`)
- [ ] D · Which VAD does ingest use? **Now:** an energy VAD per channel in the core step (CPU, runtime-neutral);
      `frame_vad` stays a GPU-pack kind used by evals
- [ ] D · How much ITN does `text_normalise@1` do? **Now:** a literal `{spoken, written}` list from params, no number
      grammars; the pack's training style is mirrored in pipeline params, not read from the pack
- [ ] D · Is the bot channel's TTS script training text? **Now:** it is kept with origin `model:tts-script`, but
      `manifest_filter@1` keeps roles `[caller, mono]` by default, so bot speech does not train unless asked
- [ ] D · Where is "no licence, no ingest" enforced? **Now:** at planning (a step param marked
      `x-cadence.registry: source` whose source is missing, archived or not cleared is refused,
      `source-unlicensed`) and again in the draft hook
- [ ] M · Who may add a mount? **Now:** an approval for everyone (preset rule `mount-registration`, registry scope, the
      admin decides); a mount's config is immutable (revision stays 1); S3 credentials are one secret
      `<accessKeyId>:<secretAccessKey>`; scans run in the control plane
- [ ] M · What is evictable? **Now:** only blobs with a recorded copy on a mount (`blob_copies`); re-derivable shards
      without a mount copy are not; pins = dataset versions of queued or running jobs, the lineage datasets of aliased
      model versions and golden-set datasets; the sweep runs as the system actor without an approval
- [ ] M · Is mount health per host? **Now:** per mount, checked from the control plane (`mounts.verify`), not per worker
- [ ] X · Which LID? **Now:** Whisper's language token (speechbrain's VoxLingua107 needs torchaudio, which conflicts
      with the NeMo runtime's torch 2.12); the VoxLingua107 path fails naming the fallback. sr/hr/bs count as one
      language (`lid_equivalents`), `lid_min_confidence` 0.5, `require_lid` → reason `lid-unknown`
- [ ] X · How is agreement measured? **Now:** pairwise WER over the longer text after the scoring normalizer,
      ≤ `pseudolabel.max_pairwise_wer` (0.15) for ≥ 2 members; segments that already carry non-pseudo text pass
      through. Known issue: when OASIS is in the agreeing pair its text wins (`vote`), so a pseudo-label can lose case
      and punctuation (OASIS writes lower case without punctuation) — prefer a cased member's text?
- [ ] X · Who may adopt an auxiliary? **Now:** an approval for everyone (`auxiliary-adoption`, registry scope);
      `outputsCommercialUse: false` is refused outright (`auxiliary-licence-refused`); a service member is probed at
      dry run and start (`auxiliary-unavailable`, 503)
- [ ] X · Whisper writes Serbian in Cyrillic: the pseudo-label pipeline transliterates (`sr-Cyrl-Latn`) its output
- [ ] R · What does adoption check? **Now:** licence (`licence-forbids-adoption`: no usable licence, outputs not for
      commercial use, NC/ND on base models, models, noise banks, auxiliaries and non-eval-only datasets; NC/ND allowed
      for golden sets and eval-only datasets) and locale (`locale-mismatch`) unless `purpose: replay`; runtimes,
      families and step kinds are never adopted
- [ ] R · How does `data.lock` act? **Now:** a step param marked `x-cadence.registry: <kind>` resolves through the
      lock at the pipeline's commit (`not-adopted` otherwise); the resolution is reported (`PlanStep.locked`), not
      substituted into params, and input-hash reuse ignores the resolved version
- [ ] R · Step-kind deprecation comes from the worker pack (`deprecated_after`, `replaced_by`, `deprecation_note`); plans
      warn, new pins are refused after the date (`step-kind-deprecated`); there is no `deprecate` verb
- [ ] R · Archive is terminal (`versions.archive`, state `archived`), refused while the version is in use
      (`version-in-use`); nothing is ever deleted
- [ ] I · Do imports index in place? **Now:** `dataset_import@4` copies into the content store and freezes at import
      (Lhotse cuts/Shar, the Cadence bundle, NeMo manifests with offset/duration, `mount://` on path mounts); only
      `sdp_ingest` indexes in place. Lhotse imports read file sources and path mounts only
- [ ] I · Is the Cadence bundle per project (R27's "project-level bundle")? **Now:** per dataset version
- [ ] I · What may go to the HF Hub? **Now:** an approval (`hub-export`, registry scope), private by default;
      `export-not-allowed` refuses sources not cleared or marked production and golden-set data
- [ ] I · Noise from calls: silences on every track ≥ 200 ms from speech; mined banks are tagged `mined`
- [ ] A · How do annotators get work? **Now:** each pulls items in their own hashed order; a revision is refused once
      another transcript is in; double annotation 10 % plus flagged, blind; adjudication when the two disagree above
      `annotation.adjudicate_wer` (0) after casefold and punctuation strip (not the project normalizer); items tagged
      `foreign` or `unintelligible` are excluded from the frozen set
- [ ] A · A golden set from calls needs the ingest draft registered eval-only; the batch's training-side dataset is
      `dataset/<goldenSet>-annotated`
- [ ] A · Guidelines are pinned at the repository's HEAD commit when the batch is created; reviewers see the path and
      commit, and the text at that commit through `guidelines.get` (phase 4 tail)
- [ ] A · Media windows: a triage item plays its segment ± 2 s, a batch item ± `context_s`; only local/NFS/SMB mounts
      and WAV (PCM, float, G.711) play in place, others are converted
- [ ] A · Triage is a tool panel (a queue), not a document
- [ ] L · Alignment runs by hand once per golden set (`pipelines/align-reference.yaml`), not at freeze; `srp_Latn` is not
      in omniASR's language list (Serbian Latin references stay unaligned unless transliterated); the `omnilingual-asr`
      package is not installed (the pack repeats the architecture numbers) and the weights come from
      `facebook/omniASR-CTC-1B` v1
- [x] S4 · found while folding: `versions.archive` may answer `202` (approval) in the contract, but no preset rule
      gates it — the handler only requires the admin. **Answered (audit F1):** the `202` response is removed; archiving
      is the admin's own call, no approval
- [x] S4 · found while folding: `worker-services` and `worker-omni` did not bind `/mnt/corpora` or `/mnt/exports`.
      **Answered at the gate (2026-10-04):** core steps such as `sdp_ingest` are leased to any worker, so every worker
      binds both
- [x] S4 · found while folding: `defaults.yaml`'s description of `annotation.adjudicate_wer` said "after the scoring
      normalizer", but the code compares after a neutral fold. **Answered (audit F2):** the description follows the code
- [ ] B · Try Cadence imports FLEURS from the HF Hub (`sdp_ingest` cannot read FLEURS `.tsv`); clearing a source must
      precede its ingest; export and parity steps of the playbook wait for phase 5
- [x] Gate (2026-10-04): a step that reads a mount is reused by its input hash even after the mount's files changed
      (the hash covers params and inputs, not mount content). **Now:** `fresh: true` re-runs it; should a mount
      step's hash include the scan's inventory fingerprint, or should such steps never be reused? **Answered (owner,
      2026-10-04):** a listing fingerprint of the files under each `mount://` URI in the step's params (path, size,
      mtime / ETag / Hub blob id; exclude globs applied, the pattern not — sidecars are read too) is folded into the
      input hash (02 "Storage and mounts"; migration 0044).
- [x] Gate: every retry of a pipeline run whose GPU estimate is unknown asks for a new `gpu-spend` approval, even when
      the person just approved the run. **Now:** fail closed, one approval per retry; should a retry within the same
      day inherit the run's approval? **Answered (owner, 2026-10-04):** yes, on the same UTC day and within the
      approved estimate when it was known (05 "Guardrails"; rule `inherited-approval`).
- [x] Gate: replay golden sets in `nb-NO` need `languages: {"nb-NO": "no-NO"}` because the base model's tag is `no`.
      **Now:** set per eval; should `knowsLanguage` treat `nb`/`nn` as `no` (macrolanguage), or `gates.yaml` carry
      the map? **Answered (owner, 2026-10-04):** every check against a base model's `locale:` tags folds macrolanguage
      members (`internal/langtag`; 03 "Languages").
- [x] Gate: `data-ingest` cannot draft untranscribed calls (the filter drops segments without text, the draft refuses
      them). **Now:** an annotation batch frames on the ingest's `segments` artifact; should a calls template stop at
      the segments? **Decided 2026-10-04 (owner):** yes — `pipelines/calls-ingest.yaml` (`sdp_ingest` alone, stereo
      split, roles from the sidecar) ends at the segments, the frame of `batches.new` (00 decision log).
- [x] Gate: with the base model as one of two members, the pseudo-label ensemble kept 490 of 2 944 FLEURS segments,
      no better than Whisper alone (WER 0.121 vs the references). **Now:** the template's members are unchanged and
      the book explains it; should the default ensemble leave the base model out, or weight members by a measured
      WER? **Decided 2026-10-04 (owner):** leave it out — the template votes Whisper + OASIS, OASIS is required (the
      dry run refuses without it), `pseudolabel_ensemble@2` refuses fewer than `pseudolabel.min_members` and keeps
      Whisper's written-form text when the two agree (`pseudolabel.prefer_written_form`; 00 decision log). No
      weighting by measured WER.
- [x] Gate (2026-10-04): emission delay on `golden-set/fleurs-sr-latn-test` is PR50 4.5 s / PR90 9.9 s at `80ms`
      but 0.42 s / 4.0 s at `160ms` (base model and fine-tune alike; latency to final at 80 ms p50 1.3 s): words in
      80 ms partials stay unstable until the final. **Now:** unexplained — the model at that look-ahead, or the
      pipeline decoder's partials (its NeMo shims)? Check before phase 5 deploys the 80 ms primary profile
      (`evl_01a106b7-d42b…`).
      **Answered 2026-10-04: a Cadence decoder bug, fixed in `nemotron_transcribe@4`** — neither the model nor the
      scorer. At 80 ms NeMo's endpointer closes a segment right after an utterance's first token, inside a word
      (A5 finding 4; " Dan" | "iel Lantane …"): 171 of 200 FLEURS sr test clips at 80 ms, 34 at 160 ms, 1 at 1120 ms.
      The finals carried `space: false` and joined right, but `pipeline.collect` joined every *partial* of the next
      segment to the finals with a space ("Dan iel Lantane …"), so each later word sat one place off until the
      segment's final — at the utterance's end — and latency_score@3 (which compares words by position, correctly)
      dated them all there. Partials now carry `space` like finals (`LivePartial.space`, the live lanes join them
      too). Check on 24 clips, base model, partials paced at their audio offset and word ends from the decode's own
      timestamps: PR50 / PR90 at 80 ms 4.04 s / 9.08 s before, −0.15 s / 0.01 s after — identical to NeMo's own
      cache-aware loop (`conformer_stream_step` with hypotheses carried, append-only) at 80 ms; 160 ms unchanged
      (−0.07 / 0.01 s); words and WER unchanged. On 200 clips after the fix no word at 80 or 160 ms is first in place
      only in the final. The 160 ms PR90 of 4.0 s on the stand has the same cause (17 % of clips split). Latency to
      final moves too: a split utterance's partials never equalled its final text, so @3 counted its final only at the
      last event (24 clips at 80 ms: the final text now shows a median 0.51 s before the last event, 0 s with @3).
      `nemotron_transcribe@4` changes the eval record key, so the stand re-decodes; the chapter 14 table needs a rerun
      (decision log 2026-10-04).
- [ ] Gate (2026-10-04, from the 80 ms check): NeMo's endpointer closes a segment after the utterance's first token
      in 86 % of 80 ms decodes because the pipeline config's `endpointing.residue_tokens_at_end: 2` hides the newest
      two frames from the end-of-utterance search, so the leading silence counts as trailing (with 0: no mid-word
      end of utterance on 48 clips, but 18 of 48 transcripts change). Finals and partials now join right, yet a live
      lane shows a one-token "final" at every utterance start. **Now:** unchanged (2); should Cadence set 0 (or
      scale it by chunk) after a WER check?
      **Measured 2026-10-04 (stream measure): 0 and 1 are refused — they switch end of utterance off; 2 stays.** Base
      model, 700 FLEURS sr test utterances (2.12 h, prompt `hr-HR`, references Cyrillic→Latin then `normalizer/basic`,
      batch 8, the pipeline decoder of `nemotron_transcribe@4`), residue 2 / 1 / 0: WER 80 ms 0.3558 / 0.3557 / 0.3557,
      160 ms 0.3464 / 0.3463 / 0.3463, 1120 ms 0.3107 / 0.3106 / 0.3106 (one utterance of 700 differs at each profile,
      paired bootstrap 95 % CI of the delta [−0.0002, 0.0000]; sign test 1 better / 0 worse); utterances with a final
      that continues a word (a mid-word end of utterance) 596 / 0 / 0 at 80 ms, 93 / 0 / 0 at 160 ms, 4 / 0 / 0 at 1120 ms;
      a one-token first final 699 / 0 / 0, 92 / 0 / 0, 1 / 0 / 0. But with 0 or 1 **no** end of utterance fires at all
      (utterances closed by the endpointer before the clip's end: 273 / 0 / 0 at 80 ms, 232 / 0 / 0 at 160 ms,
      115 / 0 / 0 at 1120 ms; the clips have a median 1.38 s of trailing silence): finals come only when the stream
      ends, so latency to final (VAD speech end to the partial from which the text is final, audio offsets) grows from
      p50 700 / p90 1 301 ms to 1 145 / 1 800 ms at 80 ms (mean +385 ms), 830 / 1 300 → 1 050 / 1 680 at 160 ms,
      1 240 / 1 720 → 1 320 / 1 880 at 1120 ms, and a live lane would never close a segment by itself. The 10 Hebrew
      FLEURS fixtures agree (WER 0.744 / 0.779 / 0.698 at 80 / 160 / 1120 ms for every residue; splits 6 / 1 / 0 with 2,
      none with 0 or 1). Why (NeMo 3.0.0, `GreedyEndpointing.detect_eou_near_pivot`): the pipeline's label buffer holds
      `stop_history_eou` frames + the residue, initialised to blanks; the search ends `residue` frames early, so with
      2 the newest frame (the pivot) is never looked at — the first token of an utterance arrives there after a buffer of
      "silence" (the blank fill at a stream's start, or the pause before it) and closes a segment holding just that
      token; with 0 or 1 the buffer can never hold more blanks than `stop_history_eou`, which the strict `>` test needs.
      **Recommendation (owner decides; not built):** keep 2 and add a sixth decoder shim — no end of utterance while the
      pivot frame holds a token. Measured the same way (residue 2 + shim): WER 0.3557 / 0.3464 / 0.3107 (80 / 160 /
      1120 ms; one utterance differs at 80 ms), mid-word ends 0 / 3 / 3, one-token first finals 0 / 0 / 0, utterances
      closed by the endpointer 264 / 232 / 115, latency to final p50 740 / 830 / 1 240 ms (80 ms: 14 utterances later,
      mean +30 ms); Hebrew fixtures: no split, WER unchanged. It changes the decoder, so it would ship as
      `nemotron_transcribe@5` and `nemotron_live@2` (a new eval record key: the stand re-decodes).
- [ ] Gate (2026-10-04, from the 80 ms check): NeMo warns that `att_context_size` [56,1] (`160ms`) "is not among the
      supported look-aheads [[56,3],[56,0],[56,6],[56,13]]" of Nemotron 3.5 — the model was trained for 80, 320, 560
      and 1120 ms only. **Now:** `160ms` stays a profile (it decodes, and its WER is in range); should it be marked
      untrained, or dropped from the family's profiles? **Measured 2026-10-04** (`evl_01a10790-92fb…`, FLEURS Serbian
      test, phase-4 fine-tune / base): WER 80 ms 0.349 / 0.356, 160 ms 0.335 / 0.346, 320 ms 0.325 / 0.333, 560 ms
      0.318 / 0.322, 1120 ms 0.304 / 0.311; emission PR50 0.38, 0.40, 0.45, 0.54, 0.81 s. `160ms` lies on the curve
      between its trained neighbours in both WER and delay — no sign of an untrained look-ahead. **Recommendation:**
      keep it, labelled "not a trained look-ahead" in the family descriptor; the owner decides.
- [ ] P5 spec (2026-10-05, deployment): R31's "WER difference ≤ 0.1 absolute" is read as 0.1 WER **points**
      (`deploy.parity_max_wer_delta` 0.001), as in A3's acceptance ("within 0.1 point"); 0.1 as a fraction would let
      a 10-point gap pass.
- [ ] P5 spec: the canary's default traffic share (5 %, `deploy.canary_share`) and the absence of a minimum canary
      period are Cadence recommendations; the spec only says flywheel signals decide whether the share grows.
      Production needs a confirmed canary of the same model version on the slot, nothing more.
- [ ] P5 spec: benchmarks run on the staging card; Эра's production card class may differ. A benchmark records the
      card class and the promotion modal shows a mismatch, but does not refuse it. Likewise a staging Triton version
      different from the delivery target's is shown, not refused, and both versions go into the record. **Changed by D4:** a
      server release other than the one the export's engine was built for is refused (the engine would not load).
- [ ] P5 spec: the parity sample comes from the project's first target golden set, and up to 20 of its utterances
      travel in every delivery bundle as the smoke check. For a golden set cut from Эра's calls that is Эра's own audio
      going back to Эра's host; it must still be redacted when R29 lands if bundles are kept outside the production host.
- [ ] P5 spec: shadow replay compares the candidate with the slot's production version, or with the project's
      baseline before any production exists, both decoded on the staging server. Shadow hours count each replayed
      call once by its duration. The calls mount itself (Эра's recordings, read-only) does not exist yet.
- [ ] P5 spec: the stand's card numbers (cap 29 GB, serving reserve 7 GB, training 22 GB) assume ≈ 30 GB free beside
      vLLM on the 98 GB card. `defaults.yaml` `compute` still describes the 48 GB card of phase 2; the stand overrides it
      with `compute.edit`, and stream D2 adds `servingReserveGb` (default 0).
- [ ] P5 spec: the instance signing key is created with the first delivery target. A lost key means a new chain
      (a `key-rotation` record needs the old key) and re-pinning on the production host; a leaked key is revoked by
      re-pinning. Neither case is automated.
- [ ] P5 D1 (owner to confirm): parity's identical share is 0.97, not R31's 0.995, plus a word-disagreement bound
      0.005 (`deploy.parity_min_identical_share`, `deploy.parity_max_disagreement`; Δ WER stays ≤ 0.1 points). Spike E1:
      NeMo's own decoder agrees with itself on 98.5 % of the 200 clips between batch 8 and batch 1, fp32 exports 96–99 %,
      fp16 76 % (disagreement fp32 ≤ 0.0033, fp16 0.017). The same share sets the delivery smoke check: ⌈0.97 × 20⌉ = 20,
      so every smoke utterance must still match. The stand's served fp32 engine matched NeMo's tokens on 20/20 smoke
      utterances (2026-10-05).
- [ ] P5 D1: the deployable serves spike E1's step graph and **the caller sends features** (the pipeline decoder's
      feature buffers): no featuriser is served in v1. Parity therefore compares engines on identical buffers; a
      production front end that does not reproduce `cadence_nemo.pipeline.Features` exactly (whole-stream log-mel) can
      give other finals, and parity does not measure that. Endpointing, detokenisation and locale-tag stripping stay in
      the caller too (E1). Next item: a stateful featuriser model before the step graph in the Triton repository
      (implicit state: two frames of audio and the pre-encode cache), with parity run from audio; it also lets the smoke
      set carry WAVs instead of feature files (≈ 2 MB of JSON per utterance today). Эра's answer to "tokens or text,
      endpointing in the client or in a Cadence-built gateway" (E1 question 7) decides its shape.
- [ ] P5 D1: a TensorRT engine runs only on the GPU architecture and the TensorRT it was built with. The export builds
      it on the staging card with the TensorRT of the staging server's Triton (the worker image carries `trtexec` and its
      libraries from `nvcr.io/nvidia/tritonserver:26.08-py3`, pinned by digest; Triton 26.08 → TensorRT 11.2.1), and
      records `serving.engine` (TensorRT version, card class, GPU name, compute capability) in the deployable. A delivery
      target on another card class or Triton release cannot load it: D4's promotion checks should refuse that (not only
      show it), and an engine for Эра's card needs an export on that card class (not built: one export per (version,
      profile, format)). The ONNX step graph in the deployable is the portable artifact to rebuild from. **D4 (2026-10-05):** the
      promotion's `engine` check refuses it (`target-does-not-serve`) when both sides name the card class and the
      server release; the benchmark's own card class is still only shown.
- [x] P5 D1: the staging target seeded from `serving.staging_target` names Triton 26.07; the export's engine builder is
      26.08's (`packs.nemo.export_server_version`). The staging server must run 26.08 (E1's workarounds were measured
      on it): stream D2 pins `serving.image` and the staging target's version. Stream D12 (2026-10-05): the seed
      (`serving.staging_target.server.version`), `serving.image`, the compose service `triton` (same digest) and the
      export's `server_version` all say 26.08, and an engine built by `nemotron_export@1` loaded and served on that
      image.
- [ ] P5 D1: benchmark levels default to 1, 8, 16, 32, 64, 128 (`deploy.benchmark_streams`), up to the deployable's
      128-stream capacity (`packs.nemo.export_max_streams`), which fits the stand's 7 GB serving reserve (Triton held
      6.2 GB with the 80 ms fp32 engine loaded); E1 found 256 streams within budget, which needs a larger state pool.
      The verdict reads p95 chunk latency from audio availability (E1's reading of R31's "time to final ≤ chunk + 100
      ms"); time to final is reported beside it.
- [ ] P5 D1: a waiting benchmark drains every card that accepts benchmarks on every host (not only the card it will
      take) while it is within `deploy.benchmark_drain_max_minutes` of being queued; jobs ahead of it in start order
      (interactive sessions, higher priorities) still start. One card on the stand makes the difference moot.
- [ ] P5 D12: a benchmark level's `foreignUtilPct` (what processes outside the server use of the card) is the card's
      utilisation sampled for 2 s before and, after a 1.5 s settle, 2 s after the level, with the model loaded and no
      stream of the step running (the larger): the worker cannot tell the server's processes from others by PID inside
      its container. A foreign load that starts and stops within the level is not seen. Likewise `memoryUsedMb`, so the
      report's `servingMemoryMb`, is the whole card's used memory (74 GB on the stand with the resident services), not
      the server's; the model's own footprint is the serve step's load measurement (`tookMb`, 4.1 GB for the 80 ms
      engine with a 2 GB pool).
- [ ] P5 D12: the served text follows NeMo's text processor where it is clear (no space before closing punctuation; a
      final that starts with it continues the previous word), but NeMo's cache-aware pipeline drops a trailing `.` the
      model emits after the last endpoint on some utterances (18 of 49 token-identical FLEURS sr utterances), and the
      served client keeps it. Parity compares token ids, and the scoring normalizer strips punctuation, so ΔWER is 0;
      a normalizer that keeps punctuation would count it. Matching NeMo exactly means reproducing its segment state
      machine in the client, or serving the text (Эра's answer to E1 question 7).
- [ ] P5 D12: `nemotron_serve` in batch mode streams from one process (a thread per stream, precomputed features). On
      the stand 64 real-time streams held p95 19.7 ms; E1 needed 4–8 client processes beyond about 128 streams, so the
      higher benchmark levels (128) may measure the client's GIL as much as the server. Split the streams over
      processes if a level's chunk latency rises while the server's own queue and compute times (`server` row) do not.
- [ ] P5 D12: the NeMo pack's conformance run (nightly) now reaches the export, parity and benchmark stages through
      `nemotron_serve`, which needs a staging server in its lease; the nightly image has no Triton, so those stages fail
      there until the nightly job starts one (the compose profile `serving`) or the suite skips serve without a lease.
- [ ] P5 D4: `deployments.new` requires `replay.source` (a registered source for the recordings: "no licence, no
      ingest"), which 02's sketch `{mount, path?}` did not name; Эра's recordings need a production source registered
      (and cleared) before their first shadow.
- [ ] P5 D4: "newest unreplayed calls first" uses the mount's file modification times (an S3 mount's stamp is an
      ETag: its calls are taken in reverse path order instead) and sizes a call from its WAV header, else at 16 kB/s
      (G.711 stereo). Эра's recordings layout and format (R32 е) may need a better clock (a sidecar's call time).
- [ ] P5 D4: the nightly shadow replay runs as the system actor without a GPU-budget decision (a project's spend is
      not checked at night); `shadowReplays.new` goes through the usual `gpu-spend` policy.
- [ ] P5 D4: shadow retention evicts the night's artifacts permanently, but a backup mirror that already copied them
      keeps them; F1's retention sweep (R28) should cover the mirror too.
- [ ] P5 D4: `sdp_ingest` moved to @3 (`files`); a project repository that pins `@2` (the stand's projects) must bump
      its pins: a worker publishes one version of a kind.
- [ ] P5 D4: marking a shadow segment for triage (11, Shadow panel) is left to stream F2 (the triage queue of
      production samples); the Shadow panel opens segments in Diff and Audio only.
- [ ] P5 D4: the benchmark chart draws what `models.get` carries per benchmark (the verdict level's p95 against the
      budget, the most streams within it); a per-level latency curve (R53) needs the report's levels in the API.

## Sources

- [Nemotron 3.5 ASR model card](https://huggingface.co/nvidia/nemotron-3.5-asr-streaming-0.6b) — release date, languages, latency settings, licence
- [How to fine-tune Nemotron 3.5 ASR](https://huggingface.co/blog/nvidia/fine-tuning-nemotron-35-asr) — recipe, streaming evaluation, replay
- [NeMo Speech repository](https://github.com/NVIDIA-NeMo/Speech) — NeMo Speech 3.0 and the 26.07 container
- [NeMo ASR fine-tuning skill, PR #15733](https://github.com/NVIDIA-NeMo/NeMo/pull/15733)
- [Community Nemotron 3.5 fine-tune kit](https://github.com/Piyazon/nemotron-3.5-asr-streaming-0.6b-custom-finetune-kit) — learning-rate and tokenizer pitfalls
- [Kenyan-language adaptation of Nemotron 3.5](https://arxiv.org/abs/2607.18912) — leakage finding
- [Agent Client Protocol overview](https://agentclientprotocol.com/protocol/overview)
- [Claude Agent SDK TypeScript reference](https://code.claude.com/docs/en/agent-sdk/typescript)
- [opencode server](https://opencode.ai/docs/server/), [SDK](https://opencode.ai/docs/sdk/) and [skills](https://opencode.ai/docs/skills)
- [Lhotse dataloading in NeMo](https://docs.nvidia.com/nemo/speech/nightly/dataloaders.html)
- [NeMo Curator audio curation](https://docs.nvidia.com/nemo/curator/curate-audio)
- W&B Registry documentation — organisation-level registries, collections, aliases, lineage (pattern for the Cadence registry)
- M. Bisani, H. Ney, "Bootstrap estimates for confidence intervals in ASR performance evaluation", ICASSP 2004 — significance of WER deltas
- T. Gebru et al., "Datasheets for Datasets", CACM 2021; M. Mitchell et al., "Model Cards for Model Reporting", FAT* 2019 — generated cards
- ISO 9241-110:2020 interaction principles; WCAG 2.2 as ISO/IEC 40500:2025; RFC 9457 and RFC 9110 — standards cited in the UI shell tab

Added 2026-09-29 with R40–R54 (extensibility, trying models, audio views and charts):

- Frameworks:
  - [icefall](https://github.com/k2-fsa/icefall) and its [installation order](https://k2-fsa.github.io/icefall/installation/index.html)
  - [k2 CUDA wheels](https://k2-fsa.github.io/k2/cuda.html) and [Lhotse](https://pypi.org/project/lhotse/)
  - [sherpa-onnx releases](https://github.com/k2-fsa/sherpa-onnx/releases) and its [Nemotron 3.5 export](https://github.com/k2-fsa/sherpa-onnx/tree/master/scripts/nemo/nemotron-3.5-asr-streaming-0.6b)
  - Zipformer, [arXiv:2310.11230](https://arxiv.org/abs/2310.11230) — training cost table; CR-CTC, [arXiv:2410.05101](https://arxiv.org/abs/2410.05101); FastConformer, [arXiv:2305.05084](https://arxiv.org/abs/2305.05084)
  - Precedents: [Kubeflow Trainer](https://www.kubeflow.org/docs/components/trainer/overview/), [W&B Launch](https://docs.wandb.ai/platform/launch/launch-terminology), [MLflow models](https://mlflow.org/docs/latest/ml/model/)
  - OpenAI-shaped speech servers: [vLLM speech-to-text](https://docs.vllm.ai/en/latest/serving/online_serving/speech_to_text/)
- Live testing:
  - NVIDIA: [NeMo streaming inference pipelines](https://github.com/NVIDIA-NeMo/Speech/tree/main/nemo/collections/asr/inference); [NVIDIA Speech NIM realtime ASR](https://docs.nvidia.com/nim/speech/latest/reference/api-references/asr/realtime-asr.html); [NeMo telephony tutorial](https://github.com/NVIDIA-NeMo/Speech/blob/main/tutorials/asr/ASR_for_telephony_speech.ipynb)
  - Vendor protocols: [Deepgram live](https://developers.deepgram.com/reference/listen-live); [AssemblyAI streaming](https://www.assemblyai.com/docs/streaming/message-sequence.md); [Speechmatics real-time](https://docs.speechmatics.com/introduction/rt-guide); [Soniox real-time](https://soniox.com/docs/stt/rt/real-time-transcription)
  - Capture: [Google STT audio best practices](https://docs.cloud.google.com/speech-to-text/docs/best-practices-provide-speech-data); [MDN AudioWorklet](https://developer.mozilla.org/en-US/docs/Web/API/AudioWorklet); [WebKit bug 281978](https://bugs.webkit.org/show_bug.cgi?id=281978) (Safari stereo capture)
  - Metrics: [Pipecat STT benchmark](https://github.com/pipecat-ai/stt-benchmark) (time to final segment); Y. Shangguan et al., "Analyzing the Quality and Stability of a Streaming End-to-End On-Device Speech Recognizer", Interspeech 2020, [arXiv:2006.01416](https://arxiv.org/abs/2006.01416); J. Yu et al., "FastEmit", ICASSP 2021, [arXiv:2010.11148](https://arxiv.org/abs/2010.11148); Z. Liu, F. Peng, "Statistical Testing on ASR Performance via Blockwise Bootstrap", [arXiv:1912.09508](https://arxiv.org/abs/1912.09508)
- Audio views and charts:
  - Spectrogram defaults in tools: [Praat editor preferences](https://github.com/praat/praat.github.io/blob/master/foned/SoundAnalysisArea_prefs.h); [Audacity spectrogram settings](https://github.com/audacity/audacity/blob/master/src/spectrogram/internal/globalspectrogramconfiguration.cpp); [librosa display](https://github.com/librosa/librosa/blob/main/librosa/display.py)
  - What models see: [Kaldi fbank options](https://github.com/kaldi-asr/kaldi/blob/master/src/feat/feature-fbank.h); [NeMo FastConformer configs](https://github.com/NVIDIA/NeMo/tree/main/examples/asr/conf/fastconformer); [Whisper audio](https://github.com/openai/whisper/blob/main/whisper/audio.py)
  - Colormaps:
    - [matplotlib colormaps, Smith & van der Walt 2015](https://bids.github.io/colormap/)
    - Nuñez et al., cividis, [PLOS ONE 2018](https://doi.org/10.1371/journal.pone.0199239)
    - Borland & Taylor, "Rainbow Color Map (Still) Considered Harmful", IEEE CG&A 2007
    - Crameri, Shephard & Heron, [Nature Communications 2020](https://doi.org/10.1038/s41467-020-19160-7)
    - [Turbo](https://research.google/blog/turbo-an-improved-rainbow-colormap-for-visualization/)
  - Libraries: [wavesurfer.js 8.0.0](https://github.com/katspaugh/wavesurfer.js/releases/tag/8.0.0); [uPlot](https://github.com/leeoniya/uPlot); [Apache ECharts](https://echarts.apache.org/)
  - ASR views: [NeMo Speech Data Explorer](https://docs.nvidia.com/nemo/speech/latest/tools/speech_data_explorer.html); [entropy-based word confidence](https://developer.nvidia.com/blog/entropy-based-methods-for-word-level-asr-confidence-estimation)
  - Standards: [Web Audio API, AnalyserNode windowing](https://webaudio.github.io/web-audio-api/#fft-windowing-and-smoothing-over-time); [W3C Media Fragments URI 1.0](https://www.w3.org/TR/media-frags/)
