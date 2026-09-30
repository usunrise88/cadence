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
runtime/*.json           runtime descriptors baked into the images
Dockerfile               runtime nemo-speech: NeMo Speech 26.07 by digest + the harness
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
- `inputs` are paths in the step's scratch directory (a file, or a directory for a directory artifact); `outputs` are
  paths the step creates. An input name may receive several artifacts as `<name>.0`, `<name>.1`, ….
- `ctx` (`StepContext`): `progress(fraction, message)`, `metric(name, value, step, epoch)`, `log(msg, level, **fields)`,
  `should_stop()`, `set_meta(output, meta)` (neutral metadata, R42), `final_metric(name, value)`, `blob(hash)` (read a
  blob an input references), `card`, `memory_cap_mb`, `batch_scale` (0.75 on the OOM retry), `resume_from` (a
  materialised training state), `work_dir`.
- Errors: torch's `OutOfMemoryError` or a CUDA out-of-memory message → `oom` (one retry at 0.75× batch);
  `StepInputError` or invalid parameters → `input`; anything else → `step`.
- Stop: a cancel, pause or closing window reaches the step as SIGTERM → `ctx.should_stop()`. A training step writes its
  `training-state` output and returns; the lease is released `cancelled` with that output only. After
  `CADENCE_STOP_GRACE_SECONDS` (60) the process group is killed.
- Card: GPU steps see their card as device 0 (`CUDA_VISIBLE_DEVICES`), `CADENCE_MEMORY_CAP_MB` holds the lease's cap and
  the harness applies `torch.cuda.set_per_process_memory_fraction` when torch is present. CPU steps get no card.
- Secrets from the lease exist only in the step process's environment and are redacted from forwarded logs; the
  worker's own token and URL are removed from it.

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
python -m cadence_worker.conformance --runtime nemo-speech   # a pack's own image (nightly on the staging card)
```

The suite checks the schemas (complete `x-cadence`, help articles, declared profiles, every required role mapped to a
published kind that declares it), then runs calibrate → train → stop → resume → average → transcribe (every latency
profile; partial events for streaming ones) → score through the real harness path with a local store and no control
plane. Export and parity join in phase 5.

## Development

```
uv sync                         # the harness, the toy pack and CPU PyTorch (dev group)
uv run pytest                   # unit tests with a fake control plane; -m conformance for the full flow
uv run python packs/toy/scripts/make_fixtures.py   # regenerate the toy fixtures (deterministic, CC0)
```

Framework stacks (NeMo, Lhotse, PyTorch for GPUs) come from each runtime's image and are never pinned here.

Core (runtime-neutral) step kinds: `echo` and `dataset_import` (imports a NeMo manifest, a Hugging Face dataset such
as FLEURS, or a folder with `metadata.csv` as a `dataset` artifact; audio helpers in `cadence_worker/audio.py`; help
`docs/help/steps/dataset-import.md`). Help slugs use dashes (`steps.dataset-import`).
