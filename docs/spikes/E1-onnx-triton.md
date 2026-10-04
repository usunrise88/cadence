# E1 — ONNX export, parity and Triton serving

Status: done — the cache-aware export, parity and Triton serving work; three spec numbers change (parity's identical
share, the precision of the served engine, the Triton memory pool), and the mel front-end is left to the builder
Box: 1 day (phase 5 preparation, 2026-10-04)

## Goal
Answer with numbers what phase 5's `models.export`, `models.parity`, `models.benchmark` and the Triton repository
builder must do for the Nemotron 3.5 streaming family (ROADMAP phase 5 "Deployment"; R30, R31, R46; A3 left Triton at
about 2 real-time streams).

## Setup
- Staging host card, **now an RTX PRO 6000 Blackwell, 96 GB** (sm_120, driver 595.91, CUDA 13.2), shared: about
  67.5 GB held by resident services (an LLM engine with 44 GB, TTS, embeddings, another Triton) that were idle (0 %
  utilisation) during the runs. The host CPU was shared with other streams' stacks (load average 7–17). R30's
  exclusive card was not available: numbers below are a lower bound of what an exclusive card gives.
- Images: `cadence/worker:2e6aead` (NeMo Speech 26.07, NeMo 3.0.0, torch 2.12 cu132) for export and parity, with
  `onnxruntime-gpu` 1.30.0 (PyPI, CUDA 13) in a scratch directory; `nvcr.io/nvidia/tritonserver:26.08-py3`
  (ONNX Runtime 1.28 backend, TensorRT 11.2.1, `trtexec`) for serving.
- Model: the base `nvidia/nemotron-3.5-asr-streaming-0.6b` at `ea30d66`, from the stand's offline Hugging Face cache
  (volume `cadence-test_artifacts`, read-only). The phase-4 fine-tune was not needed: export and serving do not
  depend on the weights.
- Sample (R31's "fixed 200 utterances"): the first 200 files by name of FLEURS sr test
  (`/cadence/corpora/fleurs-sr/70bb2e84b976/test`), 0.627 h, references transliterated sr-Cyrl-Latn as
  `dataset_import@3` does; prompt `hr-HR` (index 29), as the stand's Serbian evals.
- Memory: export on the CPU; parity ≤ 7.2 GB on the card (NeMo 2.9 GB + ORT ≤ 3.5 GB + contexts); Triton ≤ 11.3 GB
  in every run kept: 8.7 GB at the 256-stream limit, 11.3 GB with the 6.5 GB pool for 512 streams. One rejected
  configuration reached 23 GB (see Surprises).

## Steps
1. Export the family to cache-aware ONNX per latency profile (80, 160, 1120 ms); record what had to be patched.
2. Parity on the 200 utterances against Cadence's pipeline decoder (`nemotron_transcribe@4`), same chunking and
   endpointing: WER difference and identical token sequences.
3. Serve with Triton (sequence batching, implicit state); p50/p95 time to final and RTF at 1–32+ concurrent real-time
   streams at 80 ms; streams per card before p95 exceeds chunk + 100 ms.
4. Record what the step kinds and the repository builder must do, and the defaults.

## Acceptance
Export runs without hand edits; parity meets R31 or the brief says why not; Triton serves 32 streams at 80 ms within
R31's latency budget under a 12 GB cap.

## Record
Patches; parity per profile; latency and RTF per concurrency; streams per card; memory.

## Result

Run 2026-10-04 on the staging host. Scripts in `docs/spikes/e1/` (all through `run.sh`, the runtime image in a
container of our own; scratch in `/cadence/spikes/e1`):

| Script | Step | What it shows the NeMo pack |
| --- | --- | --- |
| `export_step.py` | 1 | the export: one *step graph* per profile (encoder + prompt + greedy RNN-T, all state in and out) |
| `parity.py`, `parity_all.sh`, `summarize.py` | 2 | parity against `cadence_nemo.pipeline.decode_batch` on identical feature buffers |
| `bench_ort.py`, `inspect_onnx.py` | 2–3 | ONNX Runtime per-call cost with the state on the card; graph contents |
| `prep_feats.py`, `triton/build_repo.py`, `triton/trt_build.sh`, `triton/serve.sh` | 3 | the repository builder's output, the TensorRT engine, the server |
| `triton/client.py`, `triton/client.sh`, `triton/bench_all.sh`, `check_served.py` | 3 | the benchmark: real-time streams, latency, Triton's own counters, served = parity output |

**Verdict.**
- **Export works** with four changes to NeMo's path (below) and takes 42 s per profile on the CPU.
- **Parity:** WER difference **0.000 at 80 and 160 ms, +0.025 at 1120 ms** (R31: ≤ 0.1, passes). Identical token
  sequences **96.0–99.0 %**: R31's ≥ 99.5 % fails, and so does NeMo against itself (98.5 % between batch 8 and
  batch 1). The threshold needs to change, not the export.
