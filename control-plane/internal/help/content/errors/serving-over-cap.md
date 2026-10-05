---
title: Served model over its memory reservation
summary: A model loaded on the staging server took more card memory than its reservation allows, so the serve step unloaded it again and failed. Training beside it keeps its share of the card.
contexts: [error:serving-over-cap, guide:staging-serving, step:nemotron_serve, entity:deployment_target]
---

## What this is

A serve step (the family's `serve` role, e.g. `nemotron_serve`) failed with `serving-over-cap` (the problem type's
status is `409 Conflict`). The step reads the card's memory before it loads its model on the staging server and again
after; when the model took more than its reservation plus `serving.over_cap_slack_mb` (512 MB), the step unloaded it
and failed, so the model cannot crowd out a training run sharing the card.

A served model's reservation is the deployable's `serving.memoryMb` (written by the family's export), else
`serving.model_memory_gb` (9 GB: spike E1 measured Triton at 8.7 GB with the fp32 TensorRT engine and a 4 GB CUDA
pool). It comes from the card's serving reserve (Compute `servingReserveGb`).

| Detail says | Why | What to do |
| --- | --- | --- |
| `took N MB, its reservation is M MB` | The model is larger than its export says, or the server's CUDA pool grew | Re-export with the right `serving.memoryMb`, or raise `serving.model_memory_gb` and the card's serving reserve together |
| the delta includes other work | Something outside Cadence allocated on the card during the load | Retry when the card is quieter; the check compares two telemetry readings |

Never enable Triton's `use_growable_memory` for implicit state: it reserved 23 GB in spike E1.

## Place in the loop

Block 4, Deploy: staging serving (docs/spec/06-platform.md "Staging serving", Memory).

## Fields and defaults

| Field | Meaning |
| --- | --- |
| `type` | `https://cadence.local/help/errors/serving-over-cap` |
| `status` | `409` |
| `detail` | The model, what it took and its reservation |

| Default | Value | Meaning |
| --- | --- | --- |
| `serving.model_memory_gb` | 9 | A served model's reservation when its deployable states none |
| `serving.over_cap_slack_mb` | 512 | How far over its reservation a model may be after its load |
| `deploy.triton_cuda_pool_mb` | 4096 | The staging server's CUDA pool (the streams' state) |

## Commands

- `deploymentTargets.get` — the served models and their reservations.
- `compute.edit` — the card's `servingReserveGb`.

## Playbooks

- Agents report the failure; changing reservations is the admin's.

## Sources

- docs/spikes/E1-onnx-triton.md "Surprises" (the CUDA pool decides the concurrency; `use_growable_memory`).
