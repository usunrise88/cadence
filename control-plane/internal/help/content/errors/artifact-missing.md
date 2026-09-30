---
title: Artifact missing
summary: A lease was released with an output the content store does not hold; write or upload it first.
contexts: [error:artifact-missing, entity:artifact]
---

## What this is

A `409 Conflict` problem of type `artifact-missing`, answered to `workerLeases.release`. The outcome named an output
artifact by hash, but no blob with that hash is in the content store. The release is refused as a whole and the lease
stays active, so the worker can store the output and release again.

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

## Playbooks

- Worker: write each file of a directory artifact and then its manifest (`cas/tmp/` and rename), or upload them, before
  releasing; keep heartbeating meanwhile so the lease is not reaped.

## Sources

- docs/spec/08-resolutions.md R15 — content-addressed artifact store.
- RFC 9110, HTTP Semantics, §15.5.10 409 Conflict; RFC 9457, Problem Details for HTTP APIs.
