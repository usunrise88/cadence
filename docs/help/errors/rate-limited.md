---
title: Too many attempts
summary: Too many failed sign-ins from this address or for this username — wait for Retry-After seconds, then try again.
contexts: [error:rate-limited]
---

## What this is

A `429 Too Many Requests` problem (RFC 6585 §4) from `auth.login`. Failed sign-ins are counted per client address
and per username; over the limit, Cadence refuses before it checks any password. The `Retry-After` header says how
many seconds to wait.

## Place in the loop

Login throttling (docs/spec/06-platform.md, "Authentication and access"): the admin account is the only way in,
so guessing its password is throttled. A successful sign-in or a `totp-required` step does not count.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/rate-limited` |
| `status` | `429` |
| `detail` | How long to wait |
| `Retry-After` header | Seconds until the next attempt is accepted |

Limits: 5 failed attempts per minute and 20 per hour, each per address and per username (Cadence recommendation).
Counts live in memory: a restart of the control plane clears them.

## Commands

- `auth.login` again after `Retry-After` seconds.
- On the host: `cadence admin reset-password` if the password itself is lost.

## Playbooks

- In the web UI: the sign-in form says how long to wait; do not keep retrying.
- An unexpected lockout means someone else is guessing: check the control plane's log for `sign-in failed` lines
  and their addresses, and keep the instance behind the proxy (R39).

## Sources

- RFC 6585, Additional HTTP Status Codes, §4 429 Too Many Requests.
- RFC 9110, HTTP Semantics, §10.2.3 Retry-After.
- OWASP Authentication Cheat Sheet, "Login Throttling".
