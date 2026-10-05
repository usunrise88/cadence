# Cadence spec — Agent integration, guardrails, security

_Part of the Cadence specification v0.2 (2026-09-29). Source of truth: the Claude Doc "Cadence — spec v0.2"; these files are its export, split by area. Figures referenced below live in the Doc._

## Agent integration

Cadence launches Claude Code or opencode inside a project worktree, hands it the Cadence MCP server and the project's skills, and treats the session as an entity: every turn is events, every change is a command, every file edit is a commit on a session branch — so the user watches, steers and audits the agent from the same panels they work in, and nothing the agent does is outside the loop every other entity follows.

### Session kinds

| Kind | Started by | Prompt | Ends |
| --- | --- | --- | --- |
| Interactive | The user, from Chat or Ask agent | The user's messages with the selection attached as references | The user closes it; inactivity pauses it |
| Playbook | A Project-home button or `playbooks.run` | The playbook's prefilled prompt plus the project facts; the chain's steps become the plan | When the chain's last step reports, or a budget or gate stops it |
| Scheduled | A Schedule | The playbook prompt with the schedule's parameters; no person present | Same as playbook; an approval pauses it and notifies Telegram |
| Read-only | Explain this, from any entity or help page | The help article and the entity | One turn; the session token carries no mutating verbs |

### Why ACP

