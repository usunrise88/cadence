---
paths:
  - "api/**"
  - "control-plane/internal/api/**"
  - "control-plane/internal/mcp/**"
  - "web/src/api/**"
---
# API contract rules

- `api/openapi.yaml` is the only place an operation is defined. Every operation has `operationId: <entity>.<verb>`
  (vocabulary: docs/spec/10-ui-shell.md, "Verb vocabulary"), a one-line `summary`, and for mutations the `dryRun`
  query parameter and the `Idempotency-Key` / `If-Match` headers (shared components).
- Project-scoped resources live under `/projects/{p}/…`; registry resources under `/registry/…`; global config under
  `/compute`, `/secrets`, `/policies`, `/credentials`. Actions are `POST /{resource}/{id}:{verb}`.
- Long operations respond `202` with `{ jobId }`; gated operations respond `202` with `{ approvalId }`.
- Errors: `application/problem+json` (RFC 9457) with `type` = `https://cadence.local/help/errors/<slug>`; `412` carries `currentRev`.
- Events: every mutation writes an outbox row in the same transaction; topic per docs/spec/06-platform.md "Topic scheme".
- MCP tools are generated from the operations; curate only `description` text (in `x-cadence.toolDescription`), never the schema.
- After editing the contract: `make gen`, commit the generated code, add a contract test for the new operation.
