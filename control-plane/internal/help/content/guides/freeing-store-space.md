---
title: Freeing store space
summary: How artifacts.evict deletes superseded training states from the content store after a person approves it, what it never touches, and how to bring an evicted artifact back from the backup mirror.
contexts: [guide:freeing-store-space, entity:artifact, error:artifact-not-evictable, error:artifact-missing]
---

## What this is

Training states are the largest artifacts in the content store (7.66 GB each for the 0.6B model: one per pause,
window close and `state_every_minutes`) and are only ever read to resume a run. `artifacts.evict` deletes the blobs of
the ones nothing can resume from any more. The artifact's index row stays, marked **evicted** (when, by whom, the
job), so lineage still resolves: `artifacts.get` shows `evicted`, a directory's file list still answers, and its
content says *evicted; restore it from the backup mirror*.

## Place in the loop

Train → the run finishes (or fails and you decide not to resume it) → dry-run the eviction → approve it → the job
frees the space. Nothing evicts on its own in v1; automatic retention will use the same command later.

## Fields and defaults

What is evictable (v1: type `training-state` only):

| Run state | States evicted |
| --- | --- |
| The pipeline run finished (`done`) | All of them |
| `failed` or `cancelled` | All but the newest — `runs.resume` continues from it |
| `running` | None |

Never evicted: a state a waiting or running step job names (an input or `overrides.resumeFrom`), an input of a
running pipeline, anything a registry version or a checkpoint references, a file another live artifact lists, and —
when backups are configured — anything the backup mirror does not hold yet. Directory artifacts share file blobs: a
blob is deleted only when no artifact that stays lists it, and only those blobs count in `bytesFreed`.

The request body (all optional): `runId`, `project` (slug or id), `olderThanDays`, `hashes` (exactly these; one
that may not be evicted answers `artifact-not-evictable`).

| Answer | Meaning |
| --- | --- |
| `200` (dry run) | `artifacts` (with the reason each is evictable), `kept` (with the reason each stays), `bytesFreed`, `blobs`, `permanent` |
| `202 {approvalId}` | Every real call, for the admin too: eviction deletes data, so a person approves it in Approvals |
| approval result `202 {jobId}` | The approved replay queued the eviction job; its result is the plan with the bytes actually freed |

The job marks the rows evicted and emits `artifact.evicted` on `entity.artifact.{hash}`, deletes the blobs, and
writes an audit entry (`artifacts.evict`, `detail`: artifacts, `bytesFreed`, blobs). Running it again finds nothing
left to do. Agents may not evict (the default preset's `agents-never-evict` rule).

## Commands

- `artifacts.evict?dryRun=true` — the plan, nothing changes.
- `artifacts.evict` — asks for the approval; `approvals.approve` runs it.
- `artifacts.get` — one artifact, with `evicted` once its blobs are gone.
- `backups.new` — a backup now, so the mirror holds what you are about to evict.

## Playbooks

- **Free space after a training stage.** `artifacts.evict?dryRun=true` with `{"runId": "run_…"}`; read `kept`; send
  it for real; approve it in Approvals; follow the job.
- **Restore an evicted artifact.** The backup mirror (`CADENCE_BACKUP_DIR/cas/`) never prunes. Copy each blob back
  to the same relative path under the content store (`b3/<first two hex>/<64 hex>`; for a directory, its manifest
  and every file listed by `artifacts.get`), then restart the control plane: at start it clears the eviction of
  every artifact whose blobs verify again. Producing the same artifact again clears it too.
- **No backups configured.** The dry run says `permanent: true`: an eviction cannot be undone.
- A step input naming an evicted artifact fails with `artifact-missing`; restore it first.

## Sources

- docs/spec/06-platform.md "Artifacts, metrics and logs" (Retention) and "Operations" (Backups).
- docs/spec/05-agents.md, Guardrails — no destructive data operation without an approval.
