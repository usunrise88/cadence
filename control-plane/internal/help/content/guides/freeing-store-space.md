---
title: Freeing store space
summary: How artifacts.evict deletes superseded training states from the content store after a person approves it, how eval artifacts leave by age, what is never touched, and when an eviction is permanent.
contexts: [guide:freeing-store-space, entity:artifact, error:artifact-not-evictable, error:artifact-missing, error:artifact-evicted]
---

## What this is

Training states are the largest artifacts in the content store (7.66 GB each for the 0.6B model: one per pause,
window close and `state_every_minutes`) and are only ever read to resume a run. `artifacts.evict` deletes the blobs of
the ones nothing can resume from any more. The artifact's index row stays, marked **evicted** (when, by whom, the
job), so lineage still resolves: `artifacts.get` shows `evicted`, a directory's file list still answers, and its
content says *evicted*. Training states are not copied into the backup mirror (they are read only to resume), so an
eviction is permanent: the dry run always says `permanent: true`.

## Place in the loop

Train → the run finishes (or fails and you decide not to resume it) → dry-run the eviction → approve it → the job
frees the space. Training states never leave on their own. Eval artifacts do: they are kept by age (below).

**Eval artifacts by age** (owner decision 2026-10-03). An eval record's per-utterance artifacts — `hypotheses`,
`scores` and the `metric_scores` beside it (≈ 280 MB and 27 MB for a 76-cell eval) — are evicted
`eval.artifact_retention_days` (30) days after the record's **last use**: when it was computed, or the newest eval that
linked it from the cache. A daily sweep (`evalArtifacts.retention`) queues the same eviction job, without an approval:
the owner set the policy, and with a backup mirror an artifact goes only once the mirror holds every blob of it, so
the eviction can be undone. The record (its summary), each cell's delta and every gate verdict stay for ever. Never
evicted by age: what a registered model's eval links (a model version's `evalId`), what an unfinished eval links, and
anything the checks below protect. After the eviction the Eval report shows the cell's note instead of its worst
utterances, a gate at the eval's own significance reuses the stored deltas (another significance turns the check
inconclusive: "per-utterance scores evicted (older than 30 days); re-run the eval"), `words.get` answers
`410 artifact-evicted`, and `evals.new` computes the cell again (the record takes the new artifacts). The backup
mirror keeps what it copied: it is not pruned when the store evicts (it has its own set retention,
`backups.keep_nightly` and `backups.keep_weekly`, for the database dumps only).

**Spectrogram tile pyramids by last view** (phase 4 tail). The pyramids the control plane builds for long audio on its
first view (`media.spectrogram`, ≈ 185 MB per hour of audio per channel) are a view cache: a daily sweep
(`mediaTiles.retention`) evicts those not viewed for `media.tiles_retention_days` (14), as the system actor and
without an approval, for good — the backup mirror is not waited for, because the next view of the audio builds the
pyramid again (*Building the spectrogram…*). Pyramids a pipeline step wrote are not taken. The audit entry carries
`tilesRetentionDays`.

## Fields and defaults

What is evictable (v1: type `training-state` only):

| Run state | States evicted |
| --- | --- |
| The pipeline run finished (`done`) | All of them |
| `failed` or `cancelled` | All but the newest — `runs.resume` continues from it |
| `running` | None |

Never evicted: a state a waiting or running step job names (an input or `overrides.resumeFrom`), an input of a
running pipeline, anything a registry version or a checkpoint references, and a file another live artifact lists. Directory artifacts share file blobs: a
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

- Settings → **Content store** shows the disk and this plan and sends the eviction (admin).
- `artifacts.evict?dryRun=true` — the plan, nothing changes; it also reports the store's disk (`disk`).
- `artifacts.evict` — asks for the approval; `approvals.approve` runs it.
- `artifacts.get` — one artifact, with `evicted` once its blobs are gone.

## Playbooks

- **The disk runs low.** Below `cache.store_low_free` (15 %) free, Cadence sends a failure notification (in-app and
  Telegram, at most once a day while it stays low) with what an eviction would free. Open Settings → Content store,
  press **Evict…**, approve it.

- **Free space after a training stage.** `artifacts.evict?dryRun=true` with `{"runId": "run_…"}`; read `kept`; send
  it for real; approve it in Approvals; follow the job.
- **An evicted state cannot be brought back.** Only a state nothing resumes from is evicted; a run that needs to go on
  continues from a checkpoint (`runs.stage`). If a copy of the blobs exists anyway, copy them back to the same path
  under the content store (`b3/<first two hex>/<64 hex>`) and restart the control plane: at start it clears the
  eviction of every artifact whose blobs verify again. Producing the same artifact again clears it too.
- A step input naming an evicted artifact fails with `artifact-missing`; restore it first.
- **An eval's utterances are gone.** Its artifacts passed `eval.artifact_retention_days`: run the eval again
  (`evals.new`); the cells compute again and the old eval's rows come back too. To keep an eval's rows, register the
  model it gated; to keep everything longer, raise the default.

## Sources

- docs/spec/06-platform.md "Artifacts, metrics and logs" (Retention) and "Operations" (Backups).
- docs/spec/05-agents.md, Guardrails — no destructive data operation without an approval.
