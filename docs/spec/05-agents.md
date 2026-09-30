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
7. Commit: after each turn the host commits worktree changes on the session branch with the turn id; the watcher emits `recipe.{path}` events so the Recipe document shows the diff live.
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
| Project and registry | `projects.get`, `projects.note`, `projects.sync`, `projects.adopt`, `aliases.set`, `registry.search`, `search.query`, `help.get`, `playbooks.run`, `experiments.new`, `experiments.get`, `sweeps.run` |
| Data | `sources.new`, `mounts.list`, `mounts.scan`, `pipelines.run`, `datasets.preview`, `datasets.freeze`, `datasets.materialize`, `datasets.evict`, `datasets.export`, `utterances.search`, `langpacks.get`, `langpacks.edit`, `boost.edit`, `boost.evaluate`, `augment.preview` |
| Training | `mixes.new`, `mixes.get`, `mixes.list`, `mixes.edit`, `mixes.preview`, `drafts.list`, `drafts.get`, `drafts.revert`, `runs.calibrate`, `runs.new`, `runs.resume`, `runs.stage`, `jobs.pause`, `jobs.resume`, `jobs.cancel`, `jobs.wait`, `metrics.get`, `checkpoints.list`, `checkpoints.average` |
| Evaluation | `goldenSets.list`, `goldenSets.freeze`, `evals.new`, `evals.get`, `evals.gate`, `gates.edit`, `baselines.set`, `augment.evaluate`, `batches.new`, `batches.get`, `batches.freeze` |
| Deployment | `models.register`, `models.export`, `models.parity`, `models.benchmark`, `deployments.promote`, `deployments.rollback` |
| Flywheel | `samples.query`, `signals.list`, `triage.next`, `triage.accept`, `triage.correct`, `triage.reject`, `corrections.package`, `schedules.new` |

Every mutating tool accepts `dryRun`; the verbs come from the vocabulary in the UI shell tab; the same names are the API operation ids and the UI commands.

### Context bridge

- From the UI: the current selection attaches to a prompt as references (`@run:123`, `@eval:45#he-IL/[56,1]`, `@utterance:9f3c`); Ask agent on any entity opens Chat with a prefilled prompt naming the entity and the intent.
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
| Tool call: Cadence MCP | Card naming the operation and the entity with a link; dry-run estimates inline |
| Tool call: file edit | Card with an inline diff; the Recipe document shows the same diff live |
| Tool call: shell | Card with command, exit code and collapsible output |
| Permission or approval request | Inline Allow once / Allow for session / Deny, mirrored in Approvals and on Telegram |
| Session state | Header chip: running, waiting approval, paused with the reason; budget meter for turns, tokens and GPU-hours |

### Worktree, drafts and merge

