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

- [ ] A1: ACP client against `opencode acp` and `claude-agent-acp`: chat, tool-call diffs, permission round trip, cancel, resume
- [ ] A2: Cadence MCP server with ten generated tools; both agents create a mix and launch a dry-run
- [ ] A3: Nemotron 3.5 fine-tune on the staging card under a 24 GB cap, then ONNX export and a parity check
- [ ] A4: Outbox → SSE → cache patching with an agent editing a mix while the Mix panel is open
- [ ] A5: live microphone → WebSocket relay → Nemotron checkpoint in the worker and back, beside a training job (before phase 3's live mode)
- [ ] S5: one audio view with a 30-minute call, spectrogram and word tracks at 60 fps across popouts (before phase 3's Audio panel)
- [ ] F1: k2/icefall through the framework seams without changes outside its pack (deferred with the packs beyond NeMo)

Open questions:

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
      the staging host is inventoried
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
    answers them without a person
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