- **fp16 breaks parity** (76 % identical, +0.10 WER): the served engine must be fp32 with TF32 off.
- **Triton serves 80 ms streaming at scale.** The strict-fp32 TensorRT engine:
  - p95 per chunk **20 ms at 32 streams and 63 ms at 256 streams**, so **256 streams per card** within R31's budget;
  - its served words equal NeMo's on 198/200 clips, with Δ WER 0.000.
- The fp16 engine: 9 ms at 32 streams and 57 ms at 512.
- A3's 2.2 streams were the Python loop and the host copies, not the model.
- ONNX Runtime's CUDA provider inside Triton saturates below 8 streams.

### 1 · Export

`export_step.py` writes `step.onnx` (+ one external data file) and `streaming_cfg.json` per profile:
one call = one chunk of B streams:

| | Inputs | Outputs |
| --- | --- | --- |
| Request | `audio_signal` f32[B,128,T] (the pipeline decoder's buffer: pre-encode cache + chunk, right-padded), `length` i64[B,1], `start` i32[B,1], `prompt` i64[B,1] | `tokens` i32[B, T_out × 10] (−1 = none), `encoded_len` |
| State (in → out) | `kv_cache` [24,56,1024], `conv_cache` [24,1024,8], `cache_fill` [1], `lstm_h` [2,640], `lstm_c` [2,640], `prev_token` [1] | the same, `_out` |

| Profile | `att_context_size` | Buffer T (frames) | First chunk | T_out | Nodes | Export |
| --- | --- | --- | --- | --- | --- | --- |
| 80 ms | [56,0] | 17 (9 + 8) | 1 frame, no cache | 1 | 6 898 | 28 s (+14 s restore) |
| 160 ms | [56,1] | 25 (9 + 16) | 9 frames | 2 | 7 607 | 29 s |
| 1120 ms | [56,13] | 121 (9 + 112) | 105 frames | 14 | 16 115 | 50 s |

2.55 GB fp32 (1.28 GB fp16) per profile; the weights are the same in every profile, only the graph differs.
State per stream: 6.3 MB fp32 (the 56-frame attention cache of 24 layers is 5.5 MB of it).

What had to change against NeMo's own export (`model.export()`, A3's `export_onnx.py`):
1. **The language prompt** sits between encoder and joint (A3): the step graph applies `prompt_kernel` to the
   one-hot prompt index; the stock encoder graph feeds the joint un-prompted output.
2. **The first chunk.** NeMo bakes `drop_extra_pre_encoded` (2 frames) into the graph as a Python constant.
   Cadence's pipeline decoder drops nothing on a stream's first chunk, which has no pre-encode cache (A5's shim 1).
   A3 avoided this with `pad_and_drop_preencoded` on both sides, but that is not the decoder evals use.
   - The step graph wraps the subsampling module: row b keeps pre-encoded frames `[d_b, d_b + T − 2)`, with
     d = 0 when `start` is set and 2 otherwise.
   - The first chunk is right-padded to the full buffer. The subsampling is causal, so the padding cannot reach the
     kept frames, and every row has the same T_out.
   - A batch can therefore mix first and later chunks of different streams, which is what a sequence batcher hands
     the model.
3. **State reset in the graph.** On `start` the graph zeroes the caches and LSTM state and sets the last token to
   blank. Triton's `initial_state` can only give zeros, and the last token must start as blank (13 087).
4. **Greedy RNN-T in the graph.** No Python runs between encoder and joint: per encoder frame the graph runs
   `max_symbols` = 10 prediction-network + joint steps with an `active` mask. This is NeMo's `greedy_batch`
   semantics: a row stops at its first blank of a frame; after 10 symbols the frame ends without one.
   - The steps are unrolled: 140 at 1120 ms, which makes that graph 2.3× larger and its ORT CUDA session need more
     than 4.5 GB to initialise. A `Loop` would be smaller; it was not needed at 80 ms.
5. For Triton only (`--triton-names --float-state`, see Surprises): the state tensors are named apart and
   all FP32.

ORT CPU against the traced module on a random batch: tokens and LSTM state identical, caches within 2e-4 (fp32).

### 2 · Parity (R31)

Both paths see identical feature buffers. `PipelineStream`'s `Features` and `_chunks` make them, so the ONNX side
gets exactly what `decode_batch` sends: a short first chunk, then cache + chunk, and the last chunk padded.
- NeMo side: `cadence_nemo.pipeline.decode_batch`, as `nemotron_transcribe@4` runs it (fp32, matmul precision
  `highest`, EOU at 800 ms). Its token sequence is read from the hypothesis on each stream's last step. Endpointing
  never resets the RNN-T state, so that sequence covers the whole stream.
  - On all 200 clips the pipeline's joined finals equal the detokenised sequence (`nemo_pipeline_text_equals_its_tokens`
    200/200).
