---
title: Artifact evicted
summary: A read of an eval's per-utterance artifact (hypotheses or scores) that the age retention removed from the content store; run the eval again to bring it back.
contexts: [error:artifact-evicted, entity:artifact, guide:freeing-store-space, guide:evaluation]
---

## What this is

A `410 Gone` problem of type `artifact-evicted`. An eval record's per-utterance artifacts — its hypotheses, its scores
and the metric scores beside it — stay in the content store `eval.artifact_retention_days` (30) days after the record
was last used; then the daily retention sweep evicts them (owner decision 2026-10-03). The record itself stays: its
summary, every cell's delta and every gate verdict that read it are kept for ever. What answers `410` is a read of the
bytes, for example `words.get` (the Audio panel's word track) with an evicted `hypotheses` or `scores` artifact.
`detail` names the artifact and the day it was evicted.

## Place in the loop

Evaluate → review the worst utterances and their audio → decide. Weeks later the numbers are still there, the
utterance rows and words are not; the Eval report says so on the cell instead of listing rows.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/artifact-evicted` |
| `status` | `410` |
| `detail` | The artifact type and hash, the eviction day, the retention in days |
| `eval.artifact_retention_days` | 30 days after the record's last use (range 1–3650) |

## Commands

- `evals.new` with the same subject, baseline and golden sets — a record whose artifacts were evicted is never taken
  from the cache: the cell is computed again and the record gets the new artifacts (its summary stays). The old eval
  links the same record, so its rows come back too.
- `artifacts.get` — the artifact with `evicted {at, by, jobId}`.

## Playbooks

- **The Audio panel shows "Artifact evicted".** Run the eval again (Eval report → **Run eval…**, or `evals.new`), wait
  for the cells, open the utterance again.
- **Keep an eval's rows longer.** Register the model it gated (a registered model's eval is never evicted), or raise
  `eval.artifact_retention_days`.
- With a backup mirror the eviction can be undone: copy the blobs back from `CADENCE_BACKUP_DIR/cas/` to the same
  paths under the content store and restart the control plane (see *Freeing store space*).

## Sources

- docs/spec/06-platform.md "Artifacts, metrics and logs" — Retention; docs/spec/00-overview.md decision log
  (2026-10-03).
- RFC 9110, HTTP Semantics, §15.5.11 410 Gone; RFC 9457, Problem Details for HTTP APIs.
