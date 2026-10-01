---
title: Artifact not evictable
summary: artifacts.evict named an artifact that something still needs (or that is not a live training state); nothing was evicted.
contexts: [error:artifact-not-evictable, entity:artifact, guide:freeing-store-space]
---

## What this is

A `409 Conflict` problem of type `artifact-not-evictable`, answered to `artifacts.evict` when its body lists `hashes`
and at least one of them may not be evicted. The whole request is refused; `detail` names each refused hash with the
reason, for example:

- `its run is still running` — the run may still resume from it;
- `the newest state of a failed run: runs.resume continues from it`;
- `a waiting or running step job names it (an input or overrides.resumeFrom)`;
- `a registry version references it`, `a registered checkpoint`, `a file of another live artifact`;
- `the backup mirror does not hold … yet; evict it after the next backup`;
- `not a live training-state artifact` — unknown, another type, or already evicted.

## Place in the loop

Retention frees the content store of training states nothing will resume from (see *Freeing store space*). Naming
hashes is the precise form; without `hashes` the command selects the evictable states itself and reports the rest
under `kept` instead of refusing.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/artifact-not-evictable` |
| `status` | `409` |
| `detail` | `<hash>: <reason>` for each refused artifact, separated by `;` |

## Commands

- `artifacts.evict?dryRun=true` — the plan: what would be evicted, what is kept and why, the bytes freed.
- `artifacts.get` — one artifact, with `evicted` once its blobs are gone.
- `backups.new` — take a backup now, so the mirror holds the blobs before you evict them.

## Playbooks

- Drop the refused hashes and send the request again, or send it without `hashes` and read `kept`.
- A state kept for the backup mirror becomes evictable once a backup set (nightly, or `backups.new`) copied it.

## Sources

- docs/spec/06-platform.md "Artifacts, metrics and logs" — Retention.
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
