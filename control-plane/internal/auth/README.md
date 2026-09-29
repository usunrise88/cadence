# internal/auth
Who is asking and what they may reach (docs/spec/06-platform.md "Authentication and access"). No database here.

- `Actor` (the contract's Actor) and `Scope` (`All`, `ProjectID`, `RegistryRead`, `Preset`) ride in the request
  context: `FromContext`, `ScopeFromContext`, `PrincipalFromContext`. Handlers check with `CheckProject(ctx, id)`,
  `CheckRegistryRead(ctx)` and `CheckAll(ctx)` (instance-wide operations are the admin's session only); they fail
  with `forbidden` (403) or, without a principal, `unauthenticated` (401).
- `Authenticator.Middleware` resolves `Authorization: Bearer cdk_…|cst_…` or the `cadence_session` cookie through a
  `Resolver` (the credentials store), slides the session cookie, clears stale ones, and applies the CSRF rule: a
  POST/PUT/PATCH not authenticated by Bearer needs `Cadence-Client: web`. With `Fixed` set (the server's
  `Config.Actor`) every request is that actor with full scope, unauthenticated — tests and development. A request
  whose context already carries a principal (`WithPrincipal`: an approval replayed in process, an MCP tool call)
  passes through as that principal; requests from the network never carry one.
- Primitives: Argon2id password hashes (PHC strings, RFC 9106 parameters), RFC 6238 TOTP with replay protection,
  opaque tokens (`cdk_`, `cst_`, `cwk_`, `cws_` + 256 random bits) stored as SHA-256, the in-memory login `Limiter`
  (5/min, 20/hour per address and per username).
- `ClientOf` trusts `X-Forwarded-For`/`X-Forwarded-Proto` only from loopback or private peers (the host's proxy, R39).
