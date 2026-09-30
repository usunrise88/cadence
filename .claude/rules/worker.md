---
paths:
  - "worker/**"
---
# Worker rules (harness, step kinds, packs)

Read docs/spec/03-pipelines-defaults.md ("Pipelines and extension points", "Defaults", "Augmentation") and
worker/README.md ("The step contract") first.

- One step kind = one module, registered through the `cadence.steps` entry-point group, with `version`, `consumes` and `produces` (name → artifact type), `resources` (gpu, gpus, memoryGb, diskGb, jobKind), optional `role`, `runtime`, `neutral`, `secrets`, a pydantic v2 `Params` whose every field is built with `cadence_field` (`x-cadence` default/description/source/range, or `default_ref` into defaults.yaml) and `run(params, inputs, outputs, ctx)`. Core neutral kinds live in `worker/cadence_worker/steps/`; framework kinds in their pack (`worker/packs/<pack>/`).
- Defaults live only in `control-plane/defaults/defaults.yaml` (pack sections under `packs.<pack>`); never copy a value into the worker.
- Steps read inputs and write outputs as artifacts; they never query the database or call the API — progress, metrics, logs and meta go through the `StepContext`.
- Idempotence: a step declares its input hash; re-running a pipeline skips finished steps.
- GPU: the harness sets `CUDA_VISIBLE_DEVICES` and `CADENCE_MEMORY_CAP_MB` and applies `torch.cuda.set_per_process_memory_fraction`; one training process per card; OOM surfaces as error type `oom` and the runner retries once at 0.75× batch (`ctx.batch_scale`).
- Training steps honour `ctx.should_stop()` by writing their `training-state` output and returning, and resume from `ctx.resume_from`.
- NeMo/Lhotse/PyTorch come from the pinned NeMo Speech container — do not pin them in `pyproject.toml` (the CPU toy pack pins CPU torch in its own image only).
- The worker protocol types are generated (`protocol_gen.py`, `make gen`); never hand-write them.
- Training text style follows the base model (punctuated, cased); normalizers and augmentation profiles come from the project's language pack and recipe files, never hard-coded.
- Evaluation is true streaming at deployment latency (a latency profile of the family); golden sets are never referenced by a run.
- Tests: tiny fixtures (seconds of FLEURS/Common Voice audio, or synthesized, licence noted) under the pack or `worker/tests/fixtures`; no test needs a GPU; mark GPU tests `@pytest.mark.gpu`. Every pack passes `python -m cadence_worker.conformance --runtime <runtime>`.
