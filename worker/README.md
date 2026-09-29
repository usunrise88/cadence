# Worker

Runs inside the NeMo Speech NGC container on the staging host. One training slot per card;
memory cap from `CADENCE_GPU_MEMORY_CAP_GB` applied with `torch.cuda.set_per_process_memory_fraction`.

Step kinds live in `cadence_worker/steps/` and register through the `cadence.steps` entry-point group.
Adding a step: one module + one schema. The control plane derives the API operation, the MCP tool and
the generic Pipeline run panel from the schema.

Planned step kinds for phase 2: `sdp_ingest`, `pseudolabel_ensemble`, `dataset_freeze`, `shar_export`,
`oomptimizer_calibrate`, `nemotron_finetune`, `checkpoint_average`, `streaming_eval`, `export_onnx`,
`onnx_parity`, `triton_repo_build`, `latency_benchmark`.
