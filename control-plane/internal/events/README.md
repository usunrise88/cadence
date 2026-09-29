# internal/events
Transactional outbox (same transaction as the change), dispatcher in seq order, SSE endpoint with Last-Event-ID resume, topic wildcard matching, projectId on every work event.

- `Append` inserts a command's events inside its transaction under `pg_advisory_xact_lock`, so commit order equals `seq` order (the dispatcher and resume rely on it). An insert trigger issues `NOTIFY cadence_events`.
- `Dispatcher` reads committed rows after its cursor on each notification (1 s poll fallback) and publishes them to the in-memory `Hub`; a subscriber that cannot keep up is dropped and resumes from the table.
- `Streamer` serves one SSE client: subscribe first, replay `seq > after` from the table, then follow the hub skipping what was sent. Frames are `id: <seq>` + `data: <CadenceEvent JSON>` (no `event:` line); `: ping` every 15 s. Without `after`/`Last-Event-ID` the stream starts at the current head.
- Topic patterns: comma-separated; a trailing `*` segment matches one or more segments (`run.123.*`); `*` alone matches all. `project=` keeps that project's events plus events without a projectId.
