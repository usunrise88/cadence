# Worker

The worker runs pipeline steps for the control plane. One process lives in one runtime (R40): it registers its
runtime, step kinds and model families (`workerRegistrations.new`), long-polls for leases (`workerLeases.claim`), runs
each lease in its own subprocess, sends a heartbeat with progress and card telemetry (`workerLeases.report`), forwards
logs and metrics in batches (`workerLogs.new`, `workerMetrics.new`) and releases the lease with its outputs hashed into
the content store (`workerLeases.release`). The protocol is `api/openapi.yaml`, tag `worker`; its types are generated
into `cadence_worker/protocol_gen.py` by `make gen`.

```
cadence_worker/          the harness and the runtime-neutral core step kinds (every image)
  serve.py               claim loop, heartbeats, log/metric batches, release, shutdown
  executor.py            one lease: materialise inputs, spawn run_step, stop, hash outputs
  run_step.py            the step subprocess (python -m cadence_worker.run_step <lease dir>)
  cas.py                 the shared content store (BLAKE3, b3/<ab>/<hash>, manifests) — mirrors control-plane/internal/cas
  steps/base.py          the step contract; steps/context.py the StepContext; steps/echo.py the template kind
  registry.py            entry points, runtime descriptor; defaults.py reads defaults.yaml
  conformance/           the framework-pack conformance suite (R45)
packs/toy/               the CPU toy pack (runtime toy, family toy-ctc) — its own distribution, cadence-toy
packs/nemo/              the NeMo pack (runtime nemo-speech, family nemo.fastconformer-rnnt.cache-aware: Nemotron 3.5
                         streaming) — distribution cadence-nemo, installed in the nemo-speech image
runtime/*.json           runtime descriptors baked into the images
Dockerfile               runtime nemo-speech: NeMo Speech 26.07 by digest + the harness + the NeMo pack
Dockerfile.toy           runtime toy: python:3.12-slim + CPU PyTorch + the harness + the toy pack
```

## Running

