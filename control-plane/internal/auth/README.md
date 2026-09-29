# internal/auth
The actor behind a request. Phase 0: every request is the fixed development actor `{kind: user, id: usr_admin, name: admin}` (seeded by migration 0001), injected by `Middleware`. Phase 1 replaces the middleware with session cookies and scoped tokens; handlers already read the actor from the context.
