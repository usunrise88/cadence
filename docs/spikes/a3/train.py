"""A3 step 2: fine-tune Nemotron 3.5 ASR streaming (0.6B) under the cap with NeMo's speech_to_text_finetune.py.

  docs/spikes/a3/nemo.sh --gpu python /spike/train.py <hydra overrides...>     (see run_train.sh for the exact set)

What this wrapper adds around the unchanged upstream script (examples/asr/speech_to_text_finetune.py, NeMo v3.0.0):
  * cap.apply(): per-process allocator cap before any CUDA work.
  * prints the Noam peak LR computed from the overrides: peak = lr * d_model^-0.5 * warmup^-0.5 (at step=warmup);
    NoamAnnealing's `lr` is a SCALE, not a learning rate - the step kind must expose the peak and derive the scale.
  * A3Metrics callback -> /work/runs/<name>/a3_metrics.jsonl every `A3_EVERY` steps: step, loss, lr, steps/s over
    the window, max allocated/reserved GiB. This is the shape a step kind would stream to the control plane
    (the same values also land in TensorBoard under exp_manager: train_loss, learning_rate, global_step).
"""

from __future__ import annotations

import json
import math
import os
import re
import runpy
import sys
import time

sys.path.insert(0, "/spike")
import cap  # noqa: E402

cap.apply()

import lightning.pytorch as pl  # noqa: E402
import torch  # noqa: E402

EVERY = int(os.environ.get("A3_EVERY", "10"))


def _override(key: str, default: str) -> str:
    for a in sys.argv[1:]:
        m = re.fullmatch(r"\+*" + re.escape(key) + r"=(.*)", a)
        if m:
            return m.group(1)
    return default


class A3Metrics(pl.Callback):
    def __init__(self, path: str) -> None:
        self.path, self.t0, self.s0 = path, None, 0

    def on_train_batch_end(self, trainer, pl_module, outputs, batch, batch_idx):  # type: ignore[no-untyped-def]
        step = trainer.global_step
        if self.t0 is None:
            self.t0, self.s0 = time.time(), step
            torch.cuda.reset_peak_memory_stats()
            return
        if step % EVERY:
            return
        torch.cuda.synchronize()
        now = time.time()
        loss = outputs["loss"].item() if isinstance(outputs, dict) and "loss" in outputs else float("nan")
        rec = {"step": step, "loss": round(loss, 4), "lr": trainer.optimizers[0].param_groups[0]["lr"],
               "steps_per_s": round((step - self.s0) / (now - self.t0), 3),
               "batch": int(batch[0].shape[0]), "audio_s": round(float(batch[1].sum()) / 16000, 1),
               "max_alloc_gib": round(torch.cuda.max_memory_allocated() / 2**30, 2),
               "max_reserved_gib": round(torch.cuda.max_memory_reserved() / 2**30, 2), "t": round(now, 1)}
        self.t0, self.s0 = now, step
        os.makedirs(os.path.dirname(self.path), exist_ok=True)
        with open(self.path, "a") as f:
            f.write(json.dumps(rec) + "\n")
        print("[a3.metrics]", json.dumps(rec), flush=True)


scale = float(_override("model.optim.lr", "2.0"))
d_model = int(_override("model.optim.sched.d_model", "1024"))
warmup = int(_override("model.optim.sched.warmup_steps", "10000"))
print(f"[a3.noam] scale={scale} d_model={d_model} warmup={warmup} "
      f"peak_lr={scale / math.sqrt(d_model) / math.sqrt(warmup):.3e} at step {warmup}", flush=True)

run_dir = os.path.join(_override("exp_manager.exp_dir", "/work/runs"), _override("exp_manager.name", "a3"))
_orig_init = pl.Trainer.__init__


def _init(self, *a, **kw):  # type: ignore[no-untyped-def]
    kw["callbacks"] = [*(kw.get("callbacks") or []), A3Metrics(os.path.join(run_dir, "a3_metrics.jsonl"))]
    _orig_init(self, *a, **kw)


pl.Trainer.__init__ = _init  # type: ignore[method-assign]
sys.argv = ["speech_to_text_finetune.py", *sys.argv[1:]]
try:
    runpy.run_path("/work/NeMo/examples/asr/speech_to_text_finetune.py", run_name="__main__")
finally:
    print(f"[a3.cap] {cap.peak()}", flush=True)
