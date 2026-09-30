"""Summarise a NeMo run's TensorBoard scalars (what a step kind can tail during training without NeMo changes).

  docs/spikes/a3/nemo.sh python /spike/tb_summary.py /work/runs/a3_ft/v1
Tags NeMo 3.0 writes for this model: train_loss, learning_rate, global_step, train_step_timing in s, val_wer,
training_batch_wer (every log_every_n_steps), epoch. Prints the tags and a windowed loss curve.
"""

import sys

import numpy as np
from tensorboard.backend.event_processing.event_accumulator import EventAccumulator

ea = EventAccumulator(sys.argv[1], size_guidance={"scalars": 0})
ea.Reload()
tags = ea.Tags()["scalars"]
print("tags:", tags)
loss = [(e.step, e.value) for e in ea.Scalars("train_loss")]
steps, vals = np.array([s for s, _ in loss]), np.array([v for _, v in loss])
for lo in range(0, int(steps.max()) + 1, 100):
    m = (steps >= lo) & (steps < lo + 100)
    if m.any():
        print(f"steps {lo:>3}-{lo + 99:<3} train_loss mean={vals[m].mean():7.2f} median={np.median(vals[m]):7.2f} n={m.sum()}")
for tag in ("val_wer", "training_batch_wer"):
    if tag in tags:
        print(tag, [(e.step, round(e.value, 4)) for e in ea.Scalars(tag)][-6:])
if "train_step_timing in s" in tags:
    t = np.array([e.value for e in ea.Scalars("train_step_timing in s")])
    print(f"train_step_timing s: median={np.median(t):.3f} mean={t.mean():.3f} p90={np.percentile(t, 90):.3f}")
