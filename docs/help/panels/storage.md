---
title: Storage
summary: Mounts and their health, the local cache's use against its water marks, each project's quota, and which dataset versions are pinned, evictable or evicted.
contexts: [panel:storage, entity:mount, guide:freeing-store-space]
---

## What this is

A tool panel (Data and Ops workspaces). Audio lives where it already is — a local disk, an NFS or SMB share, an
S3-compatible bucket, a Hugging Face Hub repository — and Cadence names it by **mount**:
`mount://<mount>/<path>[#t=<start>,<end>][&ch=<n>]` (a whole file, or a segment of one channel). Only what a job
needs is copied into the **local cache** (the content store), and only for as long as something pins it.

| Part | Meaning |
| --- | --- |
| Mounts | One row per mount: name, kind, root, read-only or writable, health (healthy, unhealthy with the reason, unknown, from the last check a worker ran), free space and throughput, the last scan (files, bytes, top-level entries such as `<source>/<revision>`), utterances that name it and content-store blob copies found on it. **Rescan** (`mounts.scan`) and **Check health** (`mounts.verify`) per mount; **Add mount** (`mounts.new`) asks the admin for an approval |
| Cache | The content store's filesystem: used against the high-water mark (`storage.cache_high_water_pct`, 85 %) and the low-water mark (`storage.cache_low_water_pct`, 70 %), the bytes of cached dataset versions, what an eviction could free, and when the sweep last looked |
| Quotas | Each project's cached dataset bytes against `storage.project_quota_gb` (200 GB); over-quota projects are marked |
| Dataset versions | Every dataset version with content-store shards, least recently used first: cached or evicted, the shards with a copy on a mount, and why it is pinned. **Evict** (`datasets.evict`) and **Materialize** (`datasets.materialize`) per version |

Live topics: `mount.{id}` (`mount.created`, `mount.health`, `mount.scanned`) and `entity.artifact.{hash}`
(`artifact.evicted`, `artifact.restored`) refresh the panel.

### Pinned, evictable, evicted

- **Pinned**: a waiting or running step job or a running pipeline names the version, a model version that holds an
  alias (promoted) was trained on it, or a golden set is built on it. A pinned version is never evicted.
- **Evictable**: not pinned, and every shard either has a copy on a mount (found by a scan under `…/b3/<ab>/<hash>`,
  the layout of the content store and of its backup mirror, or recorded by an export) or is also listed by another
  cached artifact. A shard that exists on no mount — imported audio — is never evicted. A scan counts a file named
  like a blob as its copy only when the sizes match (the inventory's `blobsMismatched` counts the rest), and an
  eviction reads every copy it relies on back and checks its hash before it deletes the cached blob: a copy that is
  gone or changed is dropped, and a version whose copy cannot be read now (an unreachable mount) stays cached — the
  eviction job's result says which and why. Reading the copies back costs about what a materialisation would.
- **Evicted**: the shards are gone from the cache (the manifest stays). `datasets.materialize` copies them back from
  their mount copies, verifying each by hash, shard by shard; a retry skips what is back already.

Above the high-water mark, the cache sweep (every `storage.cache_sweep_minutes`, 15) evicts evictable versions —
over-quota projects first, then least recently used — down to the low-water mark, as the system and without an
approval: everything it removes can be brought back. A run on an evicted version is refused before it is queued
(`artifact-missing`): materialise it first.

## Place in the loop

Data — before ingest (a mount to index), around freezes (quotas) and before training (shards in the cache).

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `storage.cache_high_water_pct` | 85 | Use above which the sweep evicts |
| `storage.cache_low_water_pct` | 70 | Use the sweep evicts down to |
| `storage.project_quota_gb` | 200 | Cached dataset bytes a project may have frozen |
| `storage.cache_sweep_minutes` | 15 | How often the sweep looks |
| `storage.mount_check_hours` | 6 | How often every mount's health is checked |
| `storage.mount_check_timeout_minutes` | 10 | How long a health check waits for a worker |
| `storage.mount_check_sample_mb` | 64 | How much the health check reads for throughput |
| `storage.mount_scan_max_files` | 2 000 000 | Files a scan walks before it stops (truncated) |

Mount kinds: `local`, `nfs`, `smb` (a path the OS mounted, bound into the control plane and every worker at the
same path), `s3` (bucket[/prefix], endpoint, and a secret holding `<accessKeyId>:<secretAccessKey>`), `hf`
(`datasets/<org>/<name>` or `<org>/<model>` at a commit SHA). Mounts are read-only unless registered writable, and
never edited: a new location is a new mount.

## Commands

`mounts.list`, `mounts.get`, `mounts.new` (approval, the admin decides), `mounts.scan`, `mounts.verify`,
`storage.get`, `datasets.evict`, `datasets.materialize`. Agents may list, scan, verify, evict and materialise; a
mount an agent asks for waits for the admin like anyone's.

## Playbooks

- **Attach a corpus**: the admin binds the host directory (compose `CADENCE_CORPORA_DIR` → `/mnt/corpora`), adds
  mount `corpora` (kind `local`, root `/mnt/corpora`, read-only), approves it; the first health check follows, then
  **Rescan** shows the sources and revisions on it.
- **The cache is full**: read **Dataset versions** for evictable ones; a version whose shards exist on no mount can
  only go after an export to a writable mount and a rescan of it.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, decisions 1–3 (stream M).
- docs/spec/02-domain-projects-registry.md "Storage and mounts", "Materialisation"; docs/spec/05-agents.md "Cache
  fairness".
