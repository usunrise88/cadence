---
title: Settings
summary: Instance-wide configuration for the admin — compute, the agents' model accounts, secrets, credentials, policies, notifications and the Telegram bot, backups, catalogues, security and the audit log.
contexts: [panel:settings]
---

## What this is

A tool panel for the admin account only (it opens floating; View → Open Settings, or the user menu). Sections:

| Section | What it holds |
| --- | --- |
| Compute | Hosts and their cards: memory, memory cap per card, allowed job kinds (training, eval, shadow, export, data), availability windows per job kind (R19: days, start, end, time zone; a job starts only if its estimate fits before the window closes, and a running training job pauses at the close and resumes in the next window — edited per card under Edit: add a window, pick the job kind, days, opening and closing time (HH:MM, 24:00 for midnight; a close before the open means the next day) and time zone; Queue & GPU shows them), the last telemetry a worker reported, health (from worker heartbeats) |
| Agents | The agents' own model accounts, for the whole instance: the Claude Code subscription token and opencode's providers (MiniMax, Anthropic, OpenAI, OpenRouter, DeepSeek, or an OpenAI-compatible base URL such as a self-hosted vLLM); status, expected expiry, Verify, and the default opencode model of new projects. Values are write-only |
| Secrets | Named credentials (Hugging Face, NGC, GitHub, S3, judge API): name, kind, scope, who added it, last use. The value is write-only |
| Credentials | API keys (`cdk_…`) scoped to one project and/or registry read — and, opt-in, **May run agent sessions in the project** (automation such as the agent evals; everything else stays under the default preset); your browser sessions; agent session tokens |
| Policies | Default budgets: GPU-hours per project per day, agent turns per session; the instance timezone (quiet hours, the digest and the backup schedule follow it) |
| Notifications | The routing table (per event class: in-app, Telegram, Telegram timing), quiet hours, the digest time, and the Telegram bot: write-only token, allow-listed chats, chats that wrote to the bot, test message — see the [notifications guide](../guides/notifications.md) |
| Backups | The schedule and retention, **Back up now**, the last restore test's report (row counts at backup vs restored, migration version, blobs re-hashed) and every set with its own **Restore test** — see the [backups guide](../guides/backups.md) |
| Catalogues | Read-only: base models, instruction templates and permission presets in the registry |
| Security | Two-factor sign-in (TOTP) for the admin account: **Set up** shows a QR code to scan with an authenticator app from the screen (drawn in the browser; the key never leaves the page), or the key in groups of four to type; confirm with the app's current code |
| Audit log | Every command, denial and failed attempt with actor, outcome, rule and cause; filter by actor, operation and project. Workspace layout saves are preferences, not commands: they are not listed (a refused save still is) |

## Place in the loop

Prepare. Settings sets the limits every project works inside; nothing here spends GPU time.

## Fields and defaults

Every defaulted field shows its default and a **Why this default?** popover with the description, the safe range
and the source, rendered from `defaults.yaml` (`defaults.get`). A value outside the safe range is a warning; the
server refuses values outside the ranges it enforces.

| Field | Default | Source |
| --- | --- | --- |
| Memory cap (staging card) | 24 GB of 48 GB | docs/review/2026-09-30-phase-2-plan.md (RTX PRO 5000 Blackwell 48 GB beside a resident vLLM service: memory fraction 0.5) |
| GPU-hours per project per day | 8 GPU-h (0–192) | Cadence recommendation |
| Agent turns per session | 200 turns (1–2000) | Cadence recommendation |
| Instance timezone | `UTC` | Cadence recommendation |
| Digest time | `09:00` | docs/spec/06-platform.md "Notifications" |
| Quiet hours | off (22:00–08:00 when on) | Cadence recommendation |
| Nightly backup / restore test | 03:00 / Sundays 04:00 | docs/spec/06-platform.md "Operations" |
| Backup sets kept | 7 nightly + 4 weekly | Cadence recommendation |

Policies show **departures from defaults** as chips; **Reset to recommended** puts every budget back to
defaults.yaml.

### Agents

