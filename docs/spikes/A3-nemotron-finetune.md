# A3 — Nemotron 3.5 fine-tune under a 24 GB cap, then ONNX parity

Status: partial — training, streaming eval and ONNX parity pass; Triton serves 80 ms streaming but saturates at ~2 real-time streams (phase 5)
Box: 2 days

## Goal
Confirm the training and export path on the staging card while other services stay resident.

## Setup
NeMo Speech 26.07 container; nvidia/nemotron-3.5-asr-streaming-0.6b pinned; a 20-hour Hebrew subset as Lhotse Shar; staging card RTX PRO 5000 Blackwell 48 GB with the vLLM service resident (≈ 24 GB), so memory fraction 0.5 (24 GB).

## Steps
1. Run OOMptimizer under the cap; record bucket batch sizes. 2. Fine-tune 500 steps with init_from_nemo_model, bf16, explicit Noam scale (log the computed peak LR). 3. Evaluate in streaming at [56,0] on a held-out set. 4. Export to ONNX; run parity on 200 utterances. 5. Load into Triton with sequence batching; measure p50/p95 at 80 ms chunks.

## Acceptance
Training runs without OOM; resident services untouched; ONNX WER within 0.1 point of NeMo; Triton serves streaming.

## Record
Batch sizes; steps per second; WER before/after 500 steps; parity delta; Triton latencies.

## Result

