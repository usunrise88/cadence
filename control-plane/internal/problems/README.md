# internal/problems
The single registry of error types (slug, status, title) and their RFC 9457 `application/problem+json` rendering. `type` is `https://cadence.local/help/errors/<slug>`; every type needs `docs/help/errors/<slug>.md` (a test in internal/help enforces both directions). Errors that are not `*problems.Error` render as `internal` and are logged, never echoed.
