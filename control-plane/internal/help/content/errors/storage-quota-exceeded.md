---
title: Project storage quota exceeded
summary: Freezing this dataset version would put the project's cached datasets past its quota (storage.project_quota_gb); nothing was frozen.
contexts: [error:storage-quota-exceeded, panel:storage]
---

## What this is

A `409 Conflict` problem of type `storage-quota-exceeded`. Each project may hold `storage.project_quota_gb` (200 GB)
of cached dataset versions it froze or imported — the bytes of their shards in the local cache (the content store).
A freeze that would pass it is refused; `detail` says how much the project holds, its quota and what the freeze adds.

Evicted versions do not count: their shards live on a mount, not in the cache.

## Place in the loop

Data — freezing a dataset version (`datasets.freeze`). The cache sweep also evicts over-quota projects' shards
first when the cache passes its high-water mark.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/storage-quota-exceeded` |
| `status` | `409` |
| `detail` | The project's cached dataset bytes, its quota, and the bytes the freeze adds |

| Default | Value | Source |
| --- | --- | --- |
| `storage.project_quota_gb` | 200 | Cadence recommendation (phase-4 plan, decision 2) |

## Commands

- `storage.get` — every project's cached dataset bytes against its quota, and which versions are evictable.
- `datasets.evict` — free a version the project no longer trains on (its shards must also live on a mount).
- `datasets.materialize` — bring an evicted version back when you need it again.

## Playbooks

- Evict versions the project no longer trains on, then freeze again.
- A version that cannot be evicted because its shards exist on no mount: export it to a writable mount first
  (phase 4, stream I), scan that mount, then evict.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, decision 2 (the cache is the content store; per-project quotas).
- docs/spec/05-agents.md "Cache fairness".
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
