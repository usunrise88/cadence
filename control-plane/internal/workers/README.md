# internal/workers
The worker protocol (docs/review/2026-09-30-phase-2-plan.md "Worker protocol", R14) and the step queue.

- **Register** (`workerRegistrations.new`): the runtime (`runtime/<name>`), each model family (`model-family/<name>`)
  and each step kind (`step-kind/<name>`, payload = descriptor + name, runtime, runtimeVersionId, schemaHash) become
  frozen registry versions; equal content is reused, so a restart registers nothing new. Refused: parameters without
  complete `x-cadence` (validation-failed), a framework kind another runtime publishes, a neutral kind with another
  schema (`step-kind-conflict`). One worker row per runtime and host; a new `instance` reaps the old process's leases.
- **Await** (`steps.Leases`): the `step` job's handler (stream P) calls it; the job enters `step_jobs` (spec from
  the River args) and Await blocks until the step ends — released, reaped (`lost`) or cancelled. Pause and window
  close requeue the job in place with `overrides.resumeFrom` = its `training-state` output. Calling Await again
  after a restart resumes; an ended job answers its stored outcome. Register the `step` kind on
  `jobs.QueueSteps` with a long timeout.
- **Claim** (long-poll ≤ 30 s): waiting jobs whose `kind@version` the worker published, not paused or cancelled,
  by job priority then FIFO; card slot rows are locked in index order and `internal/queue` decides the fit. The
  answer carries the spec, `cas://b3:…` inputs, the card and its memory cap, secret env (from `internal/secrets`,
  never stored or logged; a secret whose scope does not allow the step's project fails the step with `input`), a traceparent and `heartbeatSeconds` 10.
- **Report** (heartbeat): progress → `job.progress`, telemetry → `card_slots`, `gpu` (≤ 1 event / 5 s per host) and
  compute health; answers stop with `cancelled | paused | window-closed`. **Reap** (periodic, 10 s in main): three
  missed beats end the step as failed/`lost`; hosts whose workers went quiet become `unreachable`.
- **Logs**: NDJSON appended to `<LogDir>/<jobId>.ndjson` (`$CADENCE_DATA_DIR/job-logs`), one `job.{id}.log` event
  per batch (≤ 200 lines), `ReadLogs` for `jobLogs.list`, `PruneLogs` (14 days, daily). **Metrics**:
  `internal/telemetry` rows plus `run.{runId}.metrics` events. **Release**: outputs must be in the CAS
  (`artifact-missing`); `PutArtifact` backs `workerArtifacts.set` (`artifact-hash-mismatch`).
- **Queue commands**: `Pause`, `Resume`, `Prioritize` (jobs.pause|resume|edit) and `Queue` (queueEntries.list);
  queue changes go out on the `queue` topic (`queue.changed` with `change`).
