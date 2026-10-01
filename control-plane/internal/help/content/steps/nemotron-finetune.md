---
title: nemotron_finetune (step kind)
summary: The Nemotron family's train role — fine-tune from a base model or a checkpoint on a mix in bf16 with calibrated buckets, the language prompt and telephony augmentation; posts metrics, writes checkpoints and a training state, stops and resumes.
contexts: [step:nemotron_finetune, artifact:checkpoint, artifact:training-state, family:nemo.fastconformer-rnnt.cache-aware]
---

## What this is

`nemotron_finetune@1` fills the `train` role of the Nemotron 3.5 streaming family (runtime `nemo-speech`, one card,
job kind `training`). Inputs:

- `base` — a `base_model` artifact (Hugging Face repository and pinned revision; the `.nemo` is fetched into the
  worker's Hugging Face cache) or a `checkpoint` (a run that starts from a checkpoint, R44);
- `data` — the run's `mix` artifact: every dataset version it names is read from the content store in place; groups
  are sampled by their probability, datasets inside a group by their hours; clips of split `train` between
  `min_duration` and `max_duration` train, the `validation` split (first `val_max_utterances`) validates;
- `calibration` — `oomptimizer_calibrate`'s buckets, scaled by the OOM retry's batch scale and by the slowest speed
  perturbation.

Training: NeMo's model with Lightning, precision from `precision` (bf16-mixed by default), AdamW with NoamAnnealing.
You give the **peak** learning rate; the Noam scale is derived (`peak · √d_model · √warmup`) and both are logged,
posted as final metrics (`peak_lr`, `noam_scale`) and kept in the checkpoint's train arguments. Each clip's language
prompt comes from its dataset language (`he` or `he-IL` → the model's `he-IL`) unless `target_lang` is set; in
`unified` prompt mode half the clips get `auto`. Training text keeps the base model's style and ends with the locale
tag when the tokenizer has it as one piece; decoding strips it. The `augmentation` profile runs on the fly in the
dataloader workers (band-limit to 8 kHz with G.711 μ-law/A-law or GSM, level jitter, speed 0.95–1.05); `{profile:
clean}` turns it off.

Metrics during training (every `log_every` steps): `loss` (the RNN-T loss, a per-utterance sum, so it moves with clip
length), `lr`, `grad_norm` (before clipping), `throughput_audio_s_per_s`, `gpu_memory_mb`; `val_wer` after every
validation (every `val_every` steps and at the end; raw text, language tags stripped).

Outputs:

- `checkpoint` — the weights at the end (`model.nemo` + `checkpoint.json`: family, step, valWer, weightsHash, the base
  model, the tokenizer reference and the train arguments);
- `checkpoint_best` — the best validation pass of this lease (the same artifact when that was the last pass); the
  control plane ranks every checkpoint by validation WER and keeps the top k;
- `state` — `training-state` (`last.ckpt` with optimiser, scheduler and loop state, `state.json`), only for resuming.

Every validation pass before the last also saves its checkpoint and publishes it while training runs, so each one is
registered on the run at once (and survives a pause or a closing window); `checkpoint_best` links the published one,
so it is the same artifact. A training state is also written every `state_every_minutes`. Asked to stop (cancel, pause, a closing window), the step
stops at the next optimiser step and releases a training state — a fresh one when the last save was quick enough for
the stop grace, else the periodic one; the lease is released cancelled with it. `overrides.resumeFrom` continues from
a state up to `steps` in total. A card out-of-memory is an `oom` error; the retry runs at 0.75× the bucket batches.

## Place in the loop

Train — the train step of `pipelines/train-stage` (`runs.new`, `runs.stage`, `runs.resume`).

## Fields and defaults

| Parameter | Default | Source | Range |
| --- | --- | --- | --- |
| `steps` | `training.steps` (3000) | Key defaults | 1–200000 |
| `seed` | `packs.nemo.seed` (0) | Cadence recommendation | 0–2147483647 |
| `precision` | `training.precision` (bf16) | Key defaults | bf16, fp16, fp32 |
| `peak_lr` | `packs.nemo.peak_lr` (2e-4) | Key defaults | 1e-6–2e-3 |
| `warmup_steps` | `packs.nemo.warmup_steps` (100) | Key defaults | 1–20000 |
| `min_lr` | `packs.nemo.min_lr` (1e-6) | Base model config | 0–1e-3 |
| `weight_decay` | `packs.nemo.weight_decay` (0.001) | Base model config | 0–0.1 |
| `grad_clip` | `packs.nemo.grad_clip` (5) | Key defaults | 0–100 |
| `val_every` | `packs.nemo.val_every` (500) | Cadence recommendation | 1–200000 |
| `val_max_utterances` | `packs.nemo.val_max_utterances` (200) | Spike A3 | 1–5000 |
| `val_batch_size` | `packs.nemo.val_batch_size` (8) | Spike A3 | 1–64 |
| `log_every` | `packs.nemo.log_every` (10) | Spike A3 | 1–1000 |
| `state_every_minutes` | `packs.nemo.state_every_minutes` (20) | Key defaults | 1–240 |
| `max_duration` | `packs.nemo.max_duration` (20 s) | Spike A3 | 1–40 s |
| `min_duration` | `packs.nemo.min_duration` (0.5 s) | Key defaults | 0.05–10 s |
| `num_workers` | `packs.nemo.num_workers` (4) | Cadence recommendation | 0–32 |
| `prompt_mode` | `packs.nemo.prompt_mode` (unified) | Release recipe | unified, langID, auto |
| `unified_auto_ratio` | `packs.nemo.unified_auto_ratio` (0.5) | Release recipe | 0–1 |
| `target_lang` | `packs.nemo.target_lang` (from the data) | Cadence recommendation | a prompt key |
| `augmentation` | `packs.nemo.augmentation` (telephony) | spec 03 "Augmentation" | a profile |
| `cuda_context_reserve_mb` | `packs.nemo.cuda_context_reserve_mb` (1024) | Spike A3 | 0–8192 MiB |

## Commands

`runs.new`, `runs.stage`, `runs.resume`, `jobs.pause|resume`, `pipelineRuns.cancel`, `metrics.get`, `checkpoints.list`.

## Playbooks

Fine-tune from a dataset version; adapt to a new language.

## Sources

- Spike A3 (`docs/spikes/A3-nemotron-finetune.md` step 2): every Hydra key of the run, the Noam scale, the prompt mode.
- docs/spec/03-pipelines-defaults.md "Key defaults" and "Augmentation"; docs/spec/08-resolutions.md R41–R44.