[ACP](https://agentclientprotocol.com/protocol/overview) is JSON-RPC 2.0 between a client and an agent subprocess: `session/new`, `session/prompt`, streamed `session/update`, `session/cancel`, and client-side methods for permission requests and file access. opencode speaks it natively (`opencode acp`); Claude Code through the `claude-agent-acp` adapter on the Claude Agent SDK. One ACP client serves both, and a third agent is a driver, not an integration.

### Session lifecycle

1. Create: `POST /projects/{p}/agent-sessions` with kind, driver, model, prompt and references; the session entity is `created` and a scoped token is minted (see Authentication).
2. Workspace: a git worktree of the project's `main` on a branch `session/{id}`, holding `AGENTS.md` (rendered project facts), `CLAUDE.md`, `NOTES.md`, `data.lock`, `lang/`, `pipelines/`, `.claude/skills`, and the agent config files rendered from the agent profile with the MCP endpoint and the token.
3. Spawn: the driver starts the agent, runs `initialize`, then `session/new` with the worktree as working directory and the Cadence MCP server in the list; the session is `running`.
4. Turn: each message becomes `session/prompt`; references expand into a compact context block (kind, id, the header facts, a link); the host counts turns and tokens against the budget.
5. Tools: every MCP call is a command with actor = session, so it produces outbox events the open panels receive; results come back as data-marked JSON; long operations return a job id and the agent waits with `jobs.wait`.
6. Permissions: the agent's own rules apply first; a gated Cadence command returns `202` with an approval id and the session becomes `waiting_approval`, which notifies in-app and on Telegram; the decision resumes it.
7. Commit: after each turn the host commits worktree changes on the session branch with the turn id (commits the agent made itself are scanned for credentials with them — everything since the last push — and pushed as they are; one holding a credential is undone into the worktree, never pushed; `git add` and `git commit` are allowed in the shell for that reason, and the instructions tell the agent it need not run them); during the turn the watcher reports uncommitted files, which the control plane emits as `recipe.{path}` events (`recipe.working`), so the Recipe document and Session changes follow the agent's edits live and show the diff once the turn commits.
8. Pause and resume: inactivity, a budget limit or a runaway check pauses the session; resume restores the worktree and the ACP session where the driver supports it, otherwise a new ACP session starts with a transcript summary injected.
9. End: the user closes it or the playbook completes; the session is `done`, the token is revoked, and the session branch is merged (see below).
10. Failure: a driver crash marks the session `failed`, keeps the worktree and branch, and notifies.

| State | Meaning | Leaves by |
| --- | --- | --- |
| created | Entity exists, workspace not ready | spawn |
| running | Agent may be mid-turn or idle | approval request, pause, end, failure |
| waiting_approval | A gated command awaits a decision | approve or deny |
| paused | Inactivity, budget or runaway | resume, end |
| done, failed, cancelled | Terminal; transcript and branch remain | — |

### What the agent sees

- Files in the worktree: the project facts, notes, the data lockfile, language packs, pipelines, augmentation profile, skills.
- MCP resources: `project://summary` (locales, base model, aliases, budgets and today's use, open approvals; the connection's project, or `project://{p}/summary`), `selection://current` (the references the user attached), `help://{slug}` (`help://errors.not-found`), `defaults://`.
- The tool catalogue below; every tool description carries the parameter docs from the step schemas.
- Not: secrets, other projects' work (the registry is readable, their work is not), production hosts, raw audio bytes; audio-level checks run as scorer steps the agent can start.

### What the agent can do

| Block | Tools (`<entity>.<verb>`) |
| --- | --- |
| Project and registry | `projects.get`, `projects.note`, `projects.sync`, `projects.adopt`, `aliases.set`, `registry.search`, `registry.lineage`, `search.query`, `help.get`, `playbooks.run`, `experiments.new`, `experiments.get`, `experiments.list`, `sweeps.run` |
| Data | `sources.new`, `sources.list`, `sources.get`, `sources.edit` (approval: clearing for training), `mounts.list`, `mounts.get`, `mounts.scan`, `mounts.verify`, `mounts.new` (approval, the admin decides), `storage.get`, `pipelines.run`, `datasets.list`, `datasets.get`, `datasets.preview`, `datasets.freeze`, `datasets.materialize`, `datasets.evict`, `datasets.export` (Hub: approval), `exports.list`, `exports.get`, `texts.get` (a dataset card), `utterances.search`, `auxiliaries.list`, `auxiliaries.get` (adoption: approval), `triage.list`, `langpacks.get`, `langpacks.edit`, `boost.edit`, `augment.preview` (phase 5) |
| Training | `mixes.new`, `mixes.get`, `mixes.list`, `mixes.edit`, `mixes.preview`, `drafts.list`, `drafts.get`, `drafts.revert`, `runs.calibrate`, `runs.new`, `runs.resume`, `runs.stage`, `jobs.pause`, `jobs.resume`, `jobs.cancel`, `jobs.wait`, `metrics.get`, `checkpoints.list`, `checkpoints.average` |
| Evaluation | `goldenSets.list`, `goldenSets.get`, `goldenSets.freeze` (approval), `normalizers.list`, `normalizers.get`, `evals.new`, `evals.get`, `evals.list`, `evals.gate`, `gates.get`, `gates.edit` (approval), `models.register` (approval; needs a passed gate), `models.list`, `models.get`, `langpacks.list`, `langpacks.get`, `langpacks.edit`, `boost.edit` (drafts on a `langpack/<locale>-<date>` branch under the draft policy `language_pack`), `registry.lineage`, `aliases.set` (`baseline`, approval); boosting and robustness are `evals.new` axes (R1, R24); phase 4: `batches.new`, `batches.list`, `batches.get`, `batches.freeze` (approval), `batchItems.list`, `batchItems.get`, `guidelines.get` (the guidelines file at the batch's pinned commit) |
| Deployment | `models.export`, `models.parity`, `models.benchmark` (GPU spend under the usual policy; a benchmark takes the card alone), `deploymentTargets.list`, `deploymentTargets.get`, `deployments.new` (shadow on the staging target), `deployments.list`, `deployments.get`, `deployments.promote` (approval), `deployments.rollback` (approval), `promotions.list`, `promotions.get`, `shadowReplays.list`, `shadowReplays.get`, `shadowReplays.new` (GPU spend under the usual policy) (phase 5, specified 2026-10-05: 02 "Deployment entities"; built by streams D1–D4) |
| Flywheel | `samples.query`, `signals.list`, `triage.next`, `triage.accept`, `triage.correct`, `triage.reject`, `corrections.package`, `schedules.new` |

Every mutating tool accepts `dryRun`; the verbs come from the vocabulary in the UI shell tab; the same names are the API operation ids and the UI commands. Operations tagged `media` (utterance audio, peaks, spectrogram, words and, from phase 4, `tracks.get`; `transcriptions.new` and its socket) are never tools: agents read no raw audio and test models through evals (R47, R48).

Phase 4 as built (2026-10-04; `control-plane/templates/presets/guardrails-default.yaml`): the new tools above are generated
from the contract like every other. Some exist as tools but the preset keeps agents out of them, because a human
transcript must be a person's and reviewers are the admin's: `annotations.new`, `batchItems.accept`,
`triage.accept|correct|reject` and `invitations.*` (rule `annotation-is-for-people`, forbidden; `invitations.*` is
also admin-only in the handler). `versions.archive` falls under `no-deletes`, and `auth.accept` (tag `auth`) is not a
tool. An agent opens and watches batches, runs ingest and pseudo-label pipelines, previews, freezes, materialises,
evicts and exports to a mount on its own (class `draft`, reversible); what leaves the instance or is shared by every
project waits for the admin (Guardrails below).

### Context bridge

- From the UI: the current selection attaches to a prompt as references (`@run:123`, `@eval:<id>#cell:<evc_id>` or `…#cell:<evc_id>/utt:<index>` from the Eval report, `@utterance:9f3c`); Ask agent on any entity opens Chat with a prefilled prompt naming the entity and the intent.
- From the agent: references in replies render as links that open the document; an attribution badge on any entity the agent changed jumps to the tool call in Chat.
- Between sessions: `projects.note` writes to `NOTES.md`, which the next session reads through `AGENTS.md`; the transcript of any session is searchable.

### Budgets and runaway protection

- Turns and tokens are counted by the agent host per session; GPU-hours are checked by the control plane at `dryRun` and at approval; all three are set per project with session overrides.
- A runaway check pauses the session when the same tool is called with the same arguments three times in a row, when a turn exceeds the token limit, or when the inactivity timeout passes; the pause is an event with the reason, and a note is written for the next session.
- Hitting a budget never cancels running jobs; it stops new spend.

### What the Chat panel shows

| ACP update | Rendered as |
| --- | --- |
| Agent message chunk | Streaming Markdown; entity references as links |
| Thought chunk | Collapsed "thinking" block |
| Plan | Checklist that ticks as the agent works; a playbook's chain is the initial plan |
| Tool call: Cadence MCP | One quiet line naming the operation (draft, dry run, a failed status) that opens into a card with the entity link and the dry-run estimate |
| Tool call: file edit | One line with its +/− counts that opens into an inline diff; the Recipe document shows the same diff live |
| Tool call: shell | One line with the command and a failed exit code that opens into its output |
| Permission or approval request | Inline Allow once / Allow for session / Deny, mirrored in Approvals and on Telegram |
| Session state | Header chip: running, waiting approval, paused with the reason; budget meter for turns, tokens and GPU-hours |
| Header | One line: the session, its state (the reason of a pause as its tooltip), the model, and Stop, Pause or Resume, End as icons in the corner; the budget meters below |
| Tab | The chat icon filled with the session's status colour, the session the short way (`CC · S4` for Claude Code session 4, `OC` for opencode) and a dot while a finished turn or a session wanting attention (approval, pause, failure, end) has not been seen |

Every tool call starts collapsed; the attribution badge's jump opens the one it lands on. Keys typed anywhere in the Chat outside a field go to the composer.

### Worktree, drafts and merge

- Entity changes go through MCP and land directly, or as drafts with Accept and Revert on draftable kinds (mix, gate, note, language pack) when the project's policy says so. Accepting is a person's decision: `drafts.accept` is forbidden to agents; an agent may revert its own draft (phase 1: mixes; the policy per kind is the project's agent profile `draftPolicy`, set in Agent settings and filled by the wizard from `defaults.yaml` `agent.draft_policy`; a project without a profile falls back to `defaults.yaml` `drafts.*`).
- File changes commit on the session branch; the Recipe document lists open session branches and their diffs against `main`.
- On session end the branch is merged fast-forward when it applies cleanly and the permission preset allows auto-merge; otherwise it stays as "Session changes" with a three-way diff for the user to accept or discard. Branches are kept 30 days after merge.
- As built (audit 2026-10-02): a branch whose diff touches `gates.yaml`, `lang/`, `project.yaml`, `data.lock` (audit F1, 2026-10-04), `.claude/` or `opencode.json` is never auto-merged, whatever the profile's `autoMerge` (`sessions.GuardedPaths`): it stays `pending` with a `branch.waiting` reason naming the files, until a person accepts it — the guardrail "an agent's edit of `gates.yaml` reaches `main` only when a person accepts the session changes" (Guardrails table), extended to the language packs and the project and agent settings.
- Parallel sessions never share a worktree; their conflicts appear only at merge, never at runtime.

### Playbooks and schedules as sessions

- A playbook session shows the chain's estimate before it starts, runs one step at a time with `dryRun` before each spending step, and reports each step's outcome as a plan tick; a failed gate ends the playbook with a summary and a next-step suggestion.
- Mechanics (R16, phase 2): `playbooks.run` takes the playbook's inputs, answers the estimate first (the sum of the chain's step estimates, `basis` and ±) and starts an agent session of kind `playbook` with the project's driver and model. Its prompt is the template's `prompt` rendered with the inputs and the project facts; its initial plan is the chain, one entry per step (steps of a phase not yet shipped are listed as skipped). The agent works the chain through ordinary MCP tools — the same commands a person would issue — so every step is attributed, gated and budgeted as usual; a spend over the session's or project's GPU budget returns an approval id and the session waits.
- The session ends when the last step reports, or at a stop condition from the template (failed gate, exhausted budget, denied approval, a step failed after its retries), with a summary and a next-step suggestion in the transcript.
- "Fine-tune from a dataset version" (03 "Playbooks") is the phase-2 gate: mix with replay → `runs.calibrate` → `runs.new` (dry run shown first) → checkpoints registered with validation WER → from phase 3 the eval matrix and the gate.
- As built (phase 2, stream K): the session's playbook lives on the session entity (`AgentSession.playbook`: name, template version, resolved inputs, the estimate, the plan with each step `pending|running|done|failed|skipped`, `dryRuns`, `stop`, `summary`, `next`) and travels with `agent_session.changed`. The transcript opens with the estimate notice, then the rendered prompt. The server ticks the plan, never the agent: the command pipeline's session hook ticks the current step from a command of the session that succeeded (in the command's transaction; a dry run of a spending step marks it running), and reads a chain waits on (`jobs.wait`, `checkpoints.list`) are observed after they answer; only the current step ticks, from an operation it names; a terminal step waits for the entity the step before it started — `runs.get` answering the run with an ended status ticks it (failed or cancelled fails it); `jobs.wait` on one of the run's jobs only marks it running. A real spending command (`runs.new|calibrate|resume|stage`, `checkpoints.average`) needs a successful dry run of the same request (path, query, If-Match and canonical body; not the Idempotency-Key) as the session's last dry run of that operation since its last real one, else `409 playbook-dry-run-required`; once the playbook ended every spending command answers `playbook-stopped`. A failed job (step failed), a denied approval or a pause on the agent budget stops the playbook with a summary notice and the next step; the session ends when its turn ends (at once when none runs). A turn that ends without progress gets a reminder of the next step for the agent, at most twice in a row. Playbook sessions have a branch like interactive ones, are not put to sleep by the idle clock, and `jobs.wait` does not count toward the runaway rule (any session). The agent host prepends the plan and the rules to the first prompt of each agent session (both drivers, no driver change).
- A scheduled session uses the project's driver and model, has no person present, sends its approvals and its summary to Telegram, and never touches production; its budget is part of the schedule.

### Drivers

|  | Claude Code | opencode |
| --- | --- | --- |
| Launch | `claude-agent-acp` adapter | `opencode acp` |
| Authentication | The owner's Claude subscription for every session kind: a `claude setup-token` token pasted in Settings → Agents (or `agent-host login claude`), kept as `claude/oauth-token` in the agent-credentials volume and passed as `CLAUDE_CODE_OAUTH_TOKEN` (R6) | Providers from Settings → Agents (MiniMax Token Plan by default, `minimax/MiniMax-M3`; Anthropic, OpenAI, OpenRouter, DeepSeek; an OpenAI-compatible base URL such as self-hosted vLLM), kept as `opencode/auth.json` plus a provider block in `opencode/opencode.json` for custom base URLs (R6) |
| Verify (Settings → Agents) | `claude -p "Reply OK" --model haiku --output-format json` as a sandboxed session user through the egress proxy | `opencode models <provider>` (a custom provider's `/models` is probed first), then `opencode run` with the provider's cheap model; the model list is recorded |
| Permissions | `.claude/settings.json` rendered from the preset | `permission` block rendered from the preset |
| Skills | `.claude/skills` | `.claude/skills` (Claude-compatible path, [docs](https://opencode.ai/docs/skills)) |
| Resume | Where the adapter supports it; otherwise summary injection | Native session resume |
| Native escape hatch | [Claude Agent SDK](https://code.claude.com/docs/en/agent-sdk/typescript): `canUseTool`, hooks, in-process MCP tools, `interrupt()`, `rewindFiles()`, subagents | [opencode server](https://opencode.ai/docs/server/): OpenAPI HTTP API with SSE events, session fork, revert, diff |

The driver interface is ACP-shaped; a native driver translates its own events into the same stream and is used only for a named feature ACP cannot carry.

### Skills

Cadence ships `cadence-data`, `cadence-train`, `cadence-eval`, `cadence-deploy`, `cadence-flywheel` and `cadence-spikes`, plus NVIDIA's `nemo-speech-asr-finetune`. Each skill has the same shape: when to load it, the loop for its block, the tools in order, the gates, what to record with `projects.note`, and links into `docs/help`. Skills are tested nightly by the agent evals on a fixture project with both drivers.

### Failure modes

| Failure | What happens |
| --- | --- |
| Driver crash | Session `failed`, worktree and branch kept, Telegram notified; resume creates a new session with the summary |
| Tool error | The problem+json `type` points at a help article; the agent reads it before retrying; three identical retries trigger the runaway pause |
| Merge conflict | Session changes stay as a draft with a three-way diff |
| Approval never answered | Session stays `waiting_approval` until the inactivity timeout, then `paused`; the request stays open in Approvals |

### Entities and contract

Agent session: kind, driver, model, project, prompt, references, state, branch, merge state, budget and use, transcript (events), started by. Agent profile: driver, model, permission preset, config file references. Operations (R1): `agentSessions.new|list|get` (`/projects/{p}/agent-sessions`, `/agent-sessions/{id}`), `agentSessions.cancel|pause|resume|accept|revert` (`/agent-sessions/{id}:<verb>`; accept/revert merge or discard the branch as a whole), `agentMessages.new|list` (`/agent-sessions/{id}/agent-messages`, the transcript paged by `seq`); topics `agent.session.{id}` (transcript entries `agent_message.created|updated` and the session header `agent_session.changed`), `agent.sessions` (`agent_session.created|changed`), `recipe.{path}`, `approvals`.

Phase 1 as built (2026-09-30):
- The agent host speaks `hostSessions.claim|report|ask|decision|release` (tag `host`, R1) with its own credential (`cah_`, minted into `CADENCE_HOST_TOKEN_FILE` at start or by `cadence admin host-token`). A claim long-polls for sessions to start, messages, controls and permission decisions; everything it returns is taken once.
- Agent credentials (2026-09-30): the host also long-polls `hostCredentials.claim` for tasks from Settings → Agents — write a value into the agent-credentials volume in the agent's own format, remove one, or verify one (a tiny real request through the agent, run as a session user from the uid pool through the egress proxy) — and acknowledges each with `hostCredentials.report` (outcome, detail without the value, opencode's model list). Only the drivers know the formats and the checks (`writeCredential`, `removeCredential`, `verify`); values are never logged or reported.
- The session token is minted when a host claims the session (and again, the old one revoked, when another host takes it over), not at create: it then exists only in the host's memory and the agent's `session/new`.
- Transcript entries are coalesced by the host: one entry per text block, one per tool call updated in place, one plan per turn, turn start/end with usage, commits. Permission entries are the server's (from `ask` and from gated commands through a pipeline hook), so ACP permission requests and gated commands share the transcript and the Approvals panel.
- An ACP permission request first goes to the preset (`policy.AnswerPermission`: tool classes, file rules, shell patterns, web); only an `ask` becomes an approval of kind `agent_permission`, answered back to the host (once → allow_once, for session → allow_always, deny or expiry → reject_once), never replayed.
- Pre-allowed Cadence tools (2026-09-30 polish): Claude Code loads the ask and deny rules of the worktree's `.claude/settings.json` but not its allow rules (checked with the SDK's `list_permission_rules`: only the project's ask and deny rules are in effect), so every Cadence call raised a permission request the preset then answered. The control plane now sends the tools the preset allows agents (`policy.AgentAllowed`, the same set the file allows) in `HostStart.allowedTools`; the Claude driver passes them as `allowedTools` in `_meta.claudeCode.options` (a CLI-argument rule; the file's deny rules still win). opencode applies its project `opencode.json` permissions and needs nothing (A2: no MCP permission requests). The Chat leaves out a permission entry the preset allowed on its own (a rule, no approval) for a tool call the transcript shows.
- Ending: `agentSessions.cancel {"end": true}`; accept/revert need the session paused or ended (a paused one ends). A decided gated command is told to the agent as a notice (its next turn).
- Clocks: the stuck-turn clock and the runaway rule run in the host; the idle clock and the project's daily token budget on the server (a chore and each report).
- Host restarts (phase 1 punch list): on SIGTERM/SIGINT the host stops its agents, lets an interrupted turn commit and report, and calls `hostSessions.release` with the messages no turn took yet, so the next host claims the sessions at once instead of after the 90 s lapse. The release revokes the session tokens, withdraws pending agent-permission requests as interrupted (not declined), gives the messages back and keeps a note that the next claim hands over (`HostStart.resume.note`) and the agent reads before its next prompt; a Stop queued meanwhile is dropped at the takeover, pause and end wait until the session runs on the new host. `AgentSession.hostState` (`waiting`, `connected`, `released`, `lost`) and `hostLeftAt` let the Chat say "The agent host is restarting — reconnecting…"; the sweep marks a host silent past the lapse as `lost` until it answers again or another host takes over.
- Cancelled permission requests: ACP answers a request left open by a cancelled turn with `cancelled`, which agents report as "you declined". The host records why the turn ended (the person's Stop, a pause, the end, Cadence's clocks) and writes a transcript notice ("… was cancelled because the person stopped your turn (Stop) — nobody declined it"); the agent reads the same reason as a `[Cadence]` line before its next prompt. A request rejected because the control plane could not be asked is explained the same way.
- Tool-call attribution for opencode: opencode's MCP calls carry no tool-use id, so their commands get the MCP server's synthetic id (`mcp:<session>/<rpc id>`). When the host reports a completed Cadence tool call with the agent's own id, the oldest command of the same operation by the same session within 2 minutes that still has a synthetic id takes it — audit log, outbox events, permission entries, drafts and mix revision causes — and `draft.updated`, `presence.changed` and `mix.attributed` refresh open panels, so the attribution badge jumps to the tool call as it does for Claude.
- Worktree watcher and Session changes: while a turn runs the host watches the worktree (`fs.watch`, never `.git`;
  polling every 2 s where it cannot), coalesces events for 300 ms, lists the uncommitted files with `git status`
  (git's ignore rules, no optional locks) and reports a changed list as `hostSessions.report` `working` (paths,
  status, sizes and line counts; ≤ 200 files; no content); after the turn's commit it reports what is left. The
  session keeps it as `AgentSession.working`, and every file entering, changing in or leaving it is a `recipe.{path}`
  event of type `recipe.working` on the session branch (by the agent); the end clears it. Chat's Session changes lists
  the files live ("editing"), the Recipe document of such a file says which session is editing it. `branches.compare`
  compares a session or sync branch with main per file (merge base, main, branch; clean or not by git's merge; for
  conflicting files the three texts, 128 KiB each and 1 MiB per response, with diff3 hunks); Chat's Session changes
  and the Recipe branch view open conflicting files in a three-way view (side by side, stacked when narrow, or
  unified; next/previous conflict from the keyboard), clean files keep the two-way diff. Conflicts are resolved on
  the branch; the view is read-only.
- Asleep (2026-09-30 polish): an interactive session paused by the idle clock (`pauseReason.code = idle`) is shown as "asleep" — grey, no warning, no notification, no unread dot — and `agentMessages.new` wakes it (queues a resume, the message is delivered after it; an idle pause the host has not taken yet is withdrawn). Every other pause holds messages: `agentMessages.new` answers `409 conflict` with the reason and the Chat's composer says why with a Resume button.
- Chat and the context bridge (web): the workspace's Chat is pinned to a session (`panels.chat.pinnedTo`), further sessions open their own Chat; the transcript is `agentMessages.list` paged by `after` and patched in place from `agent.session.{id}` (one cache write per animation frame, windowed past 200 entries); permission entries embed the Approvals card; Session changes show the branch diff and `agentSessions.accept|revert`. Ctrl/Cmd+I and Ask agent attach the selection as `@<kind>:<id>[#part]` chips with a prefilled intent; Explain this starts a read-only session with the entity and its help article; references in replies are links; an attribution badge opens the session's Chat at the tool call (`Cadence-Tool-Call-Id` = the transcript's tool-call id). The finished turn is announced in the polite live region, streamed tokens never.

## Guardrails

Agents may do anything reversible on their own; anything that spends real GPU time beyond budget, touches production or deletes data waits for a person.

| Action class | Agent default | Who approves |
| --- | --- | --- |
| Read any entity, log, metric or result | Allowed | — |
| Edit recipes and drafts in its own worktree | Allowed | — (reversible through git and draft revisions) |
| Create sources, dataset versions, mixes, eval runs | Allowed | — |
| GPU job within the session budget, staging card | Allowed, notified | — |
| GPU job over budget | Approval request | User |
| Freeze a golden set, change a baseline or gate | Approval request; an agent's edit of `gates.yaml` in its worktree reaches `main` only when a person accepts the session changes | User (golden-set freeze: the admin, registry scope, and a person's freeze waits for the same approval — preset rule `golden-set-freeze`, `everyone: true`; phase 3) |
| Register a mount | Approval request, for people too (rule `mount-registration`, `everyone: true`; phase 4) | Admin (registry scope) |
| Adopt an auxiliary model (LID, pseudo-label member, aligner) | Approval request, for people too (rule `auxiliary-adoption`: `projects.adopt` of kind `auxiliary`); a version whose outputs forbid commercial use is refused before any approval (`auxiliary-licence-refused`) | Admin (registry scope), after reading its licence |
| Push a dataset to the Hugging Face Hub | Approval request, for people too (rule `hub-export`: `datasets.export` with `format: hf-hub`; private by default); production sources, unlicensed sources and golden-set data are refused (`export-not-allowed`); exports to a mount or the content store are allowed | Admin (registry scope) |
| Freeze an annotation batch | Approval request, for people too (rule `annotation-batch-freeze`); annotating, adjudicating, resolving triage and inviting reviewers are not allowed (`annotation-is-for-people`) | Admin (registry scope) |
| Shadow deployment | Allowed | — |
| Export, parity, benchmark | Allowed within the GPU budget (rule `gpu-spend` as any job) | User when over budget |
| Canary, production, rollback | Approval request, for people too (rule `deployments`, `everyone: true`; stream D4); the checks run first, so a promotion that would fail parity, latency, shadow volume, target support or the engine's card class and server release is refused before anyone is asked | User, through a confirm modal (a person's own request is approved there at once); the approver is named in the signed Promotion record (R33) |
| Confirm a delivery (`promotions.verify`) | Not allowed (rule `delivery-is-for-people`): the receipt comes from a production host no agent reaches | The person who ran the delivery script |
| Create or change a deployment target | Approval request, for people too (rule `deployment-targets`, `everyone: true`, as mounts): a target names production | Admin (registry scope) |
| Delete anything | Not allowed | User only, soft delete |
| Shell in the worktree | Sandboxed; network limited to Hugging Face, PyPI and NGC | — |
| Reviewer role: assigned review batches only | Triage queue, Diff and Audio for the batch; play, no download; no other panels or API scopes; invitation expires with the batch | Admin invites and closes batches |
| Sending call content to an external LLM judge | Only after PII redaction; transcripts and dialogue context only, never audio; daily spend budget | — |

- ACP permission requests and Cadence approval requests share one Approvals panel and the same inline cards in Chat.
- A policy engine answers requests the table already decides, so the user sees only real decisions.
- Budgets per project and per session: GPU-hours per day, agent spend per day, turn count; hitting a limit pauses the session.
- Retries inherit the run's approval (owner decision 2026-10-04): a `gpu-spend` command that continues a pipeline run
  — `pipelineRuns.retry`, `jobs.resume`, `runs.resume` — and that the policy would send to a person runs under the
  approval the run was started (or last continued) under, when that approval was decided the same UTC day and, if its
  estimate was known, the GPU-hours the runs under it used on cards since plus the retry's estimate stay within the
  approved GPU-hours (an unknown retry estimate against a known approval asks). An approval of an unknown estimate
  covers every retry that day, a batch scale included. Any actor inherits, agents and people alike; the decision's
  rule is `inherited-approval` and the audit row names the approval. Otherwise the retry asks as before, and an
  approved retry becomes the run's approval (`pipeline_runs.approval_id`, migration 0044).
- Secrets (Hugging Face, NGC) live in the control plane and are injected into jobs only; agent model credentials stay with each agent's own configuration in the agent-credentials volume — set from Settings → Agents they pass through the secret store's transit area to the agent host and are deleted there once written (2026-09-30).
- The audit log records every command with actor and `causedBy`, so any production change traces back to a person's approval.
- Approvals have a scope: project (runs over budget, deployments) or registry (mounts, golden-set freeze, model registration, secrets; from phase 4 auxiliary adoption, Hub export and annotation-batch freeze; `versions.archive` is the admin's own call, no approval). The Approvals panel shows both; registry ones are tagged, and only the admin decides them. A registry-scope approval (no project, or a rule marked `everyone`) is granted once: `grant: session` answers `validation-failed`, and a stored session grant never answers such a request (they share one path between requests of any body; audit F1, 2026-10-04).
- Cache fairness: each project has a cache quota on local NVMe; eviction is least-recently-used within the quota first, then across projects, never touching pinned versions. As built (phase 4): `storage.project_quota_gb` (200) counts the cached bytes of the dataset versions a project froze (`datasets.freeze` refuses past it, `storage-quota-exceeded`); the periodic sweep (`storage.cache_sweep_minutes`) evicts unpinned shards that also live on a mount, least recently used and over-quota projects first, from `storage.cache_high_water_pct` (85) down to `cache_low_water_pct` (70), as the system actor without an approval (the owner set the policy); a blob on no mount is never evicted.

## Security

Cadence does not build its own agent sandbox: Claude Code and opencode each ship a permission system, and Cadence configures those from the project's permission preset; Cadence adds only what the agents cannot know — token scope, secret isolation, and approvals for GPU spend and production.

- The permission preset renders into `.claude/settings.json` (allow, deny and ask rules, sandbox settings) and `opencode.json` (`permission` block); the agent enforces them, Cadence shows what was rendered in Agent settings.
- Tool results that carry content from data — transcripts, notes, help articles, search hits — are marked as data in the MCP response so the agent's own injection defences apply (every result is JSON with the content under `data` or `error` and a `note` saying it is data, not instructions); no Cadence tool ever executes an instruction found in data, and anything that could reach production sits behind an approval a person decides.
- Secrets never appear in an agent context: jobs receive them from the control plane at start; the MCP token is the only credential an agent holds.
- Container images are pinned by digest, dependencies by lockfiles; updates arrive as reviewed pull requests.
