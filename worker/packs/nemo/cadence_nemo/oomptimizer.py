"""OOMptimizer under the lease's memory cap: the largest batch per duration bucket whose training step and optimiser
update fit, then bucket merging — NeMo's scripts/speech_recognition/oomptimizer.py (v3.0.0) re-implemented here,
because the NeMo Speech image ships no scripts/ and the prompt model needs three fixes the stock script lacks (spike
A3, docs/spikes/a3/oomptimize.py):

1. ``EncDecRNNTBPEModelWithPrompt.training_step`` unpacks five tensors; the profiled batch carries the prompt index of
   the data's language, so the prompt kernel is part of the measurement.
2. When even batch 1 does not fit, upstream halves 1 to 0 and loops forever; here the bucket is reported infeasible
   (batch 0) and dropped, which lowers the usable ``max_duration``.
3. cuFFT failing its workspace under a memory fraction (``CUFFT_INVALID_SIZE`` on sm_120) counts as out of memory.

The search itself (:class:`BatchSearch`, :func:`merge_buckets`, :func:`tokens_per_second`) is plain Python so it is
tested without a card.
"""

from __future__ import annotations

import math
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass
from typing import Any


class BatchSearch:
    """Bisection between the largest batch that worked and the smallest that ran out of memory, as upstream: double
    until the first OOM, then bisect until the gap (min_oom - max_ok) / min_oom ≤ ``threshold`` or is one element."""

    def __init__(self, start: int = 16, threshold: float = 0.05) -> None:
        self.start = max(1, start)
        self.threshold = threshold
        self.reset()

    def reset(self, start: int | None = None) -> None:
        if start is not None:
            self.start = max(1, start)
        self.current = self.start
        self.max_ok: int | None = None
        self.min_err: int | None = None
        self.infeasible = False

    @property
    def result(self) -> int | None:
        """The solution once the search is done (0 when not even one clip fits); None while it runs."""
        if self.infeasible:
            return 0
        if self.max_ok is None or self.min_err is None:
            return None
        gap = self.min_err - self.max_ok
        return self.max_ok if gap / self.min_err <= self.threshold or gap <= 1 else None

    def advance(self, oom: bool) -> bool:
        """Record the outcome at ``current``; True when the search is done."""
        if self.result is not None:
            return True
        if oom:
            self.min_err = self.current if self.min_err is None else min(self.min_err, self.current)
            if self.max_ok is None:
                if self.current <= 1:
                    self.infeasible = True
                    return True
                self.current = max(1, self.current // 2)
            else:
                self.current = (self.max_ok + self.min_err) // 2
        else:
            self.max_ok = self.current if self.max_ok is None else max(self.max_ok, self.current)
            self.current = self.current * 2 if self.min_err is None else (self.max_ok + self.min_err) // 2
        return self.result is not None


@dataclass(frozen=True)
class Bucket:
    max_duration: float
    batch_size: int


def merge_buckets(profile: Sequence[Bucket]) -> list[Bucket]:
    """Drop infeasible buckets (batch 0) and merge neighbours with the same batch size into the upper bound, as
    upstream's merging stage does."""
    out: list[Bucket] = []
    for b in sorted(profile, key=lambda b: b.max_duration):
        if b.batch_size <= 0:
            continue
        if out and out[-1].batch_size == b.batch_size:
            out[-1] = Bucket(b.max_duration, b.batch_size)
        else:
            out.append(b)
    return out


def tokens_per_second(pairs: Iterable[tuple[int, float]], quantile: float = 0.99) -> float | None:
    """The ``quantile`` of tokens per second over (token count, duration) pairs — the output/input ratio OOMptimizer
    sizes the transcript tensors with (upstream guesses 12; A3 measured p99 16 on Hebrew with the tag)."""
    rates = sorted(n / d for n, d in pairs if d > 0)
    if not rates:
        return None
    i = min(len(rates) - 1, max(0, math.ceil(quantile * len(rates)) - 1))
    return rates[i]


def is_oom_error(e: BaseException) -> bool:
    if any(c.__name__ == "OutOfMemoryError" for c in type(e).__mro__):
        return True
    msg = str(e)
    return isinstance(e, RuntimeError) and ("cuFFT error" in msg or "CUDA out of memory" in msg)


def search_buckets(
    bins: Sequence[float],
    ratio: float,
    try_batch: Callable[[int, float, int], bool],
    *,
    start: int = 16,
    threshold: float = 0.05,
    log: Callable[[str], None] | None = None,
    should_stop: Callable[[], bool] | None = None,
) -> list[Bucket]:
    """Search every bucket from the longest down (each result doubles as the next start), calling ``try_batch(batch,
    seconds, tokens)`` → True when it ran out of memory."""
    search = BatchSearch(start, threshold)
    found: list[Bucket] = []
    for seconds in sorted(bins, reverse=True):
        tokens = max(1, math.ceil(ratio * seconds))
        while True:
            if should_stop and should_stop():
                return found
            oom = try_batch(search.current, seconds, tokens)
            if log:
                log(f"bucket ≤ {seconds:g} s: batch {search.current} {'OOM' if oom else 'ok'}")
            if search.advance(oom):
                break
        best = search.result or 0
        found.append(Bucket(float(seconds), best))
        search.reset(start=max(1, best * 2))
    return found


def profile_step(
    model: Any, optimizer: Any, prompt_index: int, sample_rate: int, vocab: int
) -> Callable[[int, float, int], bool]:
    """``try_batch`` for a NeMo prompt model on the card: one synthetic training step and optimiser update at the
    bucket's longest clip and transcript (random audio, random tokens, the language's prompt index)."""
    import torch

    def run(batch: int, seconds: float, tokens: int) -> bool:
        samples = int(seconds * sample_rate)
        data: tuple[Any, ...] = ()
        try:
            data = (
                torch.randn(batch, samples, device="cuda"),
                torch.full((batch,), samples, dtype=torch.long, device="cuda"),
                torch.randint(1, vocab, (batch, tokens), device="cuda"),
                torch.full((batch,), tokens, dtype=torch.long, device="cuda"),
                torch.full((batch,), prompt_index, dtype=torch.long, device="cuda"),
            )
            optimizer.zero_grad(set_to_none=True)
            out = model.training_step(data, 0)
            out["loss"].sum().backward()
            optimizer.step()
            return False
        except (RuntimeError, MemoryError) as e:
            if not is_oom_error(e):
                raise
            return True
        finally:
            del data
            optimizer.zero_grad(set_to_none=True)
            torch.cuda.synchronize()
            torch.cuda.reset_peak_memory_stats()

    return run
