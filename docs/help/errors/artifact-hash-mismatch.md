---
title: Artifact hash mismatch
summary: The uploaded bytes do not hash to the artifact hash in the URL, so the content store refused them.
contexts: [error:artifact-hash-mismatch, entity:artifact]
---

## What this is

A `422` problem of type `artifact-hash-mismatch`, answered to `workerArtifacts.set` (`PUT /worker-artifacts/{hash}`).
The content store is addressed by content: a blob's name is `b3:` and the BLAKE3-256 of its bytes. The control plane
hashes what it receives while writing it; bytes that hash to something else are discarded, never stored under the
wrong name.

## Place in the loop

Workers on the control plane's host write artifacts straight into the shared store (`CADENCE_CAS_DIR`); a worker
without that volume uploads each blob, then releases its lease with the output manifest (R15). A mismatch usually
means a truncated upload or a hash computed over different bytes (for a directory artifact: the manifest, not a tar).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/artifact-hash-mismatch` |
| `status` | `422` |
| `detail` | The hash the upload was sent under |

## Commands

- `workerArtifacts.set` — upload the blob again.
- `workerLeases.release` — refuses outputs the store does not hold (`artifact-missing`).

## Playbooks

- Worker: hash the file you send (BLAKE3-256, lower-case hex after `b3:`), retry the upload once, then fail the step
  with error type `step` if it keeps failing.

## Sources

- docs/spec/08-resolutions.md R15 — content-addressed artifact store.
- BLAKE3 specification, <https://github.com/BLAKE3-team/BLAKE3-specs>.
- RFC 9457, Problem Details for HTTP APIs.
