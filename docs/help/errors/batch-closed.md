---
title: Annotation batch closed
summary: The batch is freezing or frozen; its items, annotations and reviewers no longer change.
contexts: [error:batch-closed, entity:annotation_batch, panel:annotation-batch, panel:triage]
---

## What this is

A `409 Conflict` problem of type `batch-closed`. Once a batch's freeze is approved it is **freezing** (its accepted
items are being cut into the content store), then **frozen** (the dataset version, and for a golden-set batch the
golden set, are registered). From then on:

- `annotations.new` and `batchItems.accept` are refused: the frozen transcripts are what the golden set holds;
- `invitations.new` is refused, and every invitation of the batch and every reviewer session it started were revoked
  when the freeze began;
- `batches.freeze` again answers this problem (a failed freeze is the exception: it can be retried).

## Place in the loop

Data → evaluation: annotation batch → freeze → golden set or training dataset version.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/batch-closed` |
| `status` | `409` |
| `detail` | The batch and its state |

## Commands

- `batches.get` — `state` and `freeze` (the approval, the cut's pipeline run, the dataset and golden set versions).
- `batches.new` — annotate more in a new batch; frozen versions never change.

## Playbooks

- A reviewer sees this after the freeze: their work is in; the link no longer opens the batch.
- A transcript in a frozen golden set is wrong: annotate the item again in a new batch and freeze a new golden set
  version; eval records keep pointing at the version they were scored against.

## Sources

- docs/spec/04-blocks.md "Annotation workflow"; docs/spec/06-platform.md "Authentication and access" (a reviewer loses
  access when the batch closes).
- RFC 9457, Problem Details for HTTP APIs.