- ONNX side: ORT 1.30, states carried per stream, batches stepping together as in the pipeline.

| Profile | ONNX path | Batch NeMo/ORT | Identical tokens | WER NeMo | WER ONNX | Δ WER | ONNX vs NeMo words¹ |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 80 ms | fp32, CUDA EP, **TF32 off** | 8/8 | **198/200 (99.0 %)** | 32.807 | 32.807 | **0.000** | 0.05 % |
| 80 ms | fp32, CUDA EP, TF32 (ORT default) | 8/8 | 192/200 (96.0 %) | 32.807 | 32.807 | 0.000 | 0.33 % |
| 80 ms | fp32, CUDA EP, TF32 (ORT default) | 1/1 | 195/200 (97.5 %) | 32.807 | 32.832 | +0.025 | 0.20 % |
| 80 ms | **fp16**, CUDA EP | 8/8 | **152/200 (76.0 %)** | 32.807 | 32.907 | **+0.100** | 1.69 % |
| 160 ms | fp32, CUDA EP, TF32 off | 8/8 | 195/200 (97.5 %) | 32.155 | 32.155 | 0.000 | 0.18 % |
| 160 ms | fp32, CUDA EP, TF32 (default) | 8/8 | 195/200 (97.5 %) | 32.155 | 32.155 | 0.000 | 0.18 % |
| 1120 ms | fp32, CPU EP | 8/8 | 196/200 (98.0 %) | 29.270 | 29.295 | +0.025 | 0.10 % |
| *noise floor* | *NeMo batch 8 vs NeMo batch 1, 80 ms* | — | *197/200 (98.5 %)* | 32.807 | 32.807 | 0.000 | 0.10 % |
| 80 ms | **served:** Triton, TensorRT fp32 (`--noTF32`), mixed batches under load | 8/— | **198/200 (99.0 %)**; 200/200 = ORT TF32 off | 32.807 | 32.807 | **0.000** | — |
| 80 ms | served: Triton, TensorRT fp16 | 8/— | 186/200 (93.0 %) | 32.807 | 32.907 | +0.100 | — |

¹ The two hypotheses scored against each other: word errors of the ONNX transcript with NeMo's as the reference.

- **Every difference is a near-tie, one word long.** Examples: "zaključio" / "zaključu", "izozati" / "izozuti", a
  dropped "s". The transcripts re-converge within a word. The WER of the two paths is equal or 0.025 apart in every
  fp32 run.
- **TF32 was most of the 80 ms gap.** ORT's CUDA provider uses TF32 matmuls by default, while NeMo's pipeline runs
  at matmul precision `highest`. With `use_tf32=0` the gap went from 8 clips to 2. At 160 ms it changed nothing; the
  rest comes from kernel order (cuDNN and ORT against cuBLAS and torch).
- **R31's ≥ 99.5 % is below the reference's own noise.** NeMo's pipeline decoder at batch 8 and at batch 1 agrees on
  197/200 at 80 ms: the batch changes the order of floating-point sums (the phase-3 finding in `defaults.yaml`,
  `transcribe_batch_size`). The base model on Serbian under a Croatian prompt has many near-ties (WER 33). A gate at
  99.5 % would fail exports that are as faithful as NeMo is to itself.
