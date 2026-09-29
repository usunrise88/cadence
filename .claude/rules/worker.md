---
paths:
  - "worker/**"
---
# Worker rules (step kinds, NeMo)

Read docs/spec/03-pipelines-defaults.md ("Pipelines and extension points", "Defaults", "Augmentation") first.

- One step kind = one module in `worker/cadence_worker/steps/`, registered through the `cadence.steps` entry-point group, with `version`, `consumes`, `produces`, `resources`, `params_schema()` (pydantic v2, every field with `x-cadence` default/description/source/range) and `run(params, inputs, outputs)`.
- Steps read inputs and write outputs as artifacts; they never query the database or call the API except to report progress.
- Idempotence: a step declares its input hash; re-running a pipeline skips finished steps.
- GPU: respect `CADENCE_GPU_MEMORY_CAP_GB` (`torch.cuda.set_per_process_memory_fraction`); one training process per card; OOM raises a typed error the runner retries once at 0.75× batch.
- NeMo/Lhotse/PyTorch come from the pinned NeMo Speech container — do not pin them in `pyproject.toml`.
- Training text style follows the base model (punctuated, cased); normalizers and augmentation profiles come from the project's language pack and recipe files, never hard-coded.
- Evaluation is true streaming at deployment latency (`att_context_size` from the eval config); golden sets are never referenced by a run.
- Tests: tiny fixtures (seconds of FLEURS/Common Voice audio) under `worker/tests/fixtures`; no test needs a GPU; mark GPU tests `@pytest.mark.gpu` and keep them out of the default run.
