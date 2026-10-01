---
title: Upgrading Cadence
summary: Pull the release, compose up; forward-only migrations run under an advisory lock; rollback is the previous image plus the last backup. With the release matrix of what each version pins.
contexts: [guide:upgrading]
---

## What this is

Cadence upgrades itself the way it upgrades models: versioned, forward-only, with a nightly backup that is restored
every week to prove it works (see [Backups](backups.md)). Releases are semver; every image of one release carries the
same `CADENCE_VERSION` (`make up` sets it from git).

## Place in the loop

Operate. Upgrade between runs: a running training step is interrupted when the worker restarts.

## Fields and defaults

The **release matrix** — what a release pins — is stated in each release's notes:

| Component | Pinned as | Where |
| --- | --- | --- |
| Control plane, agent host, egress proxy | one `CADENCE_VERSION` | `docker-compose.yml` images |
| PostgreSQL server and client | `postgres:17`, `postgresql-client-17` in the control-plane image | `docker-compose.yml`, `control-plane/Dockerfile` |
| NeMo Speech runtime | the NGC tag, by digest (`nvcr.io/nvidia/nemo-speech:26.07`) | the worker image |
| Dockview | an exact MIT version | `web/package.json` |
| Agent adapters | `claude-agent-acp`, `opencode` versions | the agent-host image |
| Defaults | `defaults.yaml` `version` | embedded in the binary |

A major PostgreSQL upgrade is its own step (dump, new server, restore), never part of a Cadence upgrade.

## Commands

1. **Check the last backup**: Settings → Backups shows the newest set and the last restore test. If the restore test
   is older than a week, press **Back up now** and **Restore test** first.
2. **Pull and start**: `git pull` (or fetch the release), then `CADENCE_VERSION=<version> docker compose up -d --build`
   (`make up`). Compose replaces the containers; the agent host releases its sessions on the way down.
3. **Migrations** run at start, embedded in the binary, forward-only and expand-and-contract, each in its own
   transaction, while the starting control plane holds a Postgres advisory lock — a second instance waits, then finds
   nothing left to do. A control plane refuses to start on a database that a newer release already migrated.
4. **A failed migration stops the start** and leaves the previous image runnable: its migrations are a prefix of the
   new ones, and the failed one rolled back.
5. **Check**: `/healthz`, Settings → Compute health, and the log line `cadence control plane started` with
   `migrations_applied`.

## Playbooks

- **Roll back**: start the previous image (`CADENCE_VERSION=<previous> docker compose up -d`). If the new release
  applied migrations — the previous image then refuses the newer database — restore the last backup taken before the
  upgrade (the restore steps are in [Backups](backups.md)), then start the previous image. Data written after that
  backup is lost, which is why step 1 takes a fresh set.
- **Upgrade everything together**: the control plane, agent host, egress proxy and worker of one release share one
  version; do not mix releases.

## Sources

- docs/spec/06-platform.md "Operations" (versioning, install, migrations, upgrade, backups); Cadence recommendation
  for the step order.