- **fp16 is not parity:** 48 clips differ, WER +0.10. Its words differ in 1.7 % of positions, against 0.05–0.33 % for
  fp32. TensorRT's fp16 engine serves faster (below), but a model version gated on fp32 NeMo evals must be served
  in fp32 — or gated on the fp16 engine's own eval.
- **Speed of the parity harness itself:** NeMo's pipeline decodes the 0.627 h at RTF 0.023 (batch 8). ORT through
  Python with host-side state runs at RTF 0.23. Parity is a batch job of minutes: 80 ms batch 8 took 10 min, batch 1
  took 45 min.

### 3 · Triton: sequence batching with implicit state

`triton/build_repo.py` writes one model, `nemotron_step`, with:
- `max_batch_size` 64;
- the `oldest` sequence batcher, queue delay 1 ms;
- `start` wired to CONTROL_SEQUENCE_START;
- the six states as implicit state with zero initial values;
- request inputs `audio_signal` [128,17], `length` and `prompt`; output `tokens` [10].

No Python model and no ensemble: Triton keeps each stream's state and batches chunks of different streams. Backends
tried:
- ONNX Runtime (CUDA EP) on `step.onnx`;
- TensorRT on an engine `trtexec` built from the same `step.onnx` (one profile, B = 1..64, opt 32): 1.2 GB fp16,
  2.4 GB fp32 with `--noTF32`.

`triton/client.py` (features precomputed by `prep_feats.py`, the parity buffers) plays S streams in real time:
- every stream plays the 200 clips one after another, one sequence per clip;
- chunk k is sent when its audio has arrived;
- **latency** is measured from that moment to its tokens being back;
- **time to final** is the same for the clip's last chunk, the final of an utterance that ends with the audio;
- 45 s per level after a 5 s warm-up, 4–8 client processes.

R31's budget (time to final ≤ chunk + 100 ms) is read as **p95 latency ≤ 100 ms**: a word at the start of a chunk
waits up to one chunk for it to fill. The 800 ms endpointing wait of the decoder configuration is the same served or
not and is excluded.

**TensorRT, strict fp32 (`--noTF32`), the parity precision** (CUDA memory pool 4 GB):

| Streams | 1 | 8 | 16 | 32 | 48 | 64 | 96 | 128 | 160 | 192 | 256 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| latency p50 ms | 9.7 | 9.3 | 11.9 | 14.5 | 16.3 | 17.9 | 19.8 | 21.7 | 24.8 | 29.6 | 46.7 |
| latency p95 ms | 10.3 | 15.7 | 18.3 | **19.5** | 20.8 | 24.7 | 26.0 | 30.9 | 38.2 | 50.6 | **63.3** |
| latency p99 ms | 10.5 | 16.4 | 19.7 | 20.5 | 22.0 | 26.4 | 31.0 | 39.5 | 47.3 | 57.3 | 72.3 |
| time to final p50 / p95 ms | 8.8 / 9.1 | 9.0 / 14.1 | 11.3 / 18.3 | 14.3 / 21.2 | 16.2 / 23.3 | 17.4 / 28.3 | 19.3 / 30.0 | 21.2 / 37.0 | 24.6 / 44.4 | 28.9 / 54.4 | 50.6 / 83.6 |
| RTF per stream² | 0.12 | 0.13 | 0.16 | 0.18 | 0.20 | 0.22 | 0.25 | 0.28 | 0.32 | 0.40 | 0.57 |
| avg batch | 1.0 | 1.4 | 2.0 | 3.0 | 4.5 | 5.9 | 8.9 | 12.1 | 16.1 | 20.6 | 33.2 |
| execution ms | 6.4 | 6.5 | 6.7 | 7.1 | 7.5 | 8.1 | 8.5 | 8.9 | 10.1 | 12.2 | 14.3 |

Beyond 256 (CUDA pool 4–6.5 GB, 8 client processes):
- 288 streams: p50 / p95 121 / 316 ms; executions 14.7 ms at batch 40, card 97 % busy. Saturated.
- 320 streams: p95 1.7 s. 512 streams: p95 11.2 s.
- Triton peaked at 11.3 GB with the 6.5 GB pool.

