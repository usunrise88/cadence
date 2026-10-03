# internal/transcriptions
Manual transcription tests and the live channel (R47–R50; docs/spec/06-platform.md "Media"; spike A5). Like
`internal/runs` and `internal/evals` it never names a model family: the live kind is the family descriptor's `live`
role (newest published version) and the card memory its `interactive` block.

- **`transcriptions.new`** (`Prepare` + `Open`, tag `media`, people only): one to three targets (checkpoint, model
  version or base model version) of one family, each with a latency profile (default `eval.primary_profile` when the
  family has it), a language (default the project's first locale; refused when the base model's `locale:` tags lack
  it) and a boost list (`lang/<locale>/boost/<file>.txt[@ref]`, rendered like an eval's); input `microphone`, `file`
  or `span` (an utterance's stored audio, ≤ `transcriptions.max_file_minutes`); telephony `{codec ulaw|alaw|none}`,
  pace `realtime|fast`, blind (lanes shuffled with crypto/rand; labels returned, the page hides them). Targets with the
  same weights share one job input (`model.<n>` / `base.<n>`); the reservation is `interactive.memoryMb` +
  `extraCheckpointMb` per further distinct model. Refused: an open session of the same person
  (`transcription-in-progress`, also a unique index), a used-up allowance (`transcription-allowance-exhausted`).
  `Open` records the session (`transcriptions`, migration 0026), queues River kind `live` whose args are a
  `steps.Spec` (`stepId: live`, `pipelineRunId: trs_…`, job kind `interactive`, priority
  `transcriptions.interactive_job_priority`) and mints a single-use ticket (SHA-256 kept, 60 s).
- **The job**: `Handle` (River kind `live` on the steps queue) awaits the step on the worker protocol like a pipeline
  step; `Granted` (the worker protocol's grant hook) gives each interactive lease a fresh `CADENCE_LIVE_TOKEN` (hash
  kept) and moves the session to `loading`; `finish` closes the record with the GPU time of its card leases (wall
  time, the allowance's meter) and tells a browser still on the socket.
- **The relay** (`relay.go`, `coder/websocket`): `ServeClient` (ticket, then upgrade) registers the session in the
  in-process hub before using the ticket; `ServeWorker` hands the worker's socket over or parks it (pinged before
  pairing, A5). While waiting the browser gets `waiting` (queued with position and reason, then loading) every poll.
  Paired, frames pass unchanged; `ping` is answered by the relay (`pong` source relay); the queue towards the worker
  is bounded (`relay_queue_messages`; it blocks the reader while unpaired, so a file uploads while models load, and
  closes 1013 after `backpressure_wait_s` once live); idle and the session cap (or the allowance left) send a fatal
  `transcription-limit` error, inject `{"type":"end"}` and close 4001/4002 after the summary (or the drain); a lost
  worker closes 4003, a job that ended before start 4004. Relay stats every 5 s and before the summary.
- **Sweep** (30 s): unused expired tickets, sessions whose job ended unseen, and sessions without a socket in this
  process are ended (job cancelled, record closed).
- **Limits** in `defaults.yaml` `transcriptions.*`; the allowance is `budgets.manual_test_gpu_hours_per_project_per_day`
  (interactive leases do not count against the project's GPU budget, `runs.ProjectGPUHours`).

The hub is in process: one control plane serves the live channel (v1 runs one). Tests: `transcriptions_test.go`
(message type, Origin, tokens, blind order, profiles, languages, hub pairing); `internal/server/
transcriptions_integration_test.go` (the whole path with a fake worker socket, limits, sweep, allowance, queue
order).
