---
title: Lease ended
summary: The worker's lease on this step is over — reaped after missed heartbeats — so the worker must stop the step and claim again.
contexts: [error:lease-ended, entity:job]
---

## What this is

A `409 Conflict` problem of type `lease-ended`. A worker sent a heartbeat (`workerLeases.report`), logs, metric points
or a release for a lease that is no longer active. The usual cause: the worker missed three heartbeats in a row
(30 s), so the control plane reaped the lease, ended the step as failed with error type `lost` and freed the card.
A worker process that registers again from a new instance (after a restart) reaps its old leases at once, too.

## Place in the loop

A pipeline step runs on a worker as a lease on one card (docs/review/2026-09-30-phase-2-plan.md "Worker protocol").
Once a lease is reaped the pipeline engine decides about a retry; another lease may already run the retry, so the
old process must not write to the job any more. A release of a lease that was already released is not an error (a
retried request succeeds without changing anything).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/lease-ended` |
| `status` | `409` |
| `detail` | The lease and how it ended (`reaped`) |

Heartbeat interval: 10 s; a lease is reaped after 3 missed beats (Cadence recommendation, R14).

## Commands

- `workerLeases.claim` — the worker asks for new work.
- `jobs.get` — the job and its last message; `queueEntries.list` — what runs where now.

## Playbooks

- Worker: stop the step's subprocess at once, discard its partial outputs, and claim again.
- Operator: frequent reaping means the worker cannot reach the control plane in time — check the network, the
  worker's load and its logs.

## Sources

- docs/spec/08-resolutions.md R14 — heartbeats every 10 s, reaped after 3 misses.
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
