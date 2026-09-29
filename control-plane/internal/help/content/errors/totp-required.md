---
title: TOTP code required
summary: The password was right and this account has a second factor — send the six-digit code from the authenticator app.
contexts: [error:totp-required]
---

## What this is

A `401 Unauthorized` problem answered by `auth.login` when the username and password are correct, the account has
TOTP turned on, and no `totpCode` was sent. It is the second step of sign-in, not a failure: it does not count
against the sign-in rate limit.

## Place in the loop

Sign-in for the admin (docs/spec/06-platform.md, "Authentication and access"): password (Argon2id) plus optional
TOTP (RFC 6238). No session was created.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/totp-required` |
| `status` | `401` |

Codes are six digits, 30-second steps, HMAC-SHA1; one step of clock drift either way is accepted, and each code
works once.

## Commands

- `auth.login` again with the same username and password plus `totpCode`.
- `totp.enroll`, `totp.confirm`, `totp.disable` — turn the second factor on or off while signed in.

## Playbooks

- The web sign-in form shows a code field when this answer arrives; type the newest code.
- A code that keeps failing: check the phone's clock (automatic time).
- Lost phone: on the host, `cadence admin reset-password --disable-totp` sets a new password and turns TOTP off.

## Sources

- RFC 6238, TOTP: Time-Based One-Time Password Algorithm, §4 and §5.2 (validation window, one use per step).
- RFC 4226, HOTP, §5.3 (six-digit truncation).
