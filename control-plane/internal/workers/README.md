# internal/workers
The worker protocol (docs/review/2026-09-30-phase-2-plan.md "Worker protocol", R14) and the step queue.

- **Register** (`workerRegistrations.new`): the runtime (`runtime/<name>`), each model family (`model-family/<name>`)
  and each step kind (`step-kind/<name>`, payload = descriptor + name, runtime, runtimeVersionId, schemaHash) become
  frozen registry versions; equal content is reused, so a restart registers nothing new. Refused: parameters without
  complete `x-cadence` (validation-failed), a framework kind another runtime publishes, a neutral kind with another
  schema (`step-kind-conflict`). One worker row per runtime and host; a new `instance` reaps the old process's leases. Workers registering for the
  first time at once race on these rows: the handler reruns the loser's transaction (`storage.BeginRetry`, unique
  violation or deadlock), which then finds the winner's rows, so every racer gets its answer.
- **Await** (`steps.Leases`): the `step` job's handler (stream P) calls it; the job enters `step_jobs` (spec from
  the River args) and Await blocks until the step ends — released, reaped (`lost`) or cancelled. Pause and window
  close requeue the job in place with `overrides.resumeFrom` = its `training-state` output. Calling Await again
  after a restart resumes; an ended job answers its stored outcome. Register the `step` kind on
  `jobs.QueueSteps` with a long timeout.
- **Live sessions** (phase 3 · stream T): Await also takes River kind `live` (`steps.LiveJobKind`, a transcription
  session's interactive job, internal/transcriptions); `OnGranted` hooks (several, in order) let that package add a
  fresh live token to an interactive lease's secret env, and internal/serving name a served step's staging target and
  count its lease (phase 5; the claim reads a lease's served model from `serving_leases` for the queue's reserve).
- **Claim** (long-poll ≤ 30 s): waiting jobs whose `kind@version` the worker published, not paused or cancelled,
  interactive jobs first (R49), then by project queue priority, job priority, then FIFO, read in keyset pages of 50 (at most 20 per attempt) until one
  fits, so jobs that cannot fit never hide one further down; card slot rows are locked in card index order (the
  claim's FOR UPDATE and the telemetry upserts of claims and reports alike, `byCardIndex`, so concurrent claims
  never deadlock) and `internal/queue` decides the fit. The
  answer carries the spec, `cas://b3:…` inputs, the card and its memory cap, secret env (from `internal/secrets`,
  never stored or logged; a secret whose scope does not allow the step's project fails the step with `input`), a traceparent and `heartbeatSeconds` 10.
- **Report** (heartbeat): progress → `job.progress`, telemetry → `card_slots`, `gpu` (≤ 1 event / 5 s per host) and
  compute health; answers stop with `cancelled | paused | window-closed`. **Reap** (periodic, 10 s in main): three
  missed beats end the step as failed/`lost`; hosts whose workers went quiet become `unreachable`.
- **Logs**: NDJSON appended to `<LogDir>/<jobId>.ndjson` (`$CADENCE_DATA_DIR/job-logs`), one `job.{id}.log` event
  per batch (≤ 200 lines). An oversize line never refuses the batch: a line over 64 KiB is stored truncated (msg
  cut, fields dropped if needed, `[truncated: the line had N bytes]` appended; text that is not a valid line becomes a
  `warn` line stamped on arrival) and bytes past 4 MiB per request are discarded, so later lines still land. `ReadLogs` for `jobLogs.list`, `PruneLogs` (14 days, daily). **Metrics**:
  `internal/telemetry` rows plus `run.{runId}.metrics` events. **Release**: outputs must be in the CAS
  (`artifact-missing`); `PutArtifact` backs `workerArtifacts.set` (`artifact-hash-mismatch`).
- **Queue commands**: `Pause`, `Resume`, `Prioritize` (jobs.pause|resume|edit) and `Queue` (queueEntries.list);
  queue changes go out on the `queue` topic (`queue.changed` with `change`).
