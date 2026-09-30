---
title: The cadence command line
summary: cadence <entity> <verb> — every API operation as a command, generated from the contract, with an API key and JSON output.
contexts: [guide:cli]
---

## What this is

The `cadence` binary that runs the control plane (`cadence serve`) is also a client of its API. Every implemented
operation is a command named like the operation: `projects.get` is `cadence projects get`, `projects.search` is
`cadence projects search`, `jobs.wait` is `cadence jobs wait`. The commands are generated from `api/openapi.yaml`,
so the CLI offers exactly what the API, the MCP tools and the UI commands offer — the same names, validation,
revisions, approvals and audit log. Sign-in and the user's own state (`auth` and `me`: workspaces, saved searches)
are not commands; use the web UI for those.

## Place in the loop

Scripts, CI jobs and people at a terminal use it instead of hand-written `curl`. A command sends one request with an
API key and prints the answer as JSON, so it pipes into `jq`. Agents inside Cadence sessions use the MCP tools
instead; the operations are the same.

## Fields and defaults

| What | Value |
| --- | --- |
| Server | `--url` or `CADENCE_URL`; default `http://127.0.0.1:8080`. `/api` is appended unless the URL ends with it |
| Credential | `--token` or `CADENCE_TOKEN`: an API key (`cdk_…`, from `credentials.new`), sent as `Authorization: Bearer` |
| Parameters | One flag per path and query parameter, in kebab-case: `--project` for the project slug, `--dry-run`, `--limit` |
| Body | `--body '<json>'`, `--body @file.json`, or `--body @-` to read standard input; checked to be JSON before sending |
| Revisions | `--if-match 3` (or `'"3"'`, the ETag as read) on commands that change an existing entity |
| Idempotency | Each command call gets a fresh `Idempotency-Key`; `--idempotency-key` reuses one to replay a first result |
| Output | 2xx: the JSON body on standard output, exit 0 (a `202` prints its `jobId` or `approvalId`) |
| Errors | The server's `application/problem+json` on standard error after one line naming the help article, exit 1 |
| Usage | A missing required flag or body, an unknown flag, a bad integer or enum value: the command's help, exit 2 |

## Commands

- `cadence help` lists the entities and their verbs; `cadence help <entity>` lists one entity's verbs with their
  flags; `cadence <entity> <verb> --help` describes one command's flags and body properties.
- `cadence help get --id <article>` and `cadence help search --q <text>` are the help entity's own commands.
- `cadence serve`, `cadence admin reset-password` and `cadence version` are the hand-written commands of the binary.

## Playbooks

- **Script against a project.** Create a key scoped to the project in the web UI (or `cadence credentials new
  --body '{"name":"ci","scope":{"project":"hebrew"}}'` with an admin key), then
  `export CADENCE_URL=http://cadence:8080 CADENCE_TOKEN=cdk_…` and run, for example,
  `cadence projects search --project hebrew --q 'kind:job status:failed updated:>2026-09-01'`.
- **Change an entity safely.** Read it (`cadence projects get --project hebrew`), take its `rev`, send it:
  `cadence projects edit --project hebrew --if-match 4 --body '{"name":"Hebrew telephony"}'`. A
  `precondition-failed` answer means someone changed it since: read again and retry.
- **See before doing.** Add `--dry-run` to any command: the answer is what would happen; nothing is written.

## Sources

- Cadence recommendation: `docs/spec/08-resolutions.md` R34 (the CLI is generated from the contract, same binary).
- RFC 9457, Problem Details for HTTP APIs.
