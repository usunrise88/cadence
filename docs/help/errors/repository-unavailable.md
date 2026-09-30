---
title: Repository unavailable
summary: The project repository could not be read or written (git failed, or the remote refused); nothing was committed.
contexts: [error:repository-unavailable, panel:recipe, panel:agent-settings, panel:project]
---

## What this is

A `502 Bad Gateway` problem of type `repository-unavailable`: a command that commits to the project repository —
`agentProfile.edit`, `projects.edit`, `projects.note`, `projects.sync`, `branches.accept` — could not complete its
git step. The command was rolled back: its database change and its events were not written. The `detail` carries
git's own message.

Pushes to a project's remote (GitHub or a linked repository) are different: they never fail a command. `main` in
Cadence is the truth; when the push after a commit fails, the project's `repository.pushError` says why and the next
commit pushes again.

## Place in the loop

Every project has one repository (docs/spec/08-resolutions.md R10): a bare repository on the control plane,
served at `/git/<slug>.git`, with a working clone the server commits through. People and agents change recipes by
committing there; everything the UI shows about recipes is read from it.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/repository-unavailable` |
| `status` | `502` |
| `detail` | What git answered |

## Commands

- Retry the same command with a new `Idempotency-Key`; a failed command stored nothing under its key.
- `projects.get` — `repository.pushError` shows the last failed push to the remote.
- `recipes.list` — checks that the repository answers at all.

## Playbooks

- "No space left on device": the control plane's data volume is full; free space, then retry.
- A push error naming authentication: the stored token (`repository.secret`) expired or lost access; store a new one
  with `secrets.new` under the same name.
- A push error naming a non-fast-forward: someone pushed to the remote directly; Cadence never force-pushes. Bring the
  remote's change into Cadence (push it to `/git/<slug>.git`), and the next commit mirrors again.
- Agents: report the detail to the person; do not retry more than once.

## Sources

- docs/spec/08-resolutions.md R10 — the internal repository and the GitHub mirror.
- RFC 9110, HTTP Semantics, §15.6.3 502 Bad Gateway; RFC 9457, Problem Details for HTTP APIs.
