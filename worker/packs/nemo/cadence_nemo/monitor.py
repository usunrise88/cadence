"""What the training loop reports and decides between optimiser steps, independent of Lightning (the adapter in
:mod:`cadence_nemo.training` calls it from a callback), so the cadence of metrics, the periodic training states and the
stop logic are tested without a card.

- Metrics through the step context every ``log_every`` steps (and at step 1): ``loss``, ``lr``, ``grad_norm``,
  ``throughput_audio_s_per_s`` (audio seconds trained per wall second over the window) and ``gpu_memory_mb`` (peak
  reserved in the window); ``val_wer`` after each validation. Progress is steps done / total.
- A training state every ``state_every_s`` seconds (default 20 minutes, docs/spec/03 "Key defaults"), so a stop can
  release a recent one even when a fresh save would not fit the stop grace.
- Stop (cancel, pause, a closing window): at the next step boundary. A fresh state is saved when the last save took
  less than the grace left (with a margin), else the periodic one is released; with none yet, a fresh save is tried.
"""

from __future__ import annotations

import math
import time
from collections.abc import Callable
from dataclasses import dataclass, field

from cadence_worker.steps.context import StepContext


@dataclass
class Window:
    started: float
    audio_s: float = 0.0
    steps: int = 0


@dataclass
class TrainingMonitor:
    ctx: StepContext
    total_steps: int
    log_every: int
    state_every_s: float
    stop_grace_s: float = 60.0
    clock: Callable[[], float] = time.monotonic
    window: Window = field(init=False)
    last_state_at: float | None = None
    last_state_step: int | None = None
    last_save_s: float | None = None
    stop_seen_at: float | None = None
    stopped: bool = False
    best_wer: float | None = None
    best_step: int | None = None
    last_val_step: int | None = None
    last_wer: float | None = None
    peak_memory_mb: float = 0.0

    def __post_init__(self) -> None:
        self.window = Window(self.clock())

    def batch_audio(self, seconds: float) -> None:
        self.window.audio_s += seconds

    def is_log_step(self, step: int) -> bool:
        return step == 1 or step % max(1, self.log_every) == 0 or step >= self.total_steps

    def step_end(self, step: int, loss: float, lr: float, grad_norm: float | None, memory_mb: float | None) -> None:
        self.window.steps += 1
        if memory_mb is not None:
            self.peak_memory_mb = max(self.peak_memory_mb, memory_mb)
        if self.is_log_step(step):
            now = self.clock()
            elapsed = max(now - self.window.started, 1e-9)
            if math.isfinite(loss):
                self.ctx.metric("loss", loss, step=step)
            self.ctx.metric("lr", lr, step=step)
            if grad_norm is not None and math.isfinite(grad_norm):
                self.ctx.metric("grad_norm", grad_norm, step=step)
            self.ctx.metric("throughput_audio_s_per_s", self.window.audio_s / elapsed, step=step)
            if memory_mb is not None:
                self.ctx.metric("gpu_memory_mb", memory_mb, step=step)
            self.window = Window(now)
        self.ctx.progress(step / max(1, self.total_steps), f"step {step}/{self.total_steps}")

    def validation(self, step: int, wer: float) -> bool:
        """Record a validation WER; True when it is the best so far (the caller keeps those weights)."""
        self.ctx.metric("val_wer", wer, step=step)
        self.last_val_step, self.last_wer = step, wer
        if self.best_wer is None or wer < self.best_wer:
            self.best_wer, self.best_step = wer, step
            return True
        return False

    def state_due(self) -> bool:
        started = self.last_state_at if self.last_state_at is not None else self.window.started
        return self.clock() - started >= self.state_every_s

    def state_saved(self, step: int, seconds: float) -> None:
        self.last_state_at, self.last_state_step, self.last_save_s = self.clock(), step, seconds

    def stop_requested(self) -> bool:
        if self.ctx.should_stop():
            if self.stop_seen_at is None:
                self.stop_seen_at = self.clock()
            return True
        return False

    def save_fresh_on_stop(self) -> bool:
        """At a stop: save a fresh state (True) or release the periodic one (False)."""
        if self.last_state_step is None or self.last_save_s is None:
            return True
        seen = self.stop_seen_at if self.stop_seen_at is not None else self.clock()
        left = self.stop_grace_s - (self.clock() - seen)
        return self.last_save_s * 1.5 + 5 < left
