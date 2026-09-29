---
title: Not signed in
summary: The request carries no valid session or token — sign in again, or send a live API key or agent token as Bearer.
contexts: [error:unauthenticated]
---

## What this is

A `401 Unauthorized` problem (RFC 9110 §15.5.2): Cadence does not know who is asking. One of:

- no session cookie and no `Authorization: Bearer` header;
- the session expired (30 days without use) or was signed out or revoked;
- the Bearer token is unknown, revoked or expired (`cdk_` API key, `cst_` agent session token);
- `auth.login` was sent a wrong username, password or TOTP code (the answer does not say which).

## Place in the loop

Every API operation except first start, sign-in and help needs a principal (docs/spec/06-platform.md,
"Authentication and access"). Nothing was read or written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/unauthenticated` |
| `status` | `401` |
| `detail` | What was missing or wrong |

A browser session lasts 30 days and slides forward with use; API keys last until revoked or their `expiresAt`;
agent session tokens die with their session.

## Commands

- `auth.get` — whether first start is pending and who, if anyone, is signed in.
- `auth.login` — sign in with username, password and, when enabled, a TOTP code.
- `credentials.list` (signed in) — which keys exist, when each was last used, which are revoked.

## Playbooks

- In the web UI: the sign-in screen appears by itself; sign in and continue where you were.
- Automation: check that the key was not revoked (`credentials.list?revoked=true`) and create a new one with
  `credentials.new` if it was; the token is shown only once.
- Agents: a `cst_` token that stops working means the session ended; do not retry — report and stop.
- Repeated failed sign-ins are throttled (`rate-limited`).

## Sources

- RFC 9110, HTTP Semantics, §15.5.2 401 Unauthorized.
- RFC 6750, OAuth 2.0 Bearer Token Usage, §2.1 (the `Authorization: Bearer` header).
