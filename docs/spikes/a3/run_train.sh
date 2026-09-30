#!/usr/bin/env bash
# A3 step 2: 500 steps from the pinned base checkpoint, bf16-mixed, Lhotse Shar, OOMptimizer buckets, under the cap.
#   docs/spikes/a3/run_train.sh [name] [extra hydra overrides...]
# Hydra keys worth lifting into the NeMo pack's `nemotron_finetune` schema are marked (*).
set -euo pipefail
NAME=${1:-a3_ft}; shift || true
HERE=$(cd "$(dirname "$0")" && pwd)
: "${BINS:?BINS=[..] from oomptimize.py}" "${BATCHES:?BATCHES=[..] from oomptimize.py}"
export DOCKER_EXTRA="-e PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True -e A3_EVERY=${A3_EVERY:-10}"
exec "$HERE/nemo.sh" --gpu python /spike/train.py \
  --config-path=/work/NeMo/examples/asr/conf/fastconformer/cache_aware_streaming \
  --config-name=fastconformer_transducer_bpe_streaming_prompt \
  +init_from_nemo_model=/work/model/nemotron-3.5-asr-streaming-0.6b.nemo `# (*) base model version` \
  ++model.train_ds.manifest_filepath=null \
  ++model.train_ds.shar_path=/work/data/shar/train `# (*) dataset version, materialised as Shar` \
  ++model.train_ds.use_bucketing=true \
  "++model.train_ds.bucket_duration_bins=$BINS" "++model.train_ds.bucket_batch_size=$BATCHES" `# (*) from calibrate` \
  ++model.train_ds.batch_duration=null ++model.train_ds.quadratic_duration=null \
  ++model.train_ds.max_duration=20 ++model.train_ds.min_duration=0.3 ++model.train_ds.max_tps=20 \
  ++model.train_ds.num_workers=8 ++model.train_ds.bucket_buffer_size=4000 ++model.train_ds.shuffle_buffer_size=4000 \
  ++model.train_ds.default_prompt_mode=unified ++model.train_ds.unified_auto_ratio=0.5 `# (*) prompt mode` \
  ++model.validation_ds.manifest_filepath=/work/data/manifests/val.json ++model.validation_ds.batch_size=8 \
  ++model.validation_ds.default_prompt_mode=langID \
  ++model.optim.name=adamw ++model.optim.lr=0.1 `# (*) Noam SCALE: peak = 0.1/sqrt(1024)/sqrt(100) = 3.125e-4` \
  ++model.optim.weight_decay=1e-3 ++model.optim.sched.d_model=1024 ++model.optim.sched.warmup_steps=100 \
  ++model.optim.sched.min_lr=1e-6 \
  ++trainer.devices=1 ++trainer.strategy=auto ++trainer.precision=bf16-mixed `# (*)` \
  ++trainer.max_steps=500 ++trainer.max_epochs=-1 ++trainer.limit_train_batches=500 `# (*) steps; must be an int: a float makes setup_training_data call len() on the Lhotse iterable (is_tarred)` \
  ++trainer.val_check_interval=250 ++trainer.check_val_every_n_epoch=null ++trainer.log_every_n_steps=10 \
  ++trainer.gradient_clip_val=0.5 ++trainer.accumulate_grad_batches=1 \
  ++exp_manager.exp_dir=/work/runs ++exp_manager.name="$NAME" ++exp_manager.version=v1 \
  ++exp_manager.use_datetime_version=false ++exp_manager.create_tensorboard_logger=true \
  ++exp_manager.checkpoint_callback_params.save_top_k=1 ++exp_manager.checkpoint_callback_params.always_save_nemo=true \
  "$@"
