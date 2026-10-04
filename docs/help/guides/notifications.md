---
title: Notifications and the Telegram bot
summary: Settings → Notifications — the routing table by event class (channels, timing, silent), quiet hours, the 09:00 digest, and a Telegram bot that sends failures, gathers approvals into one message you can approve or deny from, and answers /status and /approvals.
contexts: [guide:notifications, panel:settings]
---

## What this is

Cadence tells you about events on two channels: the **in-app history** (the bell in the status bar, built from the
live event stream) and a **Telegram bot**. One routing table decides, per event class, which channels an event
reaches and when. Approval requests arrive on Telegram with **Approve** and **Deny** buttons; a press decides the
approval as the admin, recorded with channel `telegram` in the approval and the audit log, exactly like a decision in
the Approvals panel.

| Event class | Covers | In-app | Telegram | Timing (seeded) | Sound (seeded) |
| --- | --- | --- | --- | --- | --- |
| Approval requested | agent, automation and registry approvals | yes | one message per burst (see below) with the estimate and Approve / Deny per approval | as it happens, gathered for 2 minutes | rings |
| Failure | something ended failed: a job, a pipeline run or training run (one message per run, naming the step that failed), an eval, an agent session; compute host unreachable, mount unhealthy, backup or restore test failed, content store low on space (below `cache.store_low_free`, at most once a day) | yes | yes, even in quiet hours | as it happens | rings |
| Outcome | gate verdict (`evals.gate`, passed or failed), sweep ended, promotion, schedule finished, batch closed, a branch waiting for review (a template sync, or an agent session that ended without merging) | yes | yes | as it happens | silent |
| Progress | job done, pipeline step done or failed (a step's failure costs nothing by itself: an optional step's run goes on, an out-of-memory retry runs again, and a failure that stops the run is told once as the run's failure), eval done, golden set frozen, checkpoint saved, triage item added, backup taken; never the steps of an eval (the eval tells its end) | yes | no | — | — |
| Daily digest | runs and evals (their jobs), the day's gate verdicts, GPU spend against budgets, open approvals, branches waiting for review, events held for it, the last backup | yes | yes | 09:00 local | silent |

## Place in the loop

Operate. Set it up once after the first start; the defaults follow the table above.

## Fields and defaults

| Field | Default | Source |
| --- | --- | --- |
| Instance timezone (Policies) | `UTC` | `defaults.yaml` `operations.timezone`; Cadence recommendation |
| Digest time | `09:00` | `notifications.digest_time`; docs/spec/06-platform.md "Notifications" |
| Quiet hours | off; `22:00`–`08:00` when switched on | `notifications.quiet_hours_*`; Cadence recommendation (they end before the digest) |
| Telegram timing per class | as in the table | migration 0015 seeds the spec's table |
| Silent per class | outcome and digest silent; approvals and failures ring | migration 0043; owner feedback 2026-10-04 |
| Approval batching window | `120` s (0–900; 0 sends each at once) | `notifications.approval_batch_s` |

- **Telegram timing**: *As it happens*, *In the daily digest* (held and listed in the next digest), or *Never*. The
  digest row is *Daily* or *Never*. The in-app history is always immediate; its checkbox only switches a class off.
  Progress never interrupts: on Telegram it can only be held for the digest. A changed row shows **changed**.
- **Silent**: the class's Telegram messages arrive without sound or vibration (Telegram's `disable_notification`);
  they still show in the chat and the unread count. Only meaningful for a class that sends messages (Telegram on,
  timing not *Never*; never progress). A changed checkbox shows **changed** too.
- **Approvals close together share one message.** The first approval request opens a window of
  `notifications.approval_batch_s` (120 s); every request until it closes joins it, and when it closes one message
  lists them all — `[1] Approval requested: …`, `[2] …` — with a row of buttons each (**Approve 1** / **Deny 1**, …).
  Each row decides only its own approval with its own single-use tokens; after a press the message says
  `[2] Denied from Telegram by @you.` and keeps the rows still open. A lone approval arrives as before, at most two
  minutes late; ten at most share a message. Set the window to 0 to send each at once.
- **Quiet hours** hold Telegram messages back (they stay in the in-app history and in the next digest's open
  approvals) — except failures, which always go out. Times are local to the instance timezone; a window like
  22:00–08:00 spans midnight.
- **The bot token** is write-only: Cadence stores it in the secret store as `telegram-bot-token` and never returns
  it. Replacing it needs the settings' current revision (another tab changing them meanwhile answers `412`).
- **Allow-listed chats**: the bot talks to these chat ids only (at most 20). A chat that writes to the bot without
  being allowed gets no answer; it appears under **Chats that wrote to the bot** with its title so you can allow it.
- **Bot commands** (allow-listed chats only; read-only): `/status` — the step queue (running first, with card and
  progress), today's GPU spend against each project's budget (since local midnight) and the pending approvals;
  `/approvals` — each pending approval again (the oldest ten) with fresh Approve / Deny buttons; `/help` — the list.
  Any other text starting with `/` answers with the list; plain text is ignored.
- **Buttons**: each carries a single-use token signed with a key derived from the master key (HMAC), valid until the
  approval expires (24 h). Pressing Approve spends Deny too; a second press says *Already decided*; a button whose
  data was altered, or a press from a chat that is not allowed, decides nothing.

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Edit a rule | `notificationRules.edit` | If-Match on the rule's revision |
| Save quiet hours / digest time / chats | `notificationSettings.edit` | If-Match on the settings' revision; `telegramChats` is the complete allowlist |
| Store / Replace the bot token | `telegramBot.set` | Write-only; If-Match once a token is stored |
| Send test message | `telegramBot.verify` | `getMe` with the stored token, then a test message to every allowed chat; a dry run only checks the token |
| (the timezone) | `policies.edit` with `timezone` | An IANA name such as `Europe/Berlin` |

Agents cannot run any of these (the presets' `admin-only` rule).

## Playbooks

- **Set up the bot**: talk to @BotFather in Telegram, `/newbot`, copy the token; Settings → Notifications → Bot token
  → Store. Write `/start` to your bot from your phone; your chat appears under *Chats that wrote to the bot* → Allow.
  Press **Send test message**, then send `/status` to see the queue and budgets.
- **Nothing arrives**: the status line shows the last Bot API error. `401 Unauthorized` means the token is wrong or
  was revoked in @BotFather — store a new one. *not polling* means the control plane cannot reach
  `api.telegram.org` (it needs outbound HTTPS; the compose file's default network has it).
- **Too many messages**: tick *Silent* on the classes you only want to read later, hold Outcome for the digest, or
  switch quiet hours on. Approval bursts already share one message; a longer `notifications.approval_batch_s`
  gathers more of them.
- **An approval decided in Cadence first**: its buttons answer *Already decided in Cadence*.

## Sources

- docs/spec/06-platform.md "Notifications"; docs/spec/08-resolutions.md R5 (approval expiry), R9 (secret store).
- Telegram Bot API (core.telegram.org/bots/api): `getUpdates` long polling, inline keyboards, `callback_data` of at
  most 64 bytes.
