---
title: Staging serving
summary: How Cadence serves candidate models on the staging card with the server production runs — the compose profile serving, the staging target and its health, served models and their leases, the serving reserve beside training, and what to do when the server is down.
contexts: [guide:staging-serving, panel:deployment-targets, command:deploymentTargets.get, command:deploymentTargets.list, command:compute.edit, entity:deployment_target, error:serving-unavailable, error:serving-over-cap, error:target-does-not-serve, step:nemotron_serve]
---

## What this is

Parity checks, benchmarks, shadow replay and manual tests of a deployment decode through a real inference server on
the staging card, so they measure what production would do (R30). Cadence reaches only this **staging** target;
a **delivery** target (production) is reached by a person's delivery script, never by Cadence.

**Start the server** (Cadence never starts or stops it):

```
docker compose --profile serving up -d triton
```

It is Triton 26.08 (`serving.image`, pinned by digest) with explicit model control and an empty repository, the
`serving` volume, on the internal network `serving` with no host port. Its CUDA memory pool holds every live
stream's state, 12.6 MB per stream at fp32 and 80 ms: `CADENCE_TRITON_CUDA_POOL_MB` (default
`deploy.triton_cuda_pool_mb`, 4 096 MB, enough for 256 streams in spike E1).

**The staging target** `staging` is seeded at first start from `serving.staging_target`: endpoint
`http://triton:8000`, server `triton 26.08`, and the families, formats and latency profiles it serves. The control
plane checks it every `serving.health_check_seconds` (60) and shows `up` or `down`, since when and why, in
Settings → Deployment targets (`deploymentTargets.get` → `health`).

**Served models.** A step that consumes a deployable (the family's `serve` role) gets the target's endpoint and the
model's versioned name in its lease. It installs the model into the `serving` volume and loads it; the control plane
counts the leases per model (`servedModels`: `in-use` with its lease count, `loaded` while idle, `unloaded`) and
unloads a model no lease has used for `serving.unload_idle_minutes` (30).

**Beside training.** A card's `servingReserveGb` (Compute) is a share of its cap that a training step's whole-cap
reservation leaves alone. Served models, shadow replay and live sessions take their memory from it; leases of one
served model on a card count its memory once. On the stand (an RTX PRO 6000 Blackwell, 96 GB, about 30 GB free beside
resident services): cap 29 GB, reserve 9 GB, training keeps 20 GB. One served model (9 GB: E1 measured 8.7 GB with the
fp32 engine and the 4 GB pool) or one NeMo live session (6 GB) fits in the reserve, not both. Benchmarks take the
card alone.

| Symptom | Why | What to do |
| --- | --- | --- |
| Target `down`, work refused `serving-unavailable` | The `triton` container is not running or not ready | Start it; read `docker compose logs triton` |
| A step fails `serving-over-cap` | A model took more memory than its reservation | Check the export's `serving.memoryMb`; raise `serving.model_memory_gb` and the reserve together |
| `target-does-not-serve` | The target does not list the model's family, format or profile | Export at a served profile, or extend the target (`deploymentTargets.edit`, an approval) |
| A serve step waits in the queue | The serving reserve is taken (another model, a live session) | Wait, or raise `servingReserveGb` (`compute.edit`) |

## Place in the loop

Block 4, Deploy: between registering a model version and promoting it.

## Fields and defaults

| Default | Value | Meaning |
| --- | --- | --- |
| `serving.staging_target` | `staging`, `http://triton:8000`, triton 26.08 | The target seeded at first start |
| `serving.default_target` | `staging` | The target a serve step uses when its pipeline names none |
| `serving.image` | `nvcr.io/nvidia/tritonserver:26.08-py3@sha256:9185ba5b…` | The compose image |
| `serving.servers` | Triton: `/v2/health/ready`, `/v2/repository/index`, `/v2/repository/models/{model}/unload` | The paths the control plane calls (data) |
| `serving.model_memory_gb` | 9 | A served model's reservation unless its deployable states one |
| `serving.load_timeout_s` | 300 | Longest a model load may take |
| `serving.over_cap_slack_mb` | 512 | Allowance over the reservation after a load |
| `serving.unload_idle_minutes` | 30 | Idle time before a model is unloaded |
| `serving.health_check_seconds` | 60 | How often the server is checked |
| `deploy.triton_cuda_pool_mb` | 4096 | The server's CUDA pool |
| Compute `servingReserveGb` | 9 on the seeded staging card | The serving reserve |

## Commands

- `deploymentTargets.list|get` — targets with `health` and `servedModels`.
- `compute.edit` — a card's `memoryCapGb` and `servingReserveGb`.
- `transcriptions.new` with `deploymentId` — listen to a served model (people only).

## Playbooks

- Agents read the target's health before asking for parity or a benchmark; they never start the server.

## Sources

- docs/spec/06-platform.md "Staging serving"; docs/spec/08-resolutions.md R30, R46, R47.
- docs/spikes/E1-onnx-triton.md (Triton 26.08, the CUDA pool, memory at 256 streams).
