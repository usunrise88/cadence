---
paths:
  - "control-plane/**"
---
# Go rules (control plane)

- Go 1.27; packages under `internal/` by concern (`api`, `events`, `jobs`, `mcp`, `storage`, `projects`, `registry`, `auth`, `notify`).
- Generated code from oapi-codegen (strict server) is committed and never edited by hand.
- Postgres through `pgx`; every mutation runs in a transaction that also inserts the outbox event; use River for jobs (one training slot per card).
- Entities carry `rev`; writes check `If-Match` and return `412` with `currentRev` on mismatch.
- Commands take an `Actor` (user, agent session, automation) and an idempotency key; repeat keys return the original result.
- Secrets are read from the environment / compose secrets once at start and never logged or returned by any endpoint.
- Migrations: forward-only SQL in `control-plane/migrations`, expand-and-contract; run at start under an advisory lock.
- Tests: table-driven; integration tests use a throwaway Postgres (testcontainers); no test touches a GPU or the network.
- Logging: structured JSON lines (`log/slog`), one line per command with actor and `causedBy`.
