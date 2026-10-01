"""NoamAnnealing arithmetic. NeMo's ``optim.lr`` under ``NoamAnnealing`` is a scale, not a learning rate:

    lr(step) = scale · d_model^-0.5 · min(step^-0.5, step · warmup^-1.5)

so the peak, reached at ``step = warmup``, is ``scale · d_model^-0.5 · warmup^-0.5``. The step kind takes the peak (what
people reason about, docs/spec/03 "Key defaults": 2e-4) and derives the scale (spike A3: scale 0.1, d_model 1024,
warmup 100 → peak 3.125e-4).
"""

from __future__ import annotations

import math


def scale_for_peak(peak_lr: float, d_model: int, warmup_steps: int) -> float:
    if peak_lr <= 0 or d_model <= 0 or warmup_steps <= 0:
        raise ValueError("peak_lr, d_model and warmup_steps must be positive")
    return peak_lr * math.sqrt(d_model) * math.sqrt(warmup_steps)


def peak_for_scale(scale: float, d_model: int, warmup_steps: int) -> float:
    return scale / math.sqrt(d_model) / math.sqrt(warmup_steps)


def lr_at(step: int, scale: float, d_model: int, warmup_steps: int, min_lr: float = 0.0) -> float:
    """The learning rate NoamAnnealing gives at ``step`` (1-based), floored at ``min_lr`` after warm-up as NeMo does."""
    s = max(1, step)
    lr = float(scale * d_model**-0.5 * min(s**-0.5, s * warmup_steps**-1.5))
    return max(lr, min_lr) if s > warmup_steps else lr
