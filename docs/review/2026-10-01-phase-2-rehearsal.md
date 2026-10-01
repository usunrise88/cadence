# Phase 2 gate rehearsal — 2026-10-01

Status: done. The phase-2 training path ran end to end on the staging card through the API and the generated CLI
(`cadence <entity> <verb>`, the same operations an agent calls over MCP), without an agent, on a private stack. It
found five bugs that block or break the gate. All five are fixed on `feat/phase-2-rehearsal` with tests, and four
larger findings are listed below for the orchestrator.

## Setup

| Part | What ran |
| --- | --- |
| Base | `feat/phase-2-training` at ae30c23 |
| Database | `postgres:17` container `cadence-rehearsal-pg` on 127.0.0.1:55471 |
| Control plane | The branch's binary (`go build ./cmd/cadence`, rebuilt after each fix) on 127.0.0.1:18471, with its data and content store under `~/cadence-rehearsal` |
| GPU worker | Image `cadence/worker:ae30c23` with `--gpus all --ipc=host --network host`, `CADENCE_WORKER_HOST=staging`, the content store and scratch on one mounted directory |
| Stand worker | `cadence-dev-worker-1` was stopped for the rehearsal and started again afterwards |
| Identity | Admin created with `auth.setup` (throwaway password). A `cdk_` key scoped to the project plus registry read drove the CLI. `sources.edit` and `recipes.new` used the admin session. |
| Compute | A fresh database seeds `staging`: one card, `blackwell-48gb`, 48 GB, cap 22 GB, all job kinds (checked with `compute.list`) |

Where the setup departed from the stand:

- The worker ran as uid 1000, not 65532. The control plane ran on the host as uid 1000, so both sides of the store
  had one owner, as on the stand.
- The pack fix (bug 2) was bind-mounted read-only into the image over `cadence_nemo/steps/{calibrate,finetune}.py`.
  The image itself was not rebuilt.
- The base model came from a read-only Hugging Face cache (`CADENCE_HF_READONLY_CACHES`) that already held the
  `.nemo` at the pinned revision. The host's `~/.cache/huggingface` holds a token file and was not mounted.
- FLEURS `he_il` was downloaded into the worker's `HF_HOME`: 5.3 GB (the whole configuration). It was deleted
  afterwards.
- The content store never deletes in v1 and a training state is 7.66 GB. To stay above 25 GB free, training states
  were deleted once nothing needed them. Free disk went from 72 GB to a low of 26 GB, then back to 38–57 GB.

## Run and numbers

Every row is one or more API operations. Times are wall-clock times taken from the API or the job logs.

