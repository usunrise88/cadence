# internal/credentials
The one table of credentials (migration 0002) and the admin account's secrets.

- Kinds: `session` (browser, cookie `cws_…`, 30-day sliding), `api_key` (`cdk_…`, one project and/or registry read),
  `agent` (`cst_…`, one project + registry read + preset; dies with its agent session), `agent_host` (`cah_…`),
  `egress_proxy` (`cep_…`), `worker` (`cwk_…`, one per compute host, the host name as its subject: the worker
  protocol only; `NewWorkerToken`, `EnsureWorkerTokenFile` for `CADENCE_WORKER_TOKEN_FILE`) and `invitation`
  (later phases). Only `sha256(token)` is stored; a token is shown once. `last_used_at` is written at most once a
  minute.
- For other packages: `MintAgentToken(ctx, tx, sessionID, projectID, preset) (token, id, err)` and
  `Revoke(ctx, tx, id)` (no event; the caller's own events carry the change). `RevokeAt` is the audited
  `credentials.revoke` command (If-Match, `credential.revoked` event); `NewAPIKey` emits `credential.created`.
- Actors: a session acts as its user; an API key as `{kind: automation, id: crd_…, name: <key name>}`; an agent
  token as `{kind: agent, id: crd_…, sessionId: <agent session id>}`.
- `Store` resolves tokens for the auth middleware and runs sign-in: `Setup` (first start, once), `Login` (password,
  then TOTP when on; `totp-required` asks for the code), TOTP enroll/confirm/disable, and `ResetPassword` for
  `cadence admin reset-password`. Account changes emit `user.password_set`, `user.totp_enabled|disabled` on
  `entity.user.{id}`.