Run 2026-09-30 on the staging host, about 3 hours of the 2-day box (14:50–17:50 CDT). Scripts, all run through `docs/spikes/a3/nemo.sh`
(the pinned image, the host's Hugging Face token mounted read-only, data under `~/cadence-spikes/a3`):

| Script | Step | What it shows the NeMo pack |
| --- | --- | --- |
| `prep_data.py`, `data_stats.py` | 0 | manifests with `target_lang`, Shar with `supervision.language`, tokens/s for OOMptimizer |
| `cap.py`, `capped.py`, `gpuwatch.sh` | all | the memory cap, and proof that the resident service was not touched |
| `oomptimize.py` | 1 | the OOMptimizer invocation and the two shims the prompt model needs |
| `train.py`, `run_train.sh`, `tb_summary.py` | 2 | every Hydra key used (the `(*)` ones belong in the step schema), metrics read during training |
| `run_eval.sh`, `wer.py` | 3 | the real cache-aware streaming decoder, and the stated normalisation |
| `export_onnx.py`, `parity.py`, `parity_compare.py` | 4 | the export with the prompt kernel inside, and the parity harnesses (ORT CPU; served path) |
| `triton/` | 5 | model repository (`streaming_asr` Python BLS + two ONNX models), `serve.sh`, `client.py`, `stats.py` |

**Verdict: the training and export path works on the staging card beside vLLM. Triton serves streaming, but not yet
at the target concurrency.**
- **Training:** 500 steps ran without OOM under a 20.5 GiB allocator cap, 21.6 GiB per process.
- **Resident service:** vLLM kept 23 808 MiB and answered every health check.
- **WER** on the held-out FLEURS test in real streaming:
  - 80 ms: 50.46 → **40.83**.
  - 160 ms: 49.37 → **40.47**.
- **Parity:** ONNX vs NeMo is **0.00 points** at both profiles on 200 clips, 199/200 transcripts identical.
- **Triton:** p50/p95 = 48.8/56.6 ms at 1 stream, but only about 2.2 real-time streams before queueing (p95 867 ms at
  32 streams).
- **Status `partial`:** every acceptance line holds except "Triton serves streaming" at a useful concurrency.
  Phase 5 owns the serving design (proposal 9).

Artifacts left on the host in `~/cadence-spikes/a3`, 17 GB:
- data: wav, manifests and Shar;
- the fine-tuned `.nemo`, logs and TensorBoard files;
- both ONNX exports;
- the per-experiment GPU samples, in `logs/gpu_*.csv`.

The optimizer-state `.ckpt` files (7.66 GB each) and the base export were deleted. The Triton image stays pulled for
phase 5.

### Setup as run
- **Image:** `nvcr.io/nvidia/nemo-speech:26.07` (sha256:b8b1c094f1bb…). NeMo 3.0.0 with Python sources identical to tag
  `v3.0.0` (compared by md5). The image ships no `examples/` or `scripts/`, so these came from the v3.0.0 tarball.
  Also: torch 2.12.0+cu132, Lightning 2.4 / PL 2.6.1, lhotse 1.33, onnx 1.21, CUDA 13.2, driver 580.119.
- **Model:** `nvidia/nemotron-3.5-asr-streaming-0.6b`, revision `ea30d66debe3740a08b573244286791d423d6b3e`, file
  `.nemo`, class `EncDecRNNTBPEModelWithPrompt`.
- **Card:** one RTX PRO 5000 Blackwell with **48 GB**, not 96 GB (47.27 GiB visible to torch, sm_120). `vllm-qwen38`
  holds 23 808 MiB, so the brief's "24 GB" does not fit.
  - The cap was set with `torch.cuda.set_per_process_memory_fraction(0.4337)`, which lets the allocator use
    20.5 GiB.
  - The CUDA context and workspaces sit outside that allocator. Peak per process as nvidia-smi saw it:
    OOMptimizer 21 544 MiB, training 21 646 MiB, streaming eval about 6.4 GB.
- **Resident service untouched:** `gpuwatch.sh` sampled nvidia-smi every 2 s and `curl localhost:16080/v1/models`
  every 30 s during every experiment.
  - vLLM stayed at exactly 23 808 MiB in every sample: 572 in OOMptimizer, 492 in training, 2 894 in parity and Triton.
  - Every health request answered 200. No OOM in any process.

### 0 · Data (20.53 h train, 2.05 h held-out)
| Source | Licence | Used | Clips | Hours |
| --- | --- | --- | --- | --- |
| `google/fleurs` he_il @70bb2e8 | CC-BY-4.0 | train + dev minus 200 | 3 370 | 9.80 |
| `fsicoli/common_voice_17_0` he @8262c16 | CC0-1.0 | train + dev (validated) | 1 221 | 1.52 |
| `ivrit-ai/crowd-recital` @34d93a8 | ivrit.ai v2: CC-BY-4.0 + "AI training or academic research only", no deep-fakes | 480 sessions, cut into ≤ 20 s clips at aligned sentence boundaries | 4 049 | 9.22 |
| FLEURS he_il **test** (held-out) | CC-BY-4.0 | all | 792 | 2.05 |
| FLEURS he_il dev, first 200 (trainer's validation) | CC-BY-4.0 | — | 200 | 0.50 |

- Output formats: NeMo manifests (`lang` = `target_lang` = `he-IL`), plus Lhotse Shar for training (18 shards,
  flac, 1.3 GB).
- Clip durations: p50 7.8 s, p90 15.7 s, p99 19.8 s. 71 FLEURS clips are longer than 20 s; the dataloader's
  `max_duration=20` drops them.
- Tokens per second with the model's tokenizer (text plus the tag): p50 8.5, p90 11.6, p99 16.0.
- **ivrit.ai datasets are gated one repository at a time:**
  - The account had accepted `crowd-recital`, whose files are raw browser `.mka` recordings with Stable-Whisper
    alignments.
  - It had not accepted the ready-cut `crowd-recital-whisper-training`, which returned 403. That repository's
    LICENSE file downloads without acceptance, which is misleading.
  - We did not accept any licence on the user's behalf.

### 1 · OOMptimizer under the cap
Invocation (`oomptimize.py` wraps `scripts/speech_recognition/oomptimizer.py`):
`-n base.nemo --no-ddp -b "[4,6,8,10,12,14,16,18,20]" -r 14 -s 16 -f 0.4337 -y bfloat16`, run with
`PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True`. Result after bucket merging:

| Bucket upper bound (s) | 4 | 6 | 8 | 10 | 12 | 14 | 20 (16, 18 and 20 merged) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Max batch size | 37 | 19 | 11 | 7 | 4 | 3 | 1 |

These are small batches, and the reasons are structural:
- **Fixed cost:** fp32 master weights, gradients and AdamW state for 0.6 B parameters take about 9.6 GB. The measured
  single-utterance step (1 × 20 s) peaks at 14.4 GiB allocated.
- **RNN-T joint:** it holds B_sub × T × U × V with V = 13 088 tokens. `fused_batch_size` is 2, so one fused
  sub-batch at 20 s and 280 tokens takes 5.9 GiB.

Getting more than batch 1 at 20 s under ~20 GiB needs an 8-bit or paged optimizer, bf16 master weights, or a lower
`max_duration`. None of these was tried here.

Three things the stock script does not handle:
1. `EncDecRNNTBPEModelWithPrompt` inherits the 4-tensor `oomptimizer_schema`, but its `training_step` unpacks 5
   (`prompt_indices`). The shim adds the fifth tensor with the he-IL index.
2. When even batch 1 fails, the search halves 1 to `round(0.5) = 0`. An empty STFT then raises
   `CUFFT_INVALID_SIZE`, which OOMptimizer counts as OOM, so it loops forever. The first run did exactly this for
   10 minutes, in the 20 s bucket at the default ratio of 12. The shim stops at 0 and reports the bucket as
   infeasible.
3. The default `--ratio 12` is a guess. Measure it on the data (see step 0); the joint makes memory linear in it.

### 2 · Fine-tune, 500 steps
- **Command:** `speech_to_text_finetune.py` with the `fastconformer_transducer_bpe_streaming_prompt` config and
  `+init_from_nemo_model`; the full override list is in `run_train.sh`.
- **Optimiser:** bf16-mixed, AdamW with weight decay 1e-3, NoamAnnealing (`lr` is a *scale*: 0.1, d_model 1024,
  warmup 100). **Logged peak LR = 3.125e-4 at step 100**; it decays to 1.40e-4 at step 500. Gradient clip 0.5,
  accumulation 1.
- **Prompt:** `unified` prompt mode, the release recipe: 50 % `auto` prompt, 50 % he-IL.
- **Training text:** ends with ` <he-IL>`.
  - The base model emits `<xx-XX>` after the terminal punctuation, and every locale tag is a single tokenizer piece
    (`<he-IL>` → one id), so the targets keep the pretraining format.
  - Decoding strips the tag (`strip_lang_tags`).
  - The model card and the Riva tutorial say only "target_lang on every clip"; appending the tag is our assumption.
- **Throughput: 0.67 s/step training only (1.49 steps/s, range 1.33–1.98 per 10-step window), on 53.8 s of audio
  per step on average.** 500 steps therefore saw about 7.5 h of audio (0.37 epoch).
  - Wall time for 500 steps was 8 min 38 s. That includes two validation passes (200 clips) and three blocking
    `.nemo` saves of 30–60 s each.
- **Memory:** peak allocated 20.01 GiB, reserved 20.51 GiB (at the cap), nvidia-smi 21 646 MiB. No OOM; vLLM
  unchanged.
- **Loss:** per-batch RNN-T loss is a per-utterance sum, so it scales with clip length and jumps between buckets.
  Normalised per audio second of an utterance it fell from a median of 8.7 (steps 1–100) to 7.7 (steps 401–500).
  - Trainer `val_wer`, raw text with langID, on the 200 dev clips: 0.579 at step 250 and 0.562 at step 500.
  - `training_batch_wer` over the last 50 steps: 0.43–0.57.
- **Metrics available during training without touching NeMo:**
  - TensorBoard tags: `train_loss`, `learning_rate`, `global_step`, `train_step_timing in s`,
    `train_backward_timing in s`, `training_batch_wer`, `val_wer`, `validation_step_timing in s`, `epoch`.
  - A Lightning callback, injected by `train.py` by wrapping `Trainer.__init__`, writes JSON lines with step, loss,
    lr, steps/s, batch, audio seconds and peak memory. This is the shape a step kind would stream.
- **Checkpoint layout:** under `exp_dir/<name>/<version>/`:
  - `checkpoints/<name>.nemo`, 2.55 GB: a tar of `model_config.yaml`, `model_weights.ckpt`, `<hash>_tokenizer.model`
    and `<hash>_vocab.txt`.
  - `checkpoints/<name>--val_wer=…-epoch=0.ckpt` and `…-last.ckpt`, 7.66 GB each (weights plus optimizer state).
  - `hparams.yaml`, `cmd-args.log`, `events.out.tfevents.*`, `nemo_error_log.txt`, `lightning_logs.txt`.

### 3 · Streaming WER before and after, FLEURS he_il test (792 clips, 13 725 words)
Decoded with NeMo's `speech_to_text_cache_aware_streaming_infer.py`: chunk by chunk with encoder caches, greedy
RNN-T, `target_lang=he-IL`, `strip_lang_tags=true`, batch 32, fp32.

Normalisation in `wer.py`, applied to reference and hypothesis alike:
- NFKC; niqqud and bidi marks removed.
- Maqaf and dashes become spaces; geresh, gershayim and quotes removed.
- All other punctuation and symbols removed except `%`; lowercase; whitespace collapsed.
- Digits kept as written.

| Latency | Base WER / CER | After 500 steps WER / CER | Raw NeMo WER, base → after |
| --- | --- | --- | --- |
| `[56,0]`, 80 ms | 50.46 / 30.69 | **40.83 / 18.18** | 55.92 → 47.42 |
| `[56,1]`, 160 ms (R20 primary cell) | 49.37 / 29.60 | **40.47 / 17.81** | 54.71 → 46.79 |

- The absolute level is inflated by numbers: the model verbalises them ("באלף שמונה מאות…") where FLEURS writes
  "1889". The scoring normaliser of R21 needs number handling for Hebrew before any gate reads these numbers.
- Decode time on the card: 466 s at 80 ms and 261 s at 160 ms for 2.05 h of audio, i.e. RTF 0.063 and 0.035 at
  batch 32. Peak 5.7 GiB reserved.

### 4 · ONNX export and parity
**The stock export does not work for this model.** `model.export()` writes `encoder` and `decoder_joint`, but the
language prompt is applied between them: one-hot prompt concatenated with the encoder output, then the
`prompt_kernel` MLP (`PromptStreamingMixin._apply_prompt_to_encoded`). The stock encoder ONNX therefore hands the
joint un-prompted features.

`export_onnx.py` exports instead:
- **`encoder_prompt.onnx`:** `ConformerEncoder.forward_for_export` with `export_cache_support`, plus the prompt
  kernel, with `prompt_index` (int64[B]) as an extra input.
  - TorchScript exporter, opset 17, fp32, on CPU; about 2 min per profile.
  - 42 MB graph plus 2.45 GB of external data. The exporter first writes one file per tensor, about 300 files;
    the script re-saves them as a single file.
- **`decoder_joint.onnx`:** NeMo's `RNNTDecoderJoint`, 98 MB, unchanged. Its LSTM states are `[2, B, 640]`, batch
  in dim 1.

`streaming_cfg.json` records the geometry baked into each graph. This is exactly what the family descriptor needs
per latency profile:
- 80 ms: `chunk_size [1,8]` mel frames, `shift_size [1,8]`, `pre_encode_cache_size [0,9]`,
  `drop_extra_pre_encoded 2`, `last_channel_cache_size 56`, `valid_out_len 1`, conv cache 8.
- 160 ms: `chunk_size [9,16]`, `valid_out_len 2`, the rest the same.
- Shared: 128 mel features, 10 ms hop, 16 kHz, subsampling 8 (80 ms per encoder frame), blank = 13 087
  (vocab 13 087 + blank), `max_symbols` 10, he-IL prompt index 64.

**One export per latency profile:** the attention context is baked into the graph. The weights are the same, so it
is 2.5 GB per profile unless the data file is shared.

**Parity, 200 FLEURS test clips** (the first 200 of the held-out manifest), all at `pad_and_drop_preencoded=true`.
This is the mode an exported graph needs, because `drop_extra_pre_encoded` is baked in.

| Profile | Path compared with NeMo streaming | NeMo WER | ONNX WER | Delta | Identical transcripts |
| --- | --- | --- | --- | --- | --- |
| 80 ms `[56,0]` | Triton (ORT CUDA EP) vs NeMo streaming script, batch 1 | 40.71 | 40.71 | **0.00** | 199 / 200 (99.5 %); one letter differs, CER 18.30 vs 18.31 |
| 160 ms `[56,1]` | `parity.py`, ORT CPU vs `conformer_stream_step` on identical chunks | 39.97 | 39.97 | **0.00** | 199 / 200; the one difference is a comma |
| 80 ms `[56,0]` | `parity.py`, ORT CPU (stopped at 41 clips, see below) | — | — | — | 41 / 41 |

Both pass the ≤ 0.1-point acceptance and R31's ≥ 99.5 % identical at its edge, with transcripts rather than token
ids compared.

`parity.py` on CPU is slow: about 12 s per clip at 160 ms and 27 s at 80 ms, with other streams loading the host
(load average up to 37). The image has no onnxruntime, the pip CPU wheel was used, and no CUDA-13 ORT wheel was
available. The 80 ms CPU run was therefore stopped at 41/41 identical, and the 200-clip number for 80 ms comes from
the served Triton path, which is the stronger check anyway.

### 5 · Triton, 80 ms chunks with sequence batching
Setup:
- **Image:** `nvcr.io/nvidia/tritonserver:26.07-py3`, 23.7 GB on disk, pulled.
- **Model repository** (`triton/`, `serve.sh`; listens on 127.0.0.1:18300–18302):
  - `streaming_asr` is a Python backend with the sequence batcher (oldest strategy, START/END/CORRID, up to 64
    streams per batch). It keeps the per-stream encoder caches, LSTM state, last token and hypothesis. It makes one
    batched BLS call to `encoder_prompt`, then batched greedy RNN-T calls to `decoder_joint` (up to 10 symbols per
    frame).
  - `encoder_prompt` and `decoder_joint` are ONNX Runtime models on the CUDA EP, `max_batch_size 0`, full I/O
    declared.
- **Client** (`client.py`): features come from NeMo's streaming buffer on the client, so the preprocessor is not
  served. Each stream sends one chunk per 80 ms in real time. Latency is measured from sending a chunk to receiving
  its tokens. 64 test clips per level, 7 352 chunks.

| Concurrent streams | 1 | 4 | 8 | 16 | 32 |
| --- | --- | --- | --- | --- | --- |
| p50 ms | **48.8** | 124.2 | 283.6 | 441.3 | 541.2 |
| p95 ms | **56.6** | 247.3 | 425.4 | 551.8 | 867.2 |
| p99 ms | 62.7 | 318.0 | 462.0 | 595.6 | 982.0 |

- **Correctness:** WER 42.75 on those 64 clips at every level, with identical output. On 200 clips the served
  transcripts equal NeMo's in 199 cases (step 4).
- **Where the time goes** (Triton statistics over the runs):
  - `encoder_prompt` takes **56.6 ms of compute per execution** at an average batch of 3.3 streams, plus 5.5 ms
    input and 7.4 ms output copies.
  - `decoder_joint` takes 1.1 ms per call, about 1.9 calls per chunk.
  - So one stream fits well inside 80 ms. The fp32 ORT-CUDA encoder saturates at about 2.2 real-time streams on
    this card (total RTF 0.46 at 4–8 streams), and queueing grows from there.
- **Memory:** Triton peaked at 6.3 GB. Maximum total card use was 36.7 GB of 47.8 GB with vLLM beside it. vLLM
  stayed at 23 808 MiB in all 2 894 samples, and all 615 health checks answered 200.

Against R31 (p95 ≤ chunk + 100 ms = 180 ms): **passes at 1 stream, fails at the placeholder of 32 streams.**

The pipeline works end to end; the fp32 ONNX path is not fast enough. What phase 5 should build, cheapest first:
1. Keep the encoder caches on the device: Triton implicit state on the ORT or TensorRT model through the sequence
   batcher, or DLPack. Today about 5.6 MB per stream crosses PCIe and the Python stub twice per chunk.
2. Use a TensorRT (or ORT-TRT) fp16/bf16 engine for the encoder, with a profile for B ≤ 64 and T = 17.
3. Move the greedy loop off Python: TRT-LLM-style decoding, or a C++ backend.
4. Serve the mel front-end as its own model; NeMo can export the preprocessor.

A benchmark under R30 must also hold the card exclusively. These runs shared the host CPU with other streams'
test stacks (load average 3–37).

### Surprises
- **The card is 48 GB, and the cap cannot be 24 GB.** vLLM holds 23.8 GB of the 47.8 GB card, leaving about 23.7
  GB. What works: an allocator cap of 20.5 GiB (fraction 0.4337), which gives a process peak of 21.6 GiB as
  nvidia-smi counts it.
- **Memory is dominated by optimizer state and the RNN-T joint, not by activations.** A 20 s clip only fits at
  batch 1 under the cap (step 1).
- **Two OOMptimizer gaps for prompt models** (step 1): the missing prompt tensor, and the endless loop at batch 0.
- **`speech_to_text_finetune.py` with Lhotse Shar:**
  - `trainer.limit_train_batches` must be an int. With the example config's `is_tarred: true`, a float makes
    `setup_training_data` call `len()` on the Lhotse iterable and crash.
  - Keys absent from the example YAML need `++`/`+` (`use_bucketing`, `shar_path`, `max_tps`, `bucket_*`,
    validation `default_prompt_mode`).
  - The model's own `train_ds` config (in the .nemo) is ignored; the example YAML's `train_ds` applies.
- **A barebones Lightning trainer has `log_every_n_steps=0`,** so calling the prompt model's `training_step`
  directly raises `ZeroDivisionError`. OOMptimizer sets it to 1e6; any calibrate step kind must do the same.
- **Image gaps:**
  - The image's Python lives under `/root`, mode 0700, so containers must run as root. `nemo.sh` chowns outputs back.
  - `torchcodec` cannot load: the image has no FFmpeg libraries. libsndfile reads wav, flac and mp3 but not
    Opus/AAC `.mka`; PyAV was used.
  - No `onnxruntime`, no NeMo `examples/` or `scripts/`.
  - `pip --target` drags `numpy`/`protobuf` copies that shadow the image's; they had to be deleted.
- **Stock ONNX export drops the language prompt** (step 4).
- **The model verbalises numbers** where FLEURS writes digits, so WER needs number normalisation.
- **ivrit.ai gating is per repository,** and a LICENSE file downloads even when the data is gated.
- **exp_manager saves blocking `.nemo` files** of 2.55 GB, 30–60 s each, at every improvement and at the end. The
  7.66 GB `.ckpt` files come as a pair (best plus last).

### What the spec should change (proposals; not edited here)
1. **Compute seed / R12 table** (stream W owns `defaults.yaml`; this is the proposed row). Replace the placeholder
   `blackwell-96gb` row with the measured one:
   ```yaml
   - base_model: base-model/nemotron-3.5-asr-streaming-0.6b
     card_class: blackwell-48gb          # RTX PRO 5000 Blackwell 48 GB, sm_120 (staging)
     memory_cap_gb: 22                   # process peak 21.6 GiB (nvidia-smi); allocator cap 20.5 GiB
     precision: bf16
     seconds_per_step: 0.7               # measured 0.67 s/step (A3), OOMptimizer buckets [4..20 s] -> [37,19,11,7,4,3,1]
     plus_minus: 0.2
     description: Seconds per optimiser step, training only, about 54 s of audio per step; add about 90 s per
       validation pass plus a .nemo save
     source: docs/spikes/A3-nemotron-finetune.md (2026-09-30, 500 steps, 20.5 h Hebrew)
   ```
   Also proposed for R12: an eval row with RTF 0.063 at 80 ms and 0.035 at 160 ms (batch 32 streaming, fp32, this
   card).
2. **Staging memory cap.** The spec and the 07 open question say "24 GB when the staging card is shared" and
   `blackwell-96gb`. The staging card is `blackwell-48gb`, and with vLLM resident the job cap is **22 GB (allocator
   20.5 GiB)**.
   - The step kind should take the cap in GiB and derive the fraction per card, as `cap.py` does.
   - It should count about 1 GB of CUDA context outside the allocator.
   - It should set `PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True`.
3. **`oomptimizer_calibrate`:**
   - Measure `--ratio` (tokens per second, p99) on the dataset version instead of the default 12.
   - Carry the two prompt-model shims until upstream fixes them.
   - Report an infeasible bucket (batch 0) as a result, not a hang.
   - Record buckets and batch sizes on the Run.
4. **`nemotron_finetune` schema:** its `x-cadence` parameters, each with a `defaults.yaml` ref:
   - Expose the peak LR, warmup and d_model, and derive the Noam `lr` scale (`scale = peak × √d_model × √warmup`).
     The scale is not a learning rate, and exposing it invites 100× mistakes.
   - Also expose `prompt_mode` and `unified_auto_ratio`, the language-tag suffix, `max_duration`, `max_tps`,
     `limit_train_batches = steps` and `val_check_interval`.
   - A3 used peak 3.125e-4 and warmup 100 for 500 steps. Whether 3000-step runs keep that peak is for the pack's
     first real run.
5. **Family descriptor (Nemotron 3.5 streaming):**
   - Latency profiles as `att_context_size` [56, r] with r ∈ {0, 1, 3, 6, 13}, and per profile the export geometry
     above (chunk and shift frames, pre-encode cache, drop_extra, cache sizes).
   - Tokenizer: SentencePiece BPE, 13 087 pieces including the `<xx-XX>` locale tags, blank 13 087.
   - Features: 128 mel, 25 ms window, 10 ms hop, 16 kHz, no normalisation.
   - The prompt dictionary: he-IL = 64, auto = 101. The "adaptation-ready" tier needs fine-tuning.
6. **Export step:** the NeMo pack must own a prompt-aware export (`EncoderWithPrompt` in `export_onnx.py`); the stock
   `model.export()` output is wrong for this family. Parity must run at `pad_and_drop_preencoded=true`.
7. **R21:** the Hebrew scoring normaliser needs a number policy (verbalised vs digits) before any gate compares WER
   against references with digits.
8. **Data licences (02 Registry / adoption):** ivrit.ai data carries a use restriction ("AI training or academic
   research only") on top of CC-BY-4.0. Adoption checks need a licence class for "CC-BY with purpose restriction",
   not just an SPDX id.
9. **Triton (phase 5 / R30):** the first working design is in `triton/` (see step 5). The design phase 5 needs is
   described there.
