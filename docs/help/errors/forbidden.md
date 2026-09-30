---
title: Forbidden
summary: Cadence knows who you are, but this credential does not reach what you asked for — another project, the registry, instance settings — or a browser request lacked the Cadence-Client header.
contexts: [error:forbidden]
---

## What this is

A `403 Forbidden` problem (RFC 9110 §15.5.4): the request is authenticated, but its scope does not cover it.

- A project-scoped API key or agent session token touched another project.
- A scoped credential called an instance-wide operation (`credentials.*`, `projects.new`, compute, secrets,
  policies): those are the admin's own session only.
- A credential without registry read called a registry operation.
- A key or agent tried to manage a second factor (`totp.*`), which belongs to a signed-in person.
- A browser request that changes state (POST, PUT, PATCH) came without the header `Cadence-Client: web`.

## Place in the loop

Every credential has a scope (docs/spec/06-platform.md, "Authentication and access"): the admin's session reaches
everything; a `cdk_` key reaches one project, registry read, or both; a `cst_` agent token reaches its session's
project and registry read under the project's permission preset. Nothing was written.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/forbidden` |
| `status` | `403` |
| `detail` | Which boundary the request crossed |

## Commands

- `me.get` — the actor this credential acts as.
- `credentials.list` (admin session) — every credential with its scope.
- `credentials.new` — a key with the scope you need.

## Playbooks

- Automation: create a key for the right project instead of widening one key to everything.
- Agents: stay in the session's project; an operation outside it is not retried with another credential.
- Custom browser clients: send `Cadence-Client: web` on mutations; with SameSite=Lax cookies this is the CSRF
  defence (a cross-site form cannot set custom headers).

## Sources

- RFC 9110, HTTP Semantics, §15.5.4 403 Forbidden.
- OWASP Cross-Site Request Forgery Prevention Cheat Sheet, "Custom Request Headers".
