---
title: nemotron_serve (step kind)
summary: The Nemotron family's serve role — loads a deployable on the staging server and streams audio to it, in batch (parity, benchmarks, shadow replay) or as the relay of a transcription session whose targets are deployments.
contexts: [step:nemotron_serve, job-kind:eval, job-kind:benchmark, job-kind:shadow, job-kind:interactive, family:nemo.fastconformer-rnnt.cache-aware, guide:staging-serving]
---

## What this is

`nemotron_serve@1` fills the `serve` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`). It runs the
family's side of a staging server (docs/spec/06-platform.md "Staging serving"): the control plane never speaks the
server's stream protocol.

The lease names the staging target (`CADENCE_SERVING_TARGET`, `…_ENDPOINT`, `…_SERVER`, `…_SERVER_VERSION`) and
the versioned model name of each deployable input (`CADENCE_SERVING_MODEL`, `…_MODELS`: `cadence-<16 hex digits of
the deployable's hash>`). A target the step may not serve through — a delivery target, an archived one, one that
does not list the deployable's family, format or profile — comes as `CADENCE_SERVING_REFUSED`, and the step fails
`target-does-not-serve`.

1. **Install.** The deployable's model directory (`deployable.json` `serving.modelDir`) is copied into the `serving`
   volume (`CADENCE_SERVING_DIR`, `/var/lib/cadence/serving`) under the model name, its `config.pbtxt` renamed to it.
   An installed copy is reused.
2. **Load.** Unless the server already has the model ready (another lease brought it), the step loads it through the
   model-control API and waits up to `serving.load_timeout_s`. It reads the card's memory before and after: a model
   over its reservation (the deployable's `serving.memoryMb`, else the lease's cap) by more than
   `serving.over_cap_slack_mb` is unloaded and the step fails `serving-over-cap`. A server that does not answer fails
   `serving-unavailable` (retryable).
3. **Stream.** Spike E1's step graph: one request per chunk of one stream, the sequence batcher keeping the stream's
   state on the server (`sequence_start` resets it), the answer the chunk's token ids. The client computes the
   features the pipeline decoder would (the export's preprocessor, `pipeline.Features`) and sends exactly the buffers
   `nemotron_transcribe` decodes: a first chunk without cache, then the pre-encode cache and the chunk, right-padded.
   Endpointing (a final after `stop_history_eou_ms` of audio without a token), detokenisation and stripping the
   locale tag happen in the client, as Эра's client must do them.

**Batch mode** streams every utterance of the `data` input at `concurrency` streams, at real-time pace (a chunk
leaves when its audio has arrived) or as fast as the server answers, once — or for `seconds`, each stream cycling
through the utterances (a benchmark level). It writes:

- `hypotheses`: one row per utterance, its first complete decode, with `tokens` (the parity check compares them),
  words timed by the chunks that emitted them, partials and per-chunk `steps`;
- `serving_timings`: per utterance and chunk `[audio end ms, available ms, sent ms, answered ms]` from the level's
  start, the card's telemetry once a second, and a `level` line with the concurrency, errors and the server's own
  counters before and after.

**Relay mode** (`mode: relay`, set by `transcriptions.new` for deployment targets, job kind `interactive`) serves the
live channel like [`nemotron_live`](nemotron-live.md); every lane is a stream of a served model and no model is
loaded on the worker's own card.

## Place in the loop

Block 4, Deploy: the served side of `models.parity`, every level of `models.benchmark`, both sides of a shadow
deployment's nightly replay, and manual tests of a deployment.

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `mode` | `batch` | 06 "Staging serving" | `batch`, `relay` |
| `target` | `serving.default_target` (`staging`) | R30 | a `dtg_` id or a target name |
| `concurrency` | 1 | R31 | 1–512 |
| `pace` | `realtime` | R47 | `realtime`, `fast` |
| `profile` | the deployable's | — | an export is one profile |
| `seconds` | 0 (every utterance once) | R31 (`deploy.benchmark_seconds_per_level`) | 0–3600 |
| `partials` | true | — | — |
| `target_lang` | `packs.nemo.target_lang` (each clip's language) | — | — |
| `stop_history_eou_ms` | `packs.nemo.live_stop_history_eou_ms` (800) | Spike A5 | 80–10 000 ms |
| `load_timeout_s` | `serving.load_timeout_s` (300) | Cadence recommendation | 30–1800 |
| `over_cap_slack_mb` | `serving.over_cap_slack_mb` (512) | 06 "Staging serving" | 0–8192 |

Inputs: `deployable` (`deployable`), `data` (`dataset`, batch mode), `audio` (a span, relay mode). The reservation is
the served model's (`serving.model_memory_gb`, 9 GB, unless the deployable states one), from the card's serving
reserve; leases of the same model on a card share it.

What the deployable's `streaming_cfg.json` must hold (the export writes it, spike E1): `chunk_size`,
`pre_encode_cache_size`, `buffer_frames`, `n_mels`, `window_stride_s`, `sample_rate`, `chunk_ms`,
`prompt_dictionary`, `blank_id`, `valid_out_len`, `max_symbols`, and for the client `vocabulary` (the tokenizer's
pieces by id) and `frontend` (`{"kind": "nemo", "preprocessor": {…}}`).

## Commands

Planned by `models.parity`, `models.benchmark` and the shadow replay (the generated pipelines), and by
`transcriptions.new` with a `deploymentId` target. `deploymentTargets.get` shows the served models and their leases.

## Playbooks

None directly; the deploy block's commands run it.

## Sources

- docs/spikes/E1-onnx-triton.md (the step graph, implicit state, the CUDA pool, parity of the served engine).
- docs/spec/06-platform.md "Staging serving"; [Staging serving](../guides/staging-serving.md).
- `worker/packs/nemo/tests/fixtures/serving` — the fixture deployable (one token per chunk) the tests run on a real
  Triton.