| # | Operations | Result |
| --- | --- | --- |
| 1 | `projects.new` (dry run, then real; he-IL, default base model) → `jobs.wait` | Project `rehearsal` is active, with the internal repository and the default base model `base-model/nemotron-3.5-asr-streaming-0.6b` |
| 2 | `pipelines.run import` (dry run, then real) with `{import: {max_hours: 3}}` | 84 s including the 5.3 GB download. `dataset/fleurs-he` has 1012 clips: train 993 clips / 2.947 h, validation 19 / 0.053 h (`validation_share` 0.02, speaker-disjoint falls back to distinct transcripts). The source `fleurs` is eval-only. |
| 3 | `sources.edit {trainingCleared: true}` (dry run, then real) as admin | The source is cleared and its version becomes trainable without a re-import |
| 4 | `mixes.new` with one group `target` (dry run, then real) | Mix `fleurs-he-only` rev 1, 2.947 h. No replay group: the replay corpus is not imported on this instance, so `replayShare` is 0. |
| 5 | `runs.calibrate` (dry run, then real) | Hit bugs 1 and 2. After the fixes: lease 106 s. Calibration measured 0.3774 s/step ± 0.024 and bucket batches 40/21/12/6/3/1 for ≤ 4/6/8/10/14/20 s, with 60 s of audio per step. Peak 22 048 MiB in nvidia-smi under the 22 528 MiB cap. |
| 6 | `runs.new?dryRun=true` (300 steps) | `basis: measured`, ± 2.84 %, 113 s, 0.031 GPU-h, within the 8 GPU-h/day budget |
| 7 | `runs.new` (600 steps, `val_every` 200) | The train step started. `metrics.get` returned loss, lr, grad_norm, throughput, gpu_memory_mb and val_wer. `queueEntries.list` showed the lease on card 0 with a 22 528 MiB cap. `jobLogs.list` returned 1336 lines. |
| 8 | `jobs.pause` at step ≈ 170 | The worker saw the stop 4 s later. The training state (7.66 GB) was written in 14.1 s at step 209, after a stop validation. The lease ended cancelled 43 s after the pause. The queue showed `paused` with `resumeFrom`. Bug 3 started here. |
| 9 | `jobs.resume` | Leased again 3 s later. Model and state restore took 80 s, then training resumed at step 210 and finished 600. val_wer: 0.401 @200, 0.391 @209 (stop), 0.410 @400, 0.401 @600. One checkpoint was registered (step 600, 0.4006). 0.168 GPU-h. |
| 10 | `runs.stage` from ckp@600 (peak lr 1e-4, 300 steps, `val_every` 100) | Calibration ran again (a different start model) in 86 s. A control-plane restart during training (bug 4) made it train twice. Its seed-0 runs are deterministic, so both attempts gave val_wer 0.366 @100, 0.350 @200, 0.382 @300. Two checkpoints: best @200 (0.3502) and last @300 (0.3817). |
| 11 | `checkpoints.average` over the stage's two checkpoints (dry run, then real) | 20 s, CPU only. The `averaged` checkpoint has `averagedFrom` and `valWer` null (the step does not measure one). |
| 12 | `runs.new` (400 steps), then restart the control plane at step ≈ 100 with the bug-4 fix | The same job and attempt 1 continued: no "lost" sweep, River snoozed and re-picked the job. Done in 6 min 08 s wall. val_wer 0.401 @200, 0.382 @400. One checkpoint registered. 0.102 GPU-h. |
| 13 | `pipelines.run import` (test split, 20 clips, `eval_only`, `all-test`), `recipes.new pipelines/transcribe.yaml` (admin), `pipelines.run transcribe` ×3 at 160ms | Hit bug 5. After the fix, about 70 s each including model load; two eval leases (8 GB each) shared the card. Streaming WER on 20 held-out test clips (NFKC, lower-cased, no punctuation): **0.487** best @200, **0.493** last @300, **0.522** averaged |

Training speed: the train step ran at 0.53–0.56 s per optimiser step, including one validation of 19 clips per
200 steps and the 10-step metric cadence. That is 213 s for 400 steps in run 12 and 219 s for steps 210–600 in run 9.
Each lease also adds about 75–80 s of model (and state) restore before step 1, and about 45 s at the end: the final
validation, the state save (13–15 s) and the `.nemo` saves. In nvidia-smi, the step processes peaked at
21 288–22 026 MiB under the 22 528 MiB cap; PyTorch's own `maxAllocatedMb` was about 9.9 GB, and the rest is the
caching allocator. **vLLM was untouched:** it held 23 808 MiB in every one of 1471 two-second samples, and all 99
health checks (`/v1/models`) answered 200.

## Gate evidence (the agent's playbook session)

The phase-2 gate run (ROADMAP "Phase 2" gate paragraph), recorded here so the numbers live beside the rehearsal:

| What | Value |
| --- | --- |
| Session | Claude Code (`sonnet`), playbook "Fine-tune from a dataset version", project `test`, started from an API key allowed to run sessions |
| Data | `dataset/fleurs-he` (FLEURS he_il via `pipelines/import`): 9.18 h train, 101 validation clips; source cleared for training through an approval |
| Calibration | `oomptimizer_calibrate`: 0.491 s/step, base batch 13 |
| Estimate shown first | playbook dry run 0.19 GPU-h (budget 8 GPU-h/day); `runs.new` 300 steps, basis measured, 0.085 GPU-h with lease overhead |
| Actual | 0.090 GPU-h, 5.4 min |
| Result | one checkpoint at step 300, validation WER 0.379 (top-k kept) |
| Memory | peak 21.5 GB under the 22 GB cap; vLLM untouched (23.8 GB) |
| A3 status | training, streaming eval and ONNX parity (0.00 points) met; Triton ≈ 2 real-time streams (phase 5) |

## Bugs found and fixed (on this branch)

1. **runs.calibrate / runs.new returned 500 for every imported dataset.** The dataset hook registers
   `payload.artifact` as an artifact reference (`{hash, type, size}`, matching the contract's
   `DatasetVersion.artifact`). `runs.RenderMix` decoded it as a string. The fixture helper `RegisterDataset` wrote a
   bare string, so the tests never saw the real shape. Fix: `runs` accepts both shapes, and the helper and the
   integration test write the hook's shape. Unit test: `TestDatasetArtifactDecode`.
