---
title: Auxiliary service unavailable
summary: A running service an auxiliary model names (auxiliary/oasis) does not answer, so the pipeline that uses it does not start or its step fails; Cadence never starts the service.
contexts: [error:auxiliary-unavailable, step:oasis_transcribe, registry:auxiliary]
---

## What this is

A `503 Service Unavailable` problem of type `auxiliary-unavailable`. Some auxiliary models are not weights a step
loads but a service already running on a host — `auxiliary/oasis`, whose payload names
`service: {kind: grpc-asr, endpoint, protocol: oasis.v1}`. Cadence never starts, stops or restarts such a service.
It checks it in two places:

| Where | Check | What you see |
| --- | --- | --- |
| `pipelines.run` (the dry run included), `playbooks` steps that start a pipeline | A step parameter names the auxiliary (x-cadence `registryRef`); the control plane opens a TCP connection to `service.endpoint` within 3 s | This problem; nothing was queued — the `oasis` member of `pipelines/pseudo-label` is required (the ensemble votes Whisper and OASIS). For a step marked `optional` in a pipeline of your own only a plan warning with code `auxiliary-unavailable`: the run starts, the step fails and the run goes on without it |
| `auxiliaries.get` | The same connection, for a service auxiliary | `reachable: false` (the "Adapt a new language" playbook waits on `reachable: true`) |
| The step itself (`oasis_transcribe`) | `GetModelInfo` must answer within `health_timeout_s` (5 s) before any audio is sent; a `Transcribe` call that is unavailable or exceeds `timeout_s` fails the step too | The step fails with error type `step`, retryable, message `auxiliary-unavailable: …` |

The detail names the step, the parameter, the auxiliary version and the endpoint.

## Place in the loop

Data. Pseudo-labelling an untranscribed corpus asks several members for a text; a member that is a service must be
running for the whole pipeline. On the staging host OASIS is not resident: start it before the pipeline and stop it
afterwards.

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/auxiliary-unavailable` |
| `status` | `503` |
| `detail` | The step, the parameter, the auxiliary (`auxiliary/oasis 2026-10-03.…`) and the endpoint that did not answer |

What to do:

1. Start the service on its host. For OASIS: `scripts/serve.sh ensemble no-300m` in `/home/era/oasis` (8.6 GB on the
   card beside vLLM). It listens on port 50051.
2. Check that the endpoint in the auxiliary's payload is how the control plane and the services worker reach the host
   (`host.docker.internal:50051` in compose, which maps it to the host gateway).
3. Make sure the secret the payload names (`tokenSecret: oasis-token`) holds the service's bearer token
   (`secrets.new`); a wrong token is not this problem but an input error (`UNAUTHENTICATED`).
4. Run the pipeline again (or `pipelineRuns.retry` the failed step). Steps already done are reused.

## Commands

- `pipelines.run` with `dryRun=true` — repeats the check without starting anything.
- `auxiliaries.get` — `reachable` says whether the service answers now.
- `pipelineRuns.retry` — runs the failed step again once the service answers.
- `registry.search` with `kind:auxiliary` — the auxiliary versions and their payloads.

## Playbooks

- Agents: do not retry in a loop and never try to start the service. Tell the person which service to start, on which
  host, with the command from the auxiliary's conditions.

## Sources

- docs/review/2026-10-03-phase-4-plan.md, "Owner decisions" 1 and "Decisions taken for phase 4" 6 ("Cadence does not
  start OASIS. The pipeline's dryRun checks the service").
- docs/spec/08-resolutions.md R26 (auxiliary models).
- RFC 9457, Problem Details for HTTP APIs.