| Field | What it shows or takes |
| --- | --- |
| Claude Code token | Write-only: the `sk-ant-oat…` token `claude setup-token` prints (run it once on any machine with a browser). The row shows connected or not, the last four characters, when and by whom it was set |
| Expected expiry | Set + 365 days (the token lasts about a year); a warning from 30 days before |
| Delivery | `pending` until the agent host has written the value into the agent-credentials volume — **waiting for the agent host** while none is connected — then `written`; `removing`/`removed` after Disconnect or Remove; `failed` with the host's reason |
| Last verification | `ok` or `failed` with the agent's answer or error, the model it ran with, and when |
| opencode provider | From the catalogue; the key is write-only (optional for an OpenAI-compatible base URL). A custom provider also takes an id (`vllm`), a display name and the base URL (`http://vllm.lan:8000/v1`) |
| Models | opencode: the provider's models as `provider/model`, from the last Verify |
| Default model for new projects | opencode: one of the verified models; **Why this default?** shows where it comes from. Unset, new projects use `defaults.yaml` `wizard.opencode_model` (`minimax/MiniMax-M3`) |
| API hosts | What the provider adds to the egress allowlist while it is configured |

Nothing of a value comes back: not here, not in the API, not in events or the audit log, never to an agent. Cadence
keeps it encrypted only until the agent host has written it into the agent-credentials volume.

Secret values and API key tokens are never shown again: a secret's value leaves the page with the request; an API
key's token appears once, right after creation, with a Copy button — Cadence keeps only its hash.

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Edit compute | `compute.edit` | If-Match on the host's revision; a change made meanwhile answers `412` and the form offers "Discard mine" or "Apply mine" on the new revision |
| Save token / Add provider / Replace key | `agentCredentials.set` | The value is write-only; If-Match once the credential exists; the agent host writes it (`hostCredentials.claim|report`) |
| Verify | `agentCredentials.verify` | The agent host runs a tiny real request through the agent (Claude: `claude -p` on haiku; opencode: `opencode models <provider>`, then `opencode run`); the result arrives live |
| Default model | `agentCredentials.set` with `defaultModel` | opencode only; on the credential whose provider the model belongs to |
| Disconnect / Remove | `agentCredentials.archive` | Inline confirm; the agent host deletes the files from the volume and the provider's hosts leave the egress allowlist |
| Add secret | `secrets.new` | The value is write-only |
| Create API key | `credentials.new` | Scope: one project, registry read, or both; optional expiry |
| Revoke credential | `credentials.revoke` | Inline confirm; irreversible. Your current session cannot be revoked here — sign out instead |
| Edit policies | `policies.edit` | If-Match on the policies' revision |
| Edit notification rule | `notificationRules.edit` | If-Match on the rule's revision |
| Save quiet hours, digest time, chats | `notificationSettings.edit` | If-Match on the settings' revision |
| Store / Replace bot token | `telegramBot.set` | Write-only (secret `telegram-bot-token`); If-Match once a token is stored |
| Send test message | `telegramBot.verify` | Checks the token with Telegram, then messages every allowed chat |
| Back up now | `backups.new` | Answers the job; one set at a time |
| Restore test | `backups.verify` | Restores the set into a scratch database and checks it; answers the job |
| Two-factor authentication… | `totp.enroll`, `totp.confirm`, `totp.disable` | The same dialog as the user menu |

Agents cannot run any of these: the `admin-only` rule of every preset forbids secrets, credentials, compute,
policies, notification and backup changes. Agent credentials are not even MCP tools (tag `agentCredentials`), and the presets forbid them too.

## Playbooks

- Another service moved onto the staging card: lower its memory cap, keep `training` allowed.
- A script needs to read results: create an API key scoped to that project, copy the token into the script's secret
  store, revoke the key when the script retires.
- Connect the agents: follow the [agent credentials guide](../guides/agent-credentials.md) — paste the Claude token,
  add MiniMax, Verify both, keep `minimax/MiniMax-M3` as the default model.
- Verify says 401: the token or key is wrong or expired; set a new one and Verify again.
- Something changed and nobody remembers why: filter the audit log by operation; the cause column links approvals
  and agent tool calls. An approval decided from Telegram shows the admin with channel `telegram`.
- Approve from the phone: set up the Telegram bot ([notifications guide](../guides/notifications.md)).
- Before an upgrade: check the last restore test, then follow [Upgrading Cadence](../guides/upgrading.md).

## Sources

- docs/spec/11-ui-panels.md "Panel catalogue" (Settings) and "First run and progressive disclosure"; docs/spec/06-platform.md "Authentication and access", "Operations", "Notifications"; docs/spec/08-resolutions.md R3, R4, R6, R9, R11.