2. **Calibrate and fine-tune waited in the queue forever on the staging card.** The NeMo kinds declared
   `memoryGb: 24`, the spec's old cap, and a 24 GB reservation never fits under the 22 GB cap. The conformance run
   bypasses the scheduler, so it never showed. Fix: the two kinds declare no `memoryGb`, so they reserve the card's
   whole remaining cap and take the card alone (06 "Worker protocol"). Test in `test_nemo_pack.py`. **The stand
   needs the worker image rebuilt**: the image at ae30c23 publishes the 24 GB kinds, so stand training would queue
   forever too.
3. **A run stayed `queued` while training after `jobs.resume`.** A requeued job leased again keeps its pipeline step
   `running`, so `Engine.Leased` returned early and the run observer never re-derived the status. Fix: a lease of a
   step that is already running calls the observer. Integration test: `TestReleaseOfRunningStepCallsTheObserver`.
4. **A control-plane restart during a step lost it and trained again from scratch.** On shutdown, River cancelled the
   step handler, the job was discarded, and the startup sweep failed the step as lost and retried it as attempt 2.
   Meanwhile the worker's lease trained to the end, and its outcome (two checkpoints) was dropped. Fix: a handler cut
   by `context.Canceled` returns `jobs.ErrInterrupted`, and the jobs service answers `river.JobSnooze(0)`. The job
   keeps its attempt, its mirror stays `running`, and the next start waits for the same lease (`Await` already
   re-attaches). Timeouts still fail. Integration test: `TestStepSurvivesAControlPlaneRestart` (it fails without the
   fix with "job failed, river job discarded"). Checked live in row 12.
5. **`pipelines.run` with `{hash, type}` inputs answered "declared 0 bytes", although the dry run passed.**
   `checkpoints.list` and `datasets.get` give no size, and size is optional in the contract. Fix: `Start` fills a
   missing size and meta from the artifact index. An unindexed blob without a size is still refused. Test: in
   `TestStartValidatesInputsAgainstTheStore`.

## Findings not fixed (for the orchestrator)

- **A run registers at most two checkpoints.** The NeMo train kind outputs `checkpoint` (last) and
  `checkpoint_best` (the best of *this lease*). So `training.keep_top_k` = 3 and "average the top k" have only one
  or two checkpoints per run to work with, and after a pause or a window close the pre-pause best is lost: run 9's
  best was 0.391 @209, but `checkpoint_best` is @600. Per-validation checkpoints would need a multi-checkpoint output
  (a directory of checkpoints, or the hook reading several). That changes the step contract, so it is not a patch.
- **Measured estimates are about 2× low for short runs.** The calibration's 0.377 s/step is compute only. Real steps
  take 0.53–0.56 s, and every lease adds about 2 min of fixed restore and save time. Run 12: estimate 192 s, lease
  6 min 08 s. The table row (0.7 s/step) is closer. Suggestion: the estimate adds the per-lease overhead and
  validation, or scales by a measured loop/compute ratio.
- **`jobs.pause` races progress updates.** A running step job's `rev` goes up with every progress report (about
  every 10 s; it reached rev 27 in 3 min), so a read-then-pause got 412 once. Agents will hit this often. Suggestion:
  progress should not bump `rev`, or pause and resume should accept the run's etag.
- **Validation WER is computed on tokenized references.** Characters missing from the vocabulary (`(`, `)`) come back
  as `⁇`, which inflates val_wer a little. Also, with `validation_share` 0.02, a 3 h import leaves only 19
  validation clips, so val_wer moves in coarse steps (0.350 → 0.382 is three words).

Also observed, all working as designed: the run reads `paused` at once while the job stays `running` with
"paused; waits in the queue" until the worker releases. `runs.stage` calibrates again (it starts from another
model). `recipes.new` is denied to automation keys by the default preset. `sources.list` returns `datasets: []` (by
contract, `get` only). Metrics of a retried attempt append to the same series, which here gave duplicate points per
step.

## Cleanup

The rehearsal worker and the control plane were stopped, the Postgres container was removed, and
`~/cadence-rehearsal` was deleted. `cadence-dev-worker-1` was started again (it was idle before
and after). vLLM was at 23 808 MiB and answering 200 at the end.
