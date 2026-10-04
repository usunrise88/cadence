---
title: Invitation invalid
summary: A reviewer's invitation link is unknown, expired or revoked; it signs nobody in.
contexts: [error:invitation-invalid, op:auth.accept, panel:triage]
---

## What this is

A `401 Unauthorized` problem of type `invitation-invalid`, answered by `auth.accept` when the invitation token in the
link (`/#invitation=cri_…`) does not open a batch:

- the link was mistyped or cut short;
- it expired — an invitation lasts `annotation.invitation_max_days` (14 days) at most and never past the batch's due
  date;
- it was revoked (`credentials.revoke`), or the batch froze, which revokes every invitation of the batch.

Failed attempts count toward the sign-in rate limit of the address, like failed passwords.

## Place in the loop

Annotation: the admin invites a reviewer to a batch; the reviewer opens the link and annotates in the Triage panel.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/invitation-invalid` |
| `status` | `401` |
| `annotation.invitation_max_days` | 14 — the longest an invitation lasts |

## Commands

- `invitations.new` (admin) — a new link for the same reviewer name; their earlier annotations stay theirs.
- `invitations.list` (admin) — the batch's invitations with their expiry, last use and revocation.

## Playbooks

- Ask the admin for a new link; nothing is lost, annotations belong to the reviewer, not to the link.

## Sources

- docs/spec/06-platform.md "Authentication and access" (reviewer invitations: a signed link, no password, expires
  when the batch closes, 14 days at most).
- RFC 9110 §15.5.2 401 Unauthorized; RFC 9457, Problem Details for HTTP APIs.
