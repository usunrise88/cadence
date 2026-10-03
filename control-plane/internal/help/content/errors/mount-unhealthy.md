---
title: Mount unhealthy
summary: A step job names a mount whose last health check failed; it was not started.
contexts: [error:mount-unhealthy, entity:mount, panel:storage, step:mount_check]
---

## What this is

A `409 Conflict` problem of type `mount-unhealthy`. A job never starts on an unhealthy mount: when a worker would
take a step job whose parameters (or inputs' metadata) name `mount://<name>/…` and that mount's last health check
failed, the job ends at once as failed with error type `input` and this message, naming the mount and the check's
reason (the path is not a directory on the worker, the bucket refused the listing, the Hub revision does not
exist, a probe write failed on a writable mount).

A mount never checked (`unknown`) does not block jobs. The health check itself (`mount_check@1`) always runs.

## Place in the loop

Data — ingest (`sdp_ingest`), freeze, exports and evaluations that read audio where it lives.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/mount-unhealthy` |
| `status` | `409` |
| `detail` | The mount and the reason of its last failed check |

The mount's `health` (`mounts.get`) holds the check: `state`, `checkedAt`, `host`, `reachable`, `detail`.

## Commands

- `mounts.verify` — check the mount again now; a passing check unblocks it.
- `mounts.get` — the last health check and the job that ran it.
- `pipelineRuns.retry` — retry the failed step once the mount is healthy.

## Playbooks

- Local, NFS or SMB: is the host directory bound into the worker (compose `CADENCE_CORPORA_DIR`,
  `CADENCE_EXPORTS_DIR`), mounted by the OS, and readable by the worker's user? Fix it, then `mounts.verify`.
- S3: is the endpoint reachable from the worker host, and does the secret hold `<accessKeyId>:<secretAccessKey>`?
- Hub: does the revision exist, and does a gated repository have a token secret?

## Sources

- docs/spec/02-domain-projects-registry.md "Storage and mounts" (a job never starts on an unhealthy mount).
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