- Entity changes go through MCP and land directly, or as drafts with Accept and Revert on draftable kinds (mix, gate, note, language pack) when the project's policy says so. Accepting is a person's decision: `drafts.accept` is forbidden to agents; an agent may revert its own draft (phase 1: mixes; the policy per kind is the project's agent profile `draftPolicy`, set in Agent settings and filled by the wizard from `defaults.yaml` `agent.draft_policy`; a project without a profile falls back to `defaults.yaml` `drafts.*`).
- File changes commit on the session branch; the Recipe document lists open session branches and their diffs against `main`.
- On session end the branch is merged fast-forward when it applies cleanly and the permission preset allows auto-merge; otherwise it stays as "Session changes" with a three-way diff for the user to accept or discard. Branches are kept 30 days after merge.
- Parallel sessions never share a worktree; their conflicts appear only at merge, never at runtime.

### Playbooks and schedules as sessions

- A playbook session shows the chain's estimate before it starts, runs one step at a time with `dryRun` before each spending step, and reports each step's outcome as a plan tick; a failed gate ends the playbook with a summary and a next-step suggestion.
- A scheduled session uses the project's driver and model, has no person present, sends its approvals and its summary to Telegram, and never touches production; its budget is part of the schedule.

### Drivers

|  | Claude Code | opencode |
| --- | --- | --- |
| Launch | `claude-agent-acp` adapter | `opencode acp` |
| Authentication | The owner's Claude subscription, as the CLI, for every session kind (R6) | MiniMax through its Token Plan by default; other providers configured in the project's `opencode.json`, including self-hosted vLLM (R6) |
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
- The agent host speaks `hostSessions.claim|report|ask|decision` (tag `host`, R1) with its own credential (`cah_`, minted into `CADENCE_HOST_TOKEN_FILE` at start or by `cadence admin host-token`). A claim long-polls for sessions to start, messages, controls and permission decisions; everything it returns is taken once.
- The session token is minted when a host claims the session (and again, the old one revoked, when another host takes it over), not at create: it then exists only in the host's memory and the agent's `session/new`.
- Transcript entries are coalesced by the host: one entry per text block, one per tool call updated in place, one plan per turn, turn start/end with usage, commits. Permission entries are the server's (from `ask` and from gated commands through a pipeline hook), so ACP permission requests and gated commands share the transcript and the Approvals panel.
- An ACP permission request first goes to the preset (`policy.AnswerPermission`: tool classes, file rules, shell patterns, web); only an `ask` becomes an approval of kind `agent_permission`, answered back to the host (once → allow_once, for session → allow_always, deny or expiry → reject_once), never replayed.
- Ending: `agentSessions.cancel {"end": true}`; accept/revert need the session paused or ended (a paused one ends). A decided gated command is told to the agent as a notice (its next turn).
- Clocks: the stuck-turn clock and the runaway rule run in the host; the idle clock and the project's daily token budget on the server (a chore and each report).
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
| Freeze a golden set, change a baseline or gate | Approval request | User |
| Shadow deployment | Allowed | — |
| Canary, production, rollback | Approval request | User, through a confirm modal |
| Delete anything | Not allowed | User only, soft delete |
| Shell in the worktree | Sandboxed; network limited to Hugging Face, PyPI and NGC | — |
| Reviewer role: assigned review batches only | Triage queue, Diff and Audio for the batch; play, no download; no other panels or API scopes; invitation expires with the batch | Admin invites and closes batches |
| Sending call content to an external LLM judge | Only after PII redaction; transcripts and dialogue context only, never audio; daily spend budget | — |

- ACP permission requests and Cadence approval requests share one Approvals panel and the same inline cards in Chat.
- A policy engine answers requests the table already decides, so the user sees only real decisions.
- Budgets per project and per session: GPU-hours per day, agent spend per day, turn count; hitting a limit pauses the session.
- Secrets (Hugging Face, NGC) live in the control plane and are injected into jobs only; agent model credentials stay with each agent's own configuration.
- The audit log records every command with actor and `causedBy`, so any production change traces back to a person's approval.
- Approvals have a scope: project (runs over budget, deployments) or registry (mounts, golden-set freeze, model registration, secrets). The Approvals panel shows both; registry ones are tagged, and only the admin decides them.
- Cache fairness: each project has a cache quota on local NVMe; eviction is least-recently-used within the quota first, then across projects, never touching pinned versions.

## Security

Cadence does not build its own agent sandbox: Claude Code and opencode each ship a permission system, and Cadence configures those from the project's permission preset; Cadence adds only what the agents cannot know — token scope, secret isolation, and approvals for GPU spend and production.

- The permission preset renders into `.claude/settings.json` (allow, deny and ask rules, sandbox settings) and `opencode.json` (`permission` block); the agent enforces them, Cadence shows what was rendered in Agent settings.
- Tool results that carry content from data — transcripts, notes, help articles, search hits — are marked as data in the MCP response so the agent's own injection defences apply (every result is JSON with the content under `data` or `error` and a `note` saying it is data, not instructions); no Cadence tool ever executes an instruction found in data, and anything that could reach production sits behind an approval a person decides.
- Secrets never appear in an agent context: jobs receive them from the control plane at start; the MCP token is the only credential an agent holds.
- Container images are pinned by digest, dependencies by lockfiles; updates arrive as reviewed pull requests.