`python -m cadence_worker serve` reads `CADENCE_URL`, `CADENCE_WORKER_TOKEN_FILE` (the `cwk_` token the control plane
writes at start; re-read on every call), `CADENCE_CAS_DIR`, `CADENCE_WORKER_HOST`, `CADENCE_WORKER_SCRATCH` (on the
store's file system, so inputs are hard links) and the runtime descriptor from `CADENCE_RUNTIME` (JSON) or
`CADENCE_RUNTIME_FILE` (default `/etc/cadence/runtime.json`); the rest is in `cadence_worker/config.py`. Compose runs
the GPU worker under the `gpu` profile and the toy worker under `toy`:

```
docker compose --profile gpu up -d worker        # nemo-speech on the staging card
docker compose --profile toy up -d worker-toy    # CPU, for trying the seams end to end
```

`python -m cadence_worker registry [RUNTIME]` and `families [RUNTIME]` print what a worker of that runtime publishes.

Live sessions (phase 3 · stream T, R47–R50): a lease whose `resources.jobKind` is `interactive` runs a family's `live`
role kind. The harness adds `CADENCE_LIVE_URL` (the control plane's `/api/worker-live/{jobId}`, `ws://` or `wss://`) to
the step's environment, and the lease's secret env carries `CADENCE_LIVE_TOKEN`; the kind loads its targets, dials the
relay with that token (header `Cadence-Live-Token`) and serves the session with `cadence_worker/live.py` (the live
channel's messages, the streaming polyphase resampler and the phone line of `cadence_worker/resample.py`; files decoded
with ffmpeg when the image has it). The worker never hands a step its own credential. Every ten minutes the claim loop
removes scratch directories of leases it no longer runs that are older than `transcriptions.scratch_sweep_minutes`
(60): what a crashed step process left, a session's uploaded file included.

## The step contract

A step kind is a class registered under the `cadence.steps` entry-point group:

```python
class TrainStep:
    version = "1"
    consumes = {"data": "dataset"}  # input name → artifact type
    produces = {"checkpoint": "checkpoint", "state": "training-state"}
    resources = {"gpu": True, "gpus": 1, "memoryGb": 24, "jobKind": "training"}
    role = "train"  # the model-family role it fills; omit for neutral kinds
    runtime = "toy"  # framework kinds name their runtime; neutral core kinds set neutral = True instead
    secrets = ("HF_TOKEN",)  # secret names, injected as env into the step process only
    Params = TrainParams  # pydantic v2; every field built with cadence_field(...)

    def run(self, params, inputs, outputs, ctx): ...
```

- Every parameter carries `x-cadence` {default, description, source, range}. `cadence_field(default_ref="packs.toy.
  train_steps")` takes default, source and range from `control-plane/defaults/defaults.yaml` (the single source; the
  images copy it unchanged to `/opt/cadence/defaults.yaml`); ranges are enforced before `run`.
- `optional_outputs` (a frozenset of output names, published as `optionalOutputs`) lists outputs a successful step may
  leave unwritten — a train step's final training state; no pipeline may wire them into another step.
- `optional_inputs` (published as `optionalInputs`) lists inputs of `consumes` a pipeline may leave unwired — a
  transcribe step's `boost` list; the step then finds no such key in `inputs`.
- `inputs` are paths in the step's scratch directory (a file, or a directory for a directory artifact); `outputs` are
  paths the step creates. An input name may receive several artifacts as `<name>.0`, `<name>.1`, ….
- `ctx` (`StepContext`): `progress(fraction, message)`, `metric(name, value, step, epoch)`, `log(msg, level, **fields)`,
  `should_stop()`, `set_meta(output, meta)` (neutral metadata, R42), `final_metric(name, value)`, `blob(hash)` (read a
  blob an input references), `card`, `memory_cap_mb`, `batch_scale` (0.75 on the OOM retry), `resume_from` (a
  materialised training state), `work_dir`, and `publish(output, path, meta, metrics)`: an intermediate instance of an
  output (a validation checkpoint) the control plane registers at once (`workerOutputs.new`); write each one to its own
  path under `work_dir` and leave it alone, the harness stores and removes it.
- Errors: torch's `OutOfMemoryError` or a CUDA out-of-memory message → `oom` (one retry at 0.75× batch);
  `StepInputError` or invalid parameters → `input`; anything else → `step`.
- Stop: a cancel, pause or closing window reaches the step as SIGTERM → `ctx.should_stop()`. A training step writes its
  `training-state` output and returns; the lease is released `cancelled` with that output only. After
  `CADENCE_STOP_GRACE_SECONDS` (60) the process group is killed.
- Card: GPU steps see their card as device 0 (`CUDA_VISIBLE_DEVICES`), `CADENCE_MEMORY_CAP_MB` holds the lease's cap and
  the harness applies `torch.cuda.set_per_process_memory_fraction` when torch is present. CPU steps get no card.
- Secrets from the lease exist only in the step process's environment; the worker's own token and URL are removed from
  it. Everything the step reports (log lines and fields, progress messages, output meta, the error message) has each
  secret value and every credential-shaped token (`cdk_`/`cst_`/`cwk_`/`cah_`/`cep_`, `hf_`, `Bearer …`) replaced by
  `[redacted]` before it leaves the worker (`cadence_worker.sanitize`).
- Non-finite numbers (NaN, ±inf: `val_wer` = 0/0) never leave the worker: such a metric point is dropped with one
  warning line per name, such a publication or outcome metric is dropped with a warning line, and in meta it becomes
  `null`. The client serialises with `allow_nan=False`.

Adding a step: one module, its schema, `docs/help/steps/<kind>.md` (underscores become dashes: `toy_train` →
`steps.toy-train`), an entry point. A runtime-neutral core kind (such
as `echo`, and `dataset_import` next) is listed in this package's `pyproject.toml`; a framework kind belongs to its
pack's distribution.

## Framework packs and the conformance suite

A pack (R45) is a distribution with entry points `cadence.steps` (its role kinds, each with `runtime = "<runtime>"`)
and `cadence.families` (a `cadence_worker.registry.Family`: runtime, `ModelFamilyDescriptor`, fixtures and per-stage
conformance parameters), a `packs.<pack>` section in defaults.yaml, a runtime image and help articles.

```
make conformance                                          # the toy pack (CI, every pull request)
python -m cadence_worker.conformance --runtime nemo-speech --memory-cap-mb 22528 --help-dir docs/help
                                                          # the NeMo pack in its image on the card (nightly.yml)
```

Inputs are filled by declared artifact type, as the control plane fills a run's: `dataset` (the imported fixtures),
`mix` (a `cadence.mix/1` over them), `base_model` (from the family's `conformance["base_model"]`), `calibration` (the
calibrate stage's output), `checkpoint`. `--memory-cap-mb` is the card cap a lease would carry (the NeMo run shares
the staging card with vLLM).

The NeMo pack (`packs/nemo`, help `docs/help/guides/nemo-pack.md`): `oomptimizer_calibrate`, `nemotron_finetune`,
`checkpoint_average`, `nemotron_transcribe` (version 3: NeMo's cache-aware streaming pipeline, `pipeline.py`, with
per-stream phrase boosting from an optional `boost_list`, `cadence_worker/boost.py`), `checkpoint_from_base` (role
`materialize`), `nemotron_live` (role `live`, the same decoder), defaults `packs.nemo`. Its pure parts (mix reading,
Noam arithmetic, averaging, augmentation, the OOMptimizer search, the training monitor, hypotheses, the pipeline
decoder's events) are unit-tested here without NeMo; the NeMo glue (`training.py`, `pipeline.py`, `streaming.py` — the
cache-aware loop of transcribe versions 1–2, kept for its boost list type and comparisons — `nemo_data.py`) runs in
the image. `CADENCE_NEMO_DEVICE=cpu` lets the
train and transcribe steps run on a CPU (slowly, fp32) to check the glue without a card — development only.

The suite checks the schemas (complete `x-cadence`, help articles, declared profiles, every required role mapped to a
published kind that declares it), then imports the pack's fixtures (a `folder-csv` folder with `metadata.csv`) with
`dataset_import` and runs calibrate → train → stop → resume → average → transcribe (every latency
profile; partial events for streaming ones, each transcription scored by `wer_score`) → baseline → materialize the base
model → transcribe it → score through the real harness path with a local store and no control plane. Export and
parity join in phase 5.

## Development

```
uv sync                         # the harness, the toy pack and CPU PyTorch (dev group)
uv run pytest                   # unit tests with a fake control plane; -m conformance for the full flow
uv run python packs/toy/scripts/make_fixtures.py   # regenerate the toy fixtures (deterministic, CC0)
```

Framework stacks (NeMo, Lhotse, PyTorch for GPUs) come from each runtime's image and are never pinned here.

Core (runtime-neutral) step kinds: `echo` and `dataset_import` (imports a NeMo manifest, a Hugging Face dataset such
as FLEURS, or a folder with `metadata.csv` as a `dataset` artifact; audio helpers in `cadence_worker/audio.py`; help
`docs/help/steps/dataset-import.md`) and `wer_score` (hypotheses + dataset + scoring normalizer → `scores`: WER, CER,
S/D/I, duration buckets, partial stability; the normalizer interpreter is `cadence_worker/normalize.py`, the
alignment `cadence_worker/align.py`; help `docs/help/steps/wer-score.md`) and `pseudolabel_ensemble` (members'
hypotheses + `segments` + scoring normalizer + optional `lid` → `segments` with pseudo-labels or disputes and
`hypotheses`; `cadence_worker/segments.py` reads and writes `cadence.segments/1`, `cadence_worker/members.py` holds what
the members share; help `docs/help/steps/pseudolabel-ensemble.md`) and `segments_cut` (the segments that need a label,
cut from their mount into the `dataset` the members read, `purpose: pseudo-label`; help
`docs/help/steps/segments-cut.md`). Help slugs use dashes (`steps.dataset-import`).

Auxiliary models (phase 4, R26): a parameter built with `cadence_field(registry_ref={"kind": "auxiliary", "role": …})`
names an auxiliary version; the control plane resolves it to the version the project adopted and the step reads it
with `ctx.auxiliary(param)` (`{versionId, name, version, payload}`). The NeMo pack loads Whisper per job for
`whisper_transcribe` and `lid_classify` (`cadence_nemo/whisper.py`); the services pack (`packs/services`, runtime
`services`, CPU, `Dockerfile.services`, compose profile `services`) calls running services: `oasis_transcribe`, a gRPC
client of the OASIS contract vendored under `packs/services/proto/` (source commit in `proto/SOURCE.yaml`; regenerate
with `uv run python packs/services/scripts/gen_proto.py`). A service that does not answer raises
`AuxiliaryUnavailable` (error type `step`, retryable, message `auxiliary-unavailable: …`). Guide:
`docs/help/guides/auxiliary-models.md`.
