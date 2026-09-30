---
title: Backups and the restore test
summary: Settings → Backups — the nightly pg_dump and content-store copy, the weekly restore into a scratch database with its report, retention, and restoring by hand.
contexts: [guide:backups, panel:settings]
---

## What this is

Every night the control plane takes a **backup set**: a `pg_dump` of the database (custom format) and a copy of the
content-store blobs that are new since the last set. Once a week it proves a set works: it restores the newest set
into a **scratch database** on the same server, compares row counts of the key tables and the migration version with
what the set recorded, re-hashes a sample of the copied blobs, and drops the scratch database. A failed backup or
restore test is a *failure* notification (in-app and Telegram, even in quiet hours).

Targets: 24 h recovery point, 1 h recovery time (docs/spec/06-platform.md "Operations").

A set lives in `CADENCE_BACKUP_DIR/sets/<time>-<trigger>-<id>/`:

| File | What it is |
| --- | --- |
| `cadence.dump` | `pg_dump --format=custom`, taken on an exported snapshot, so the row counts in the manifest are exact |
| `manifest.json` | id, trigger, time, dump size and SHA-256, pg_dump and server versions, migration version, row counts, blob counts |
| `secrets/` | the secret store's sealed values — still encrypted |

Content-store blobs are immutable, so they are mirrored once into `CADENCE_BACKUP_DIR/cas/` and shared by every set.
**The master key is not in any set**: back up `CADENCE_MASTER_KEY_FILE` separately and keep it apart from the sets —
without it the secrets in a restored instance cannot be read, and with the sets alone nobody else can read them.

## Place in the loop

Operate. Nothing to do day to day; check the last restore test when you look at Settings.

## Fields and defaults

| Field | Default | Source |
| --- | --- | --- |
| Nightly at | `03:00` local | `defaults.yaml` `backups.nightly_at`; docs/spec/06 (nightly) |
| Restore test | Sundays `04:00` local | `backups.restore_test_weekday`, `backups.restore_test_at` |
| Nightly (and manual) sets kept | 7 | `backups.keep_nightly` (1–60) |
| Weekly sets kept | 4 | `backups.keep_weekly` (1–52) |
| Directory | `$CADENCE_DATA_DIR/backups` (compose: the `cadence-backups` volume at `/backups`) | `CADENCE_BACKUP_DIR`; a mount in phase 4 |

The set taken on the restore-test day is the **weekly** set. Retention removes older sets' files and keeps their rows
(*files removed*); the content-store mirror is never pruned. A control plane that was down at the scheduled time
catches up as soon as it starts. One set is taken at a time.

The control-plane image ships `postgresql-client-17` to match the compose file's `postgres:17`; a client older than
the server refuses to dump (the set fails with the version to install). `CADENCE_PG_DUMP` / `CADENCE_PG_RESTORE`
override the commands.

## Commands

| Command | API | Notes |
| --- | --- | --- |
| Back up now | `backups.new` | Answers the job (`202`); a set already running answers `409` |
| Restore test | `backups.verify` | On one set, now; If-Match on the set's revision; answers the job |
| (list, one set) | `backups.list`, `backups.get` | The list carries the schedule and the last restore test |

## Playbooks

- **Restore after losing the database** (the upgrade guide's rollback uses the same steps):
  1. Stop the control plane (`docker compose stop control-plane agent-host`).
  2. Recreate the database: `docker compose exec postgres dropdb -U cadence cadence && docker compose exec postgres createdb -U cadence cadence`.
  3. Restore the newest good set: `docker compose exec -T postgres pg_restore -U cadence -d cadence --no-owner --no-privileges < <set>/cadence.dump`.
  4. Put the content store back: copy `CADENCE_BACKUP_DIR/cas/b3` into `$CADENCE_DATA_DIR/cas/b3`, and the set's
     `secrets/` into `$CADENCE_DATA_DIR/secrets/`; restore the master key file.
  5. Start the image the set was taken with (its migration version is in `manifest.json`).
- **The restore test failed**: the report lists the tables whose row counts differ, a migration mismatch or a blob
  that no longer matches its hash (bit rot in the mirror). Take a new set with **Back up now** and run its restore
  test; keep the failed set's files for inspection.
- **The disk is filling**: lower the kept counts in `defaults.yaml` (a release change) or move `CADENCE_BACKUP_DIR`
  to a larger mount.

## Sources

- docs/spec/06-platform.md "Operations" (backups, 24 h RPO, 1 h RTO); PostgreSQL 17 documentation: `pg_dump`
  (`--format=custom`, `--snapshot`), `pg_restore`, `pg_export_snapshot()`.
