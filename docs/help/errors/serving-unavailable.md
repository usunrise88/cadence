---
title: Staging server unavailable
summary: The staging target's inference server is down or did not answer — work that decodes through it (parity, benchmarks, shadow replay, transcriptions of a deployment) waits until it is up again. Cadence never starts it.
contexts: [error:serving-unavailable, guide:staging-serving, panel:deployment-targets, command:transcriptions.new, command:models.parity, command:models.benchmark, entity:deployment_target]
---

## What this is

A `503 Service Unavailable` problem of type `serving-unavailable`. Work that decodes through a staging target —
`models.parity`, `models.benchmark`, a shadow deployment's nightly replay, or a transcription session with a
deployment lane — needs the target's server, and the last health check found it down. The same text is the error of
a serve step that could not reach the server or load its model in time (`serving.load_timeout_s`); such a step is
retryable.

The control plane checks every staging target every `serving.health_check_seconds` (60) on the health path its
server kind names in `serving.servers` (Triton: `/v2/health/ready`). Settings → Deployment targets shows the state,
since when, and the error of the last check.

| Detail says | Why | What to do |
| --- | --- | --- |
| `… is down (dial tcp …: connection refused)` | The server's container is not running | `docker compose --profile serving up -d triton` on the staging host |
| `… answered 503` | The server runs but is not ready (still starting, or a model failed to load) | Wait a minute; read `docker compose logs triton` |
| `the model … did not load within …` (a step's error) | A model load took longer than `serving.load_timeout_s` | Check the server's log; a TensorRT engine built for another card class never loads |
| `no route to host`, `timeout` | The worker or the control plane is not on the `serving` network | Check `docker-compose.yml`: the control plane and the GPU worker join the `serving` network |

## Place in the loop

Block 4, Deploy: staging serving (docs/spec/06-platform.md "Staging serving").

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/serving-unavailable` |
| `status` | `503` |
| `detail` | The target, the last check's error and since when |

| Default | Value | Meaning |
| --- | --- | --- |
| `serving.health_check_seconds` | 60 | How often the control plane checks each staging target |
| `serving.health_timeout_s` | 5 | How long one health call may take |
| `serving.load_timeout_s` | 300 | Longest a serve step waits for a model load |

## Commands

- `deploymentTargets.get` — the target's `health` (state, since, detail, latency) and its served models.
- Retry the failed step (`pipelineRuns.retry`) once the target is up again.

## Playbooks

- Agents report the target and its state to a person: starting the server is a host operation, never an agent's.

## Sources

- docs/spec/06-platform.md "Staging serving"; [Staging serving](../guides/staging-serving.md).
