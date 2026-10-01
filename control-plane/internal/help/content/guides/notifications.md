---
title: Notifications and the Telegram bot
summary: Settings → Notifications — the routing table by event class, quiet hours, the 09:00 digest, and a Telegram bot that sends failures, lets you approve or deny from the phone and answers /status and /approvals.
contexts: [guide:notifications, panel:settings]
---

## What this is

Cadence tells you about events on two channels: the **in-app history** (the bell in the status bar, built from the
live event stream) and a **Telegram bot**. One routing table decides, per event class, which channels an event
reaches and when. Approval requests arrive on Telegram with **Approve** and **Deny** buttons; a press decides the
approval as the admin, recorded with channel `telegram` in the approval and the audit log, exactly like a decision in
the Approvals panel.

| Event class | Covers | In-app | Telegram | Timing (seeded) |
| --- | --- | --- | --- | --- |
| Approval requested | agent, automation and registry approvals | yes | message with the estimate and Approve / Deny | as it happens |
| Failure | job failed, pipeline step failed (no retry left), compute host unreachable, mount unhealthy, backup or restore test failed, content store low on space (below `cache.store_low_free`, at most once a day) | yes | yes, even in quiet hours | as it happens |
| Outcome | gate verdict, promotion, schedule finished, batch closed | yes | yes | as it happens |
| Progress | job done, pipeline step done, checkpoint saved, triage item added, backup taken | yes | no | — |
| Daily digest | runs and evals (their jobs), GPU spend against budgets, open approvals, events held for it, the last backup | yes | yes | 09:00 local |

## Place in the loop

Operate. Set it up once after the first start; the defaults follow the table above.

## Fields and defaults

| Field | Default | Source |
| --- | --- | --- |
| Instance timezone (Policies) | `UTC` | `defaults.yaml` `operations.timezone`; Cadence recommendation |
| Digest time | `09:00` | `notifications.digest_time`; docs/spec/06-platform.md "Notifications" |
| Quiet hours | off; `22:00`–`08:00` when switched on | `notifications.quiet_hours_*`; Cadence recommendation (they end before the digest) |
| Telegram timing per class | as in the table | migration 0015 seeds the spec's table |

- **Telegram timing**: *As it happens*, *In the daily digest* (held and listed in the next digest), or *Never*. The
  digest row is *Daily* or *Never*. The in-app history is always immediate; its checkbox only switches a class off.
  Progress never interrupts: on Telegram it can only be held for the digest. A changed row shows **changed**.
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
- **Too many messages**: hold Outcome for the digest, or switch quiet hours on.
- **An approval decided in Cadence first**: its buttons answer *Already decided in Cadence*.

## Sources

- docs/spec/06-platform.md "Notifications"; docs/spec/08-resolutions.md R5 (approval expiry), R9 (secret store).
- Telegram Bot API (core.telegram.org/bots/api): `getUpdates` long polling, inline keyboards, `callback_data` of at
  most 64 bytes.
