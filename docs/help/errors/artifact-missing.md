---
title: Artifact missing
summary: A lease was released with an output the content store does not hold, or a pipeline input or a dataset training reads was evicted; store or restore it first.
contexts: [error:artifact-missing, entity:artifact, guide:freeing-store-space]
---

## What this is

A `409 Conflict` problem of type `artifact-missing`, answered to `workerLeases.release`. The outcome named an output
artifact by hash, but no blob with that hash is in the content store. The release is refused as a whole and the lease
stays active, so the worker can store the output and release again.

`pipelines.run` (and the run facades) answer it too when an input names an artifact that `artifacts.evict` removed
from the store: the index row says when; restore its blobs from the backup mirror (see *Freeing store space*).

A run whose training step would read a dataset version the cache evicted (`datasets.evict` or the high-water sweep:
its shards are on a mount, not in the content store) is refused the same way, at the dry run of `runs.new` and
`pipelines.run` and again when the training step is queued, directly or through a mix. The detail names the version;
bring it back with `datasets.materialize` (`{versionId}`), wait for its job, and run again. Cadence does not
materialise it for you: a copy back can take hours and counts against the project's storage quota.

## Place in the loop

A step's outputs become artifacts the next steps and the output hooks read (a dataset version, checkpoints). The
control plane records them only once their bytes are safely stored (R15).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/artifact-missing` |
| `status` | `409` |
| `detail` | The output name and its hash |

## Commands

- `workerArtifacts.set` — upload a blob when the worker does not share the store's volume.
- `workerLeases.release` — release again once the outputs are stored.
- `artifacts.get` — whether an input was evicted, and when.
- `datasets.materialize` — copy an evicted dataset version back from its mount copies (dry run: how much, from where).

## Playbooks

- Worker: write each file of a directory artifact and then its manifest (`cas/tmp/` and rename), or upload them, before
  releasing; keep heartbeating meanwhile so the lease is not reaped.

## Sources

- docs/spec/08-resolutions.md R15 — content-addressed artifact store.
- docs/spec/02-domain-projects-registry.md "The cache and materialisation".
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
