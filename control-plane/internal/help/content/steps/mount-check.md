---
title: mount_check (step kind)
summary: The mount health check a worker runs — reachable, free space, a throughput sample and, for a writable mount, a probe write.
contexts: [step:mount_check, entity:mount, panel:storage]
---

## What this is

`mount_check@1` checks one mount from the worker host that runs it. It reads the mount through its lease (the
lease carries every mount; see [Storage](../panels/storage.md)) and reports final metrics:

- `reachable` — 1 when the mount answered;
- `free_bytes`, `total_bytes` — the filesystem under the root (local, NFS and SMB mounts; object storage and the Hub
  report no free space);
- `throughput_mbps`, `sampled_bytes` — a read of up to `sample_mb` of the mount's files, at most 64 files, in name
  order;
- `writable` — 1 when a writable mount accepted a probe file `.cadence-probe-<random>` (removed again at once).

A mount that does not answer — a path that is not a directory on the worker, a bucket that refuses the listing, a
Hub revision that does not exist, a failed probe on a writable mount — fails the step with the reason. The control
plane then records the mount as `unhealthy` with that reason, and a step job that names the mount (`mount://<name>/`
in its parameters) fails at once with [mount-unhealthy](../errors/mount-unhealthy.md) until a check passes again.

It is a runtime-neutral core kind (CPU, job kind `data`), shipped in every worker image. It is not used in pipelines:
the control plane queues it itself as the job `mounts.verify`.

## Place in the loop

Data — before anything reads a mount: when a mount is registered, after every `mounts.scan`, on `mounts.verify`
and every `storage.mount_check_hours` (6 h).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `mount` | — (set by the control plane) | Cadence recommendation | a mount name |
| `uri` | `mount://<name>/` (set by the control plane) | Cadence recommendation | ≤ 100 characters |
| `sample_mb` | 64 (`storage.mount_check_sample_mb`) | Cadence recommendation | 1–4096 |
| `probe_write` | true for a writable mount | Cadence recommendation | — |

A check no worker picks up within `storage.mount_check_timeout_minutes` (10) is dropped; the mount keeps its state
and its health says no worker ran the check.

## Commands

- `mounts.verify` — run the check now (202 with the job; event `mount.health` on `mount.{id}` when it ends).
- `mounts.get`, `mounts.list` — the last result under `health`.
- `mounts.scan` — rescan the mount; a check follows.

## Playbooks

- Unhealthy local, NFS or SMB mount: make sure the host directory is bound into the worker (compose:
  `CADENCE_CORPORA_DIR`, `CADENCE_EXPORTS_DIR`) and readable by the worker's user, then `mounts.verify`.
- Unhealthy S3 mount: check the endpoint and the secret (`<accessKeyId>:<secretAccessKey>`).

## Sources

Cadence recommendation — docs/review/2026-10-03-phase-4-plan.md (stream M); docs/spec/02-domain-projects-registry.md
"Storage and mounts" (the health check the worker runs).
