---
title: nemotron_export (step kind)
summary: The Nemotron family's export role — turn a checkpoint into a deployable at one latency profile — the fp32 cache-aware step graph as ONNX, its TensorRT engine (TF32 off) built for the target server in a Triton model directory, deployable.json with the manifest hash the delivery script checks, and the smoke client.
contexts: [step:nemotron_export, artifact:deployable, artifact:checkpoint, job-kind:export, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_export@1` fills the `export` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, job kind
`export`, a card for the engine build). `models.export` runs it once per latency profile; it reads a `checkpoint`
(input `model`) and writes a `deployable` (`cadence.deployable/1`, a directory):

| Path | What |
| --- | --- |
| `deployable.json` | format `triton-tensorrt-cache-aware`, family, profile, weights hash, precision `fp32`; `serving` (server `triton` and its version, `modelDir`, `memoryMb`, `cudaMemoryPoolMb`, `statePerStreamMb`, `maxStreams`, `maxBatch`, `chunkMs`, `input: features`, `engine`: TensorRT version, precision, `tf32: false`, card class, GPU, compute capability); `files` (the model directory's files with SHA-256 and bytes) and `manifestSha256` |
| `model/config.pbtxt`, `model/1/model.plan` | the Triton model directory: the TensorRT engine and its configuration |
| `client/transcribe` | the smoke client the delivery script runs on the production host (Python 3 standard library only) |
| `onnx/step.onnx`, `onnx/step.onnx.data`, `onnx/streaming_cfg.json` | the portable artifact: the fp32 step graph, its weights and its geometry |

**The step graph** (spike E1): one call is one 80 ms (or longer) chunk of up to `max_batch` streams — the cache-aware
encoder, the language prompt kernel and greedy RNN-T with up to 10 symbols per encoder frame, all in one graph. Every
piece of a stream's state (attention and convolution caches, cache fill, prediction-network LSTM state, last token)
is an input and an output, so Triton's sequence batcher keeps it per stream on the card (implicit state): no Python
runs in the serving path. A request carries the pipeline decoder's feature buffer — the pre-encode cache and a chunk of
log-mel frames, exactly what [`nemotron_transcribe`](nemotron-transcribe.md) decodes — the stream's first chunk is
flagged by Triton's sequence start, which resets the state inside the graph. The answer is the chunk's token ids
(`-1` = none). **The caller sends features** and does endpointing and detokenisation itself (`serving.input:
features`); a served featuriser is not built yet.

**The engine.** An engine runs only on the GPU and the TensorRT it was built with. `server_version` names the Triton
release of the target (26.08 → TensorRT 11.2.1); the worker image carries that TensorRT's `trtexec` under
`/opt/tensorrt/<version>` (from the Triton image itself), and the step refuses a release it has no builder for. The
build is strict fp32 (`--noTF32`): TF32 alone moved parity from 99.0 % to 96.0 % identical token sequences in spike
E1, and fp16 to 76 %. `card_class` (the staging target's) and the card's name and compute capability are recorded;
a target on another card class needs an export built there.

**The model directory** follows E1's Triton 26.08 workarounds: the TensorRT backend, the `oldest` sequence batcher
(`max_candidate_sequences` = `max_streams`, 1 ms queue delay), `start` wired to `CONTROL_SEQUENCE_START`, six implicit
states named apart (Triton confused `cache_last_channel` with `cache_last_channel_len`), all FP32, zero initial state,
never `use_growable_memory` (it reserved 23 GB and broke the size checks). `config.pbtxt` sets no `name`: the
directory names the model, so staging and the delivery script install it under a versioned name.

**Memory.** The server needs a CUDA memory pool of at least `maxStreams` × the per-stream state in and out
(`serving.cudaMemoryPoolMb`; 12.6 MB per stream at 80 ms); with 256 MB (A3's value) Triton keeps the state of more
than about 20 streams in host memory and stalls. `serving.memoryMb` is the engine, that pool and
`export_serving_overhead_mb` (CUDA context, activations, Triton).

**Manifest.** `manifestSha256` is the SHA-256 of one `<sha256>  <path>` line per model-directory file sorted by path
bytes — what `find | LC_ALL=C sort | sha256sum` gives over the installed directory, so a delivery receipt proves the
installed files are the approved ones.

### Measured (staging card, 2026-10-04)

Base model `nvidia/nemotron-3.5-asr-streaming-0.6b` at 80 ms, through the step's own code, beside the resident
services (about 30 GB of the 96 GB card free):

| What | Value |
| --- | --- |
| ONNX export on the CPU | 35 s (6 905 nodes, 2.55 GB of weights) |
| Engine build (TensorRT 11.2.1, fp32, TF32 off, B 1–64) | 27 s; TensorRT allocations peaked at 2.4 GB, the card at +3.45 GB |
| The whole step | 68 s |
| Engine | 2.56 GB; deployable about 5.1 GB with the ONNX |
| Triton 26.08 with the model loaded under a versioned name (pool 1 614 MB for 128 streams) | 6.0 GiB; `serving.memoryMb` 6 474 |
| `client/transcribe` against it on 20 FLEURS sr parity-sample utterances (`python:3.12-slim`, standard library) | 20/20 token sequences identical to [`nemotron_parity`](nemotron-parity.md) |

## Place in the loop

Deploy — `models.export` generates a pipeline with one export step per profile (job kind `export`); its `deployable`
becomes the model version's export (`models.get` → `exports[]`), which parity, benchmarks, shadow replay and
promotions use.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `profile` | `packs.nemo.profile` (80ms) | Key defaults (primary cell) | the family's profiles |
| `format` | `packs.nemo.export_format` (triton-tensorrt-cache-aware) | Spike E1 | triton-tensorrt-cache-aware |
| `card_class` | `packs.nemo.export_card_class` (empty; the control plane passes the target's) | Spike E1 | any |
| `server_version` | `packs.nemo.export_server_version` (26.08) | Spike E1 (Triton 26.08, TensorRT 11.2.1) | 26.08 |
| `max_batch` | `packs.nemo.export_max_batch` (64) | Spike E1 | 1–256 |
| `opt_batch` | `packs.nemo.export_opt_batch` (32) | Spike E1 | 1–256 |
| `max_streams` | `packs.nemo.export_max_streams` (128) | Spike E1; the stand's 7 GB serving reserve | 1–4096 |
| `queue_delay_us` | `packs.nemo.export_queue_delay_us` (1000) | Spike E1 | 0–100 000 µs |
| `workspace_mb` | `packs.nemo.export_workspace_mb` (2048) | Spike E1 | 256–16 384 MiB |
| `serving_overhead_mb` | `packs.nemo.export_serving_overhead_mb` (2300) | Spike E1 (8.7 GB = 2.4 engine + 4 pool + 2.3) | 0–16 384 MB |

## Commands

`models.export` (dryRun first); `models.get` shows the exports.

## Playbooks

None yet (phase 5).

## Sources

- docs/spikes/E1-onnx-triton.md (the step graph, parity per precision, Triton's serving numbers and workarounds) and
  its scripts in docs/spikes/e1/ (`export_step.py`, `triton/build_repo.py`, `triton/trt_build.sh`).
- docs/spec/03-pipelines-defaults.md "Export, parity and benchmark (phase 5)"; docs/spec/08-resolutions.md R31, R46.
- Triton Inference Server: sequence batcher, implicit state management; TensorRT `trtexec`.
