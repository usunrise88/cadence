"""The training resampler, streamed: SciPy's polyphase ``resample_poly`` (the import resampler of
:mod:`cadence_worker.audio`), computed frame by frame, and the phone line built from it (R50, spike A5 finding 7).

:class:`StreamingResampler` gives the same samples (to float rounding) as ``resample_poly`` over the whole signal,
whatever the frame split: it keeps the input window that later outputs still depend on and emits only outputs no
further input can change, so it holds back less than 1 ms at 44.1 or 48 kHz → 16 kHz. :class:`Telephony` is
16 kHz → 8 kHz → G.711 (μ-law or A-law) → 16 kHz with it, the same chain the training augmentation's telephone stage
uses, so a live "telephony simulation" equals what training saw.
"""

from __future__ import annotations

import math
from typing import Literal

import numpy as np
from numpy.typing import NDArray

Audio = NDArray[np.float32]
Codec = Literal["ulaw", "alaw", "none"]

RESAMPLER = "streaming polyphase (scipy.signal.resample_poly), the import and training resampler"


def resample_poly(x: NDArray[np.floating], src: int, dst: int) -> Audio:
    """The whole-signal polyphase resampler (SciPy's defaults: Kaiser window, β 5, half length 10 x max(up, down))."""
    from scipy import signal

    if src == dst or x.size == 0:
        return np.asarray(x, dtype=np.float32)
    g = math.gcd(src, dst)
    y = signal.resample_poly(np.asarray(x, dtype=np.float64), dst // g, src // g)
    return np.asarray(y, dtype=np.float32)


class StreamingResampler:
    """``resample_poly(x, up, down)`` computed frame by frame: identical output (to float rounding) for any split of
    the input, with a look-ahead of the filter's half length."""

    def __init__(self, src: int, dst: int) -> None:
        if src <= 0 or dst <= 0:
            raise ValueError("sample rates must be positive")
        g = math.gcd(src, dst)
        self.src, self.dst = src, dst
        self.up, self.down = dst // g, src // g
        self.identity = src == dst
        # resample_poly's filter has half length 10 * max(up, down) taps in the upsampled domain: in input samples
        # that is ceil(10 * max / up); one more down-step and a little slack cover the polyphase alignment.
        self.margin = math.ceil(10 * max(self.up, self.down) / self.up) + self.down + 2
        self.buf = np.zeros(0, dtype=np.float64)
        self.base = 0  # absolute input index of buf[0] (a multiple of down)
        self.emitted = 0  # absolute output samples emitted
        self.total_in = 0

    def _run(self, final: bool) -> Audio:
        from scipy import signal

        if self.buf.size == 0:
            return np.zeros(0, dtype=np.float32)
        y = signal.resample_poly(self.buf, self.up, self.down)
        first_abs = self.base * self.up // self.down  # output sample m of this window is absolute first_abs + m
        if final:
            end_abs = math.ceil(self.total_in * self.up / self.down)
        else:
            safe_in = self.total_in - self.margin  # inputs beyond this still change the outputs before it
            end_abs = max(self.emitted, math.floor(safe_in * self.up / self.down))
        out = y[self.emitted - first_abs : end_abs - first_abs]
        self.emitted = end_abs
        need_from = (self.emitted * self.down) // self.up - self.margin
        need_from = max(self.base, (need_from // self.down) * self.down)
        self.buf = self.buf[need_from - self.base :]
        self.base = need_from
        return np.asarray(out, dtype=np.float32)

    def push(self, x: NDArray[np.floating]) -> Audio:
        if self.identity:
            return np.asarray(x, dtype=np.float32)
        self.buf = np.concatenate([self.buf, np.asarray(x, dtype=np.float64)])
        self.total_in += int(x.size)
        return self._run(False)

    def flush(self) -> Audio:
        """The outputs held back for look-ahead (call once, at the end of the signal)."""
        if self.identity:
            return np.zeros(0, dtype=np.float32)
        return self._run(True)


MU = 255.0
A = 87.6


def mulaw(x: NDArray[np.floating]) -> Audio:
    """G.711 μ-law: compand, quantise to 8 bits, expand."""
    c = np.sign(x) * np.log1p(MU * np.minimum(np.abs(x), 1.0)) / np.log1p(MU)
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    return np.asarray(np.sign(q) * ((1 + MU) ** np.abs(q) - 1) / MU, dtype=np.float32)


def alaw(x: NDArray[np.floating]) -> Audio:
    """G.711 A-law: compand, quantise to 8 bits, expand."""
    ax = np.minimum(np.abs(x), 1.0)
    small = ax < 1 / A
    c = np.where(small, A * ax / (1 + np.log(A)), (1 + np.log(np.maximum(A * ax, 1e-12))) / (1 + np.log(A)))
    c = np.sign(x) * c
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    aq = np.abs(q)
    y = np.where(aq < 1 / (1 + np.log(A)), aq * (1 + np.log(A)) / A, np.exp(aq * (1 + np.log(A)) - 1) / A)
    return np.asarray(np.sign(q) * y, dtype=np.float32)


def codec(x: NDArray[np.floating], name: str) -> Audio:
    if name == "ulaw":
        return mulaw(x)
    if name == "alaw":
        return alaw(x)
    if name == "none":
        return np.asarray(x, dtype=np.float32)
    raise ValueError(f"unknown codec {name!r}")


class Telephony:
    """A phone line, streamed: ``rate`` → ``line_rate`` (polyphase) → codec → ``rate`` (polyphase)."""

    def __init__(self, codec_name: Codec = "ulaw", line_rate: int = 8000, rate: int = 16000) -> None:
        codec(np.zeros(1), codec_name)  # refuse an unknown codec at once
        self.codec = codec_name
        self.line_rate = line_rate
        self.down = StreamingResampler(rate, line_rate)
        self.up = StreamingResampler(line_rate, rate)

    @property
    def chain(self) -> str:
        c = {"ulaw": "G.711 μ-law", "alaw": "G.711 A-law", "none": "no codec"}[self.codec]
        return f"16 kHz → {self.line_rate // 1000} kHz (polyphase) → {c} → 16 kHz (polyphase)"

    def push(self, x: NDArray[np.floating]) -> Audio:
        return self.up.push(codec(self.down.push(x), self.codec))

    def flush(self) -> Audio:
        a = self.up.push(codec(self.down.flush(), self.codec))
        return np.concatenate([a, self.up.flush()]).astype(np.float32)


def telephone(x: NDArray[np.floating], codec_name: str, line_rate: int = 8000, rate: int = 16000) -> Audio:
    """The whole-signal phone line (training augmentation): the same chain as :class:`Telephony`."""
    low = resample_poly(x, rate, line_rate)
    return resample_poly(codec(low, codec_name), line_rate, rate)