Served transcripts, the first complete decode of each clip during the sweep in whatever batch it fell into, compared
with the parity run (`check_served.py`):
- **200/200 identical to ORT's offline decode** with TF32 off;
- 198/200 identical to NeMo's pipeline decoder;
- WER 32.807, the same as NeMo's.

Batching under load therefore does not move the strict-fp32 engine's words, and TensorRT fp32 equals ORT fp32 here.

**TensorRT fp16** (faster, not parity):

| Streams | 1 | 8 | 16 | 32 | 48 | 64 | 96 | 128 | 160 | 192 | 256 | 320 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| latency p50 ms | 5.3 | 5.4 | 5.6 | 6.1 | 6.7 | 7.5 | 8.0 | 8.8 | 9.1 | 9.6 | 10.6 | 11.4 |
| latency p95 ms | 5.8 | 7.7 | 7.8 | 9.0 | 9.8 | 10.5 | 11.1 | 13.3 | 15.8 | 17.5 | 22.2 | 26.0 |
| time to final p95 ms | 5.6 | 6.5 | 6.9 | 7.7 | 8.7 | 9.4 | 10.0 | 10.7 | 11.3 | 11.8 | 14.8 | 17.3 |
| RTF per stream² | 0.07 | 0.07 | 0.07 | 0.08 | 0.09 | 0.09 | 0.10 | 0.11 | 0.12 | 0.13 | 0.15 | 0.16 |
| execution ms (avg batch) | 3.0 (1) | 3.1 (1.5) | 3.1 (2) | 3.2 (2.3) | 3.3 (3) | 3.2 (3.4) | 3.4 (4.6) | 3.5 (5.8) | 3.6 (7.3) | 3.6 (8.7) | 3.8 (11.7) | 3.9 (15.3) |

Beyond 320 (pool 6.5 GB):
- p95 29.6 ms at 384 streams, 36.0 ms at 448, **57.3 ms at 512** (still in budget);
- executions 5.3 ms at batch 36; Triton peaked at 9.8 GB;
- not pushed further: more streams need a larger state pool than the 12 GB cap allows.

Served fp16 transcripts: 186/200 identical to NeMo, WER +0.10 (32.907).

² Mean latency per chunk ÷ chunk length: the share of real time a stream waits for the server.

**ONNX Runtime (CUDA EP) in Triton**, same graph, same repository otherwise:

| Streams | 1 | 8 | 16 | 32 | 64 |
| --- | --- | --- | --- | --- | --- |
| fp32, TF32 off: latency p50 / p95 ms | 14.4 / 15.7 | 70.6 / 159.2 | 591 / 1 451 | 2 562 / 5 246 | 8 913 / 18 227 |
| execution ms (avg batch) | 12.1 (1.0) | 26.7 (3.0) | 33.6 (5.7) | 44.7 (13.9) | 57.4 (22.1) |
| fp16: latency p50 / p95 ms | 11.5 / 14.0 | 4 738 / 9 117 | 6 707 / 12 832 | 5 223 / 13 091 | 15 576 / 28 715 |

