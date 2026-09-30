-- 0015 · Phase 2 (stream O): notification routing, the Telegram bot, backups (docs/spec/06-platform.md
-- "Notifications" and "Operations").

-- The instance timezone (policies.timezone): quiet hours, the daily digest and the backup schedule follow it. NULL
-- reads through to defaults.yaml operations.timezone, like the budgets.
ALTER TABLE policies ADD COLUMN timezone text;

-- The routing table: one row per event class (ntr_<class>), seeded with the spec's table. channels/timing are
-- what notificationRules.edit changes; bypass_quiet_hours is fixed per class (failures only).
CREATE TABLE notification_rules (
    id                 text PRIMARY KEY,
    event_class        text NOT NULL UNIQUE,
    position           integer NOT NULL,
    label              text NOT NULL,
    in_app             boolean NOT NULL,
    telegram           boolean NOT NULL,
    timing             text NOT NULL CHECK (timing IN ('immediate', 'digest', 'daily', 'none')),
    bypass_quiet_hours boolean NOT NULL DEFAULT false,
    rev                integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

INSERT INTO notification_rules (id, event_class, position, label, in_app, telegram, timing, bypass_quiet_hours) VALUES
    ('ntr_approval_requested', 'approval_requested', 1, 'Approval requested (agent, automation, registry)', true, true, 'immediate', false),
    ('ntr_failure', 'failure', 2, 'Job failed, mount unhealthy, card closed, backup failed', true, true, 'immediate', true),
    ('ntr_outcome', 'outcome', 3, 'Gate verdict, promotion, schedule finished, batch closed', true, true, 'immediate', false),
    ('ntr_progress', 'progress', 4, 'Progress (step done, checkpoint saved, triage item added)', true, false, 'none', false),
    ('ntr_digest', 'digest', 5, 'Daily digest: runs, evals, spend against budgets, open approvals', true, true, 'daily', false);

-- The notification settings singleton: quiet hours, the digest time, the Telegram allowlist. NULL columns read
-- through to defaults.yaml notifications.*. telegram_seen holds chats that wrote to the bot without being allowed
-- (shown in Settings so the admin can allow one; the bot never answers them). The bot token is the secret
-- telegram-bot-token (internal/secrets), never a column.
CREATE TABLE notification_settings (
    id                  text PRIMARY KEY CHECK (id = 'instance'),
    quiet_hours_enabled boolean,
    quiet_hours_start   text,
    quiet_hours_end     text,
    digest_time         text,
    telegram_chats      jsonb NOT NULL DEFAULT '[]',
    telegram_seen       jsonb NOT NULL DEFAULT '[]',
    telegram_username   text,
    telegram_error      text,
    telegram_sent_at    timestamptz,
    digest_sent_on      date,
    rev                 integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
INSERT INTO notification_settings (id) VALUES ('instance');

-- Deliveries: what the router decided for each event and channel (Telegram only; the in-app history is the event
-- stream itself). queued → sent | failed; suppressed (quiet hours); digest (held for the next digest, then
-- digested). The router writes rows in the transaction that advances its cursor; the sender delivers them.
CREATE TABLE notification_deliveries (
    id           text PRIMARY KEY,                      -- ntf_<uuidv7>
    event_seq    bigint,                                -- the outbox event; NULL for the digest and test messages
    event_class  text NOT NULL,
    channel      text NOT NULL CHECK (channel IN ('telegram')),
    state        text NOT NULL CHECK (state IN ('queued', 'sent', 'failed', 'suppressed', 'digest', 'digested')),
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    approval_id  text,                                  -- approval messages carry Approve / Deny buttons
    attempts     integer NOT NULL DEFAULT 0,
    error        text,
    next_at      timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    UNIQUE (event_seq, channel)
);
CREATE INDEX notification_deliveries_queue_idx ON notification_deliveries (next_at) WHERE state = 'queued';
CREATE INDEX notification_deliveries_digest_idx ON notification_deliveries (created_at) WHERE state = 'digest';

-- Single-use action tokens of inline buttons (approve / deny from Telegram). The button carries the id and an
-- HMAC over (id, action) keyed from the master key; the row makes it single-use and bounds it by the approval's
-- expiry. Using one token of an approval spends its sibling too.
CREATE TABLE notification_tokens (
    id          text PRIMARY KEY,                       -- 16 random base32 characters
    approval_id text NOT NULL REFERENCES approvals (id),
    action      text NOT NULL CHECK (action IN ('approve', 'deny')),
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    used_by     jsonb,                                  -- {chatId, userId, username} of the press
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_tokens_approval_idx ON notification_tokens (approval_id);

-- The router's cursor starts at the current end of the outbox: history is never replayed to Telegram.
INSERT INTO event_cursors (name, seq) SELECT 'notify', coalesce(max(seq), 0) FROM events ON CONFLICT DO NOTHING;

-- Backup sets (bkp_): nightly/weekly/manual pg_dump + content-store copy under CADENCE_BACKUP_DIR, with the
-- weekly restore test's report. Retention removes files and sets pruned_at; the record stays.
CREATE TABLE backups (
    id                text PRIMARY KEY,
    state             text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    trigger           text NOT NULL CHECK (trigger IN ('nightly', 'weekly', 'manual')),
    job_id            text,
    path              text,
    dump_bytes        bigint,
    dump_sha256       text,
    pg_dump_version   text,
    server_version    text,
    migration_version bigint,
    tables            jsonb,                            -- [{name, rows}] at dump time
    cas_blobs         bigint,
    cas_copied        bigint,
    cas_bytes_copied  bigint,
    error             text,
    restore_test      jsonb,
    actor             jsonb NOT NULL,
    rev               integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    created_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    pruned_at         timestamptz
);
CREATE INDEX backups_created_idx ON backups (created_at DESC);
