-- 0043 · Quieter notifications (docs/spec/06-platform.md "Notifications", "Quieter Telegram"): a per-class silent
-- flag (Telegram disable_notification), copied onto each delivery when it is routed, and a dedupe key so an end that
-- several events repeat (an agent session's) is told once.

ALTER TABLE notification_rules ADD COLUMN silent boolean NOT NULL DEFAULT false;
-- Seeded: approvals and failures ring; outcomes and the digest arrive silently; progress never reaches the phone.
UPDATE notification_rules SET silent = true WHERE event_class IN ('outcome', 'digest');

ALTER TABLE notification_deliveries ADD COLUMN silent boolean NOT NULL DEFAULT false;
ALTER TABLE notification_deliveries ADD COLUMN dedupe_key text;
CREATE UNIQUE INDEX notification_deliveries_dedupe_idx ON notification_deliveries (dedupe_key, channel)
    WHERE dedupe_key IS NOT NULL;