- ORT serves one stream in budget and saturates between 1 and 8. Its graph runs about 4 000 small kernels per call:
  14–19 ms per call at any batch in `bench_ort.py`, and longer in Triton where batch sizes vary between calls.
  - The fp32 run used `arena_extend_strategy` kSameAsRequested (A3's value), which reallocates for every new batch
    shape.
  - cuDNN's frontend reports "No execution plans support the graph" and runs the conformer convolutions in fallback
    mode.
  - Triton's input and output handling around the ORT model grew with the batch even with a 2 GB pool: 17 ms and
    10 ms per execution at batch 14, 56 ms and 21 ms at batch 22. Under TensorRT it stayed under 2.2 ms at batch 33.
  - Not tuned further: TensorRT on the same graph is 2–4× faster per call and fuses the graph.
- **Memory:**
  - Triton peaked at 8.7 GB with the 2.4 GB fp32 engine and a 4 GB CUDA pool, and at 7.2 GB with fp16.
  - Card utilisation p50 was 91 % in the fp32 sweep and 56 % in the fp16 sweep.
  - The resident services were untouched; the card had about 30 GB free throughout.

**Streams per card before p95 exceeds chunk + 100 ms (80 ms profile, this card, shared host CPU):**
| Engine | Parity | Streams per card (p95 ≤ 100 ms) | p95 at 32 streams | Triton memory at the limit |
| --- | --- | --- | --- | --- |
| TensorRT fp32, TF32 off | passes (99.0 %, Δ 0.000) | **256** (p95 63 ms; 288 fails at 316 ms) | 19.5 ms | 8.7 GB (4 GB pool) |
| TensorRT fp16 | fails (93.0 %, Δ +0.10) | **≥ 512** (p95 57 ms; not pushed further, memory cap) | 9.0 ms | 9.8 GB (6.5 GB pool) |
| ONNX Runtime CUDA EP fp32 | passes (offline) | **1** (p95 159 ms at 8) | 5 246 ms | — |

- R31's target of 32 streams is met with eightfold headroom by the parity engine.
- One card at 80 ms carries about 256 real-time calls on one Triton instance. Near the limit the card was 91–97 %
  busy, so a second instance would not add capacity; a smaller state (fp16 caches) or a faster engine would.
- A3 reached 2.2 streams on the 48 GB card.

### What phase 5 should build (step kinds and builder)

- **`models.export` → the NeMo pack's export kind** (role `export` in the family descriptor, e.g. `nemotron_export@1`):
  - input: a checkpoint and a latency profile; output: an `onnx_export` artifact with `step.onnx`, its data file and
    `streaming_cfg.json`, which records the geometry, the state shapes and names, the prompt dictionary and the
    precision;
  - CPU only, about 1 min and 5 GB of RAM per profile; one export per profile, because the attention context is
    baked in;
  - carries the four changes of step 1; `max_symbols` comes from the model config;
  - fp32 only. Lower precision is the engine builder's decision and must pass parity itself.
- **`models.parity` → the pack's parity kind** (role `parity`):
  - feeds the identical pipeline buffers to the served artifact and to `nemotron_transcribe`'s decoder, at the eval's
    batch (8);
  - reports identical share, Δ WER, word disagreement and the differing clips (`parity.py`'s JSON is the shape);
  - must run the **engine that will be served** (the TensorRT plan, through Triton or the TRT runtime), not only the
    ONNX file: precision is decided at engine build, and fp16 is where parity broke;
  - CUDA EP or TensorRT with TF32 off.
- **The Triton repository builder** (in the NeMo pack, R46), per target card class:
  - builds the TensorRT engine on the target's GPU class: an engine is specific to the GPU and the TensorRT version.
    The plan is an artifact keyed by (export, card class, TensorRT version, precision);
  - writes the `config.pbtxt` of `build_repo.py`: the oldest strategy, START wired to `start`, implicit state with
    zero initial values, `max_batch_size` = the engine profile's maximum;
  - the server needs a CUDA memory pool of at least streams × 12.6 MB (state in and out) at 80 ms fp32;
  - no Python model in the hot path.
- **Missing from this spike: the mel front-end.**
  - The client sent features computed by the pipeline's `Features`, the whole-stream-equal featurisation of A5's
    shim 4.
  - A production target receives audio. The builder must put a front-end model before `nemotron_step` (ensemble, or
    a decoupled BLS): a stateful featuriser that keeps two frames of audio per stream (implicit state again) and
    emits exactly those buffers. NeMo's preprocessor exports as STFT + mel ONNX.
  - Parity must then run through the front-end too. Not measured here.
  - Its cost is small next to the encoder (one 512-point STFT per 10 ms).
- **Endpointing and text stay outside the server.** Triton returns tokens. EOU detection (800 ms of blank emissions),
  detokenisation and stripping of the locale tag happen in the caller, as in `cadence_nemo.pipeline.consume`. Эра's
  client or a thin gateway must use the same rules, or finals will differ from the eval's.
- **`models.benchmark`:**
  - a job of kind `benchmark` (R30) with the client of `triton/client.py`: a real-time sweep over stream counts,
    reporting latency p50/p95/p99, time to final, RTF per stream, and Triton's execution, batch, queue and
    input/output counters;
  - stops at the first level whose p95 exceeds the budget, and reports the last passing one as "streams per card";
  - needs the card exclusively (R30): the numbers here were taken beside idle residents with the host CPU loaded, and
    executions grow 6 → 14 ms between 1 and 256 streams.

### Surprises
- **Triton mixes up implicit states whose names extend one another.**
  - With states `cache_last_channel` and `cache_last_channel_len`, the second request of every sequence failed:
    "Invalid rank for input: cache_last_channel_len Got: 4 Expected: 2", or a dtype mismatch.
  - Renaming the states so that none extends another (`kv_cache_in` / `cache_fill_in` …) fixed it. Triton 26.08,
    ONNX Runtime backend.
  - INT64 states also came back as FP32 once. All states are FP32 in the served graph.
- **The CUDA memory pool decides the concurrency.**
  - With a 256 MB pool (A3's setting), the state buffers of more than about 20 streams (256 MB ÷ 12.6 MB) do not fit
    and Triton places them in host memory (an inference from the arithmetic and the effect below; the 256 MB runs
    predate the input/output counters).
  - Gathering and scattering 12.6 MB per request then capped the server at about 850 requests/s, about 64 streams,
    with the card 15–30 % busy.
  - At 2 GB: the same fp32 engine went from p95 301 ms to 22 ms at 64 streams, and from 11.3 s to 25 ms at 96
    streams; input + output handling stayed under 0.8 ms per execution.
- **`use_growable_memory` is unusable here.**
  - It rounds state buffers up to 2 MB granules, and then the input size check rejects every request: "unexpected
    total byte size 6291456 for input 'kv_cache_in', expecting 5505024".
  - It also reserved 23 GB on the card. That breaks the 12 GB cap: do not enable it.
- **ORT's CUDA EP defaults to TF32**, while NeMo uses fp32 at matmul precision `highest`. That alone moved parity at
  80 ms from 99.0 % to 96.0 % identical.
- **The card changed:** the staging host now has a 96 GB RTX PRO 6000, not A3's 48 GB RTX PRO 5000. The R12 compute
  rows and A3's 22 GB cap were measured on the old card.
- **A3's Triton bottleneck was the architecture, not the model.** A Python BLS model looped the greedy decoding and
  copied 5.6 MB of state through the host twice per chunk. The same weights in one TensorRT step graph with
  implicit state serve over 100× the streams.

### What the spec should change (proposals; not edited here)
1. **R31 parity** (`defaults.yaml` `deploy.parity_*`, new):
   - `parity_wer_delta_max: 0.1` (absolute WER points; keep);
   - **`parity_identical_min: 0.97`** instead of 0.995. NeMo against itself is 0.985 and fp32 exports 0.975–0.99. The
     source line should cite this spike;
   - `parity_disagreement_max: 0.005`, words of the served transcript against NeMo's (fp32 0.0005–0.0033, fp16 0.017);
   - parity runs the served engine at the eval's batch, with TF32 off on both sides.
2. **R31 latency:**
   - `latency_budget_ms: 100` beyond the chunk at p95, per chunk, measured from audio availability (keep);
   - `benchmark_target_streams: 32` stays Эра's placeholder;
   - add `streams_per_card` as a measured output of `models.benchmark`, not a default. Seed it from this spike:
     TensorRT fp32 at 80 ms on `blackwell-96gb`: 256 streams (p95 63 ms; shared host).
3. **R46 / 03 "deployment target":** the Эра target serves a **TensorRT engine of the fp32 step graph** with sequence
   batching and implicit state. That is R46's pattern, with ONNX as the portable artifact and TensorRT as the engine.
   - ONNX Runtime's CUDA provider is an acceptable parity reference, not a serving engine for this family (≈ 1–8
     streams).
   - Engines are built per (card class, TensorRT version) by the builder in the NeMo pack.
4. **R30 staging serving:**
   - Triton's compose profile sets `--cuda-memory-pool-byte-size` to at least target streams × 12.6 MB at fp32
     (`deploy.triton_cuda_pool_mb`, default 4096 for 256 streams);
   - the server's cap is the engine plus the pool plus about 2 GB (this run: 8.7 GB at 4 GB pool);
   - never `use_growable_memory`.
5. **03 / family descriptor:**
   - per profile, the step graph's state names and shapes (as `streaming_cfg.json` records them) and T_out;
   - "export: fp32 only; fp16 must pass parity on its own".
6. **06 / 11:** the Model panel's benchmark chart needs the time-to-final series and the per-level Triton counters
   (batch, execution, queue), which explain a failing level at a glance.
7. **Open questions for the owner:**
   - Эра's side receives tokens or text? Endpointing in Эра's client or in a Cadence-built gateway?
   - Is fp16 acceptable if the model version is re-gated on the fp16 engine's own eval? That would double the
     streams per card.
