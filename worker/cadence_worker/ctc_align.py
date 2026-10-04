"""CTC forced alignment in NumPy (phase 4 · stream L; R51, R54): the most likely CTC path through a model's per-frame
log-probabilities that emits exactly a given token sequence (Viterbi over the blank-extended targets), and the frame
spans of each token and word along it.

The definition is torchaudio's ``functional.forced_align`` (Graves et al., ICML 2006, the alignment variant of the CTC
forward pass): states ``blank t1 blank t2 … tN blank``; from state s a frame may stay, step to s+1, or skip to s+2 when
s+2 is a token different from s's previous token (a blank is mandatory between two equal tokens); the path starts in
one of the first two states and ends in one of the last two. ``align_reference`` uses torchaudio's implementation on
the card when the runtime has it and this one otherwise; both give the same path up to ties
(packs/omni/tests/test_align_gpu.py compares them on real emissions).
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

import numpy as np

NEG_INF = -np.inf

FloatArray = np.ndarray[Any, np.dtype[np.floating[Any]]]
IntArray = np.ndarray[Any, np.dtype[np.int64]]


def viterbi(log_probs: FloatArray, targets: Sequence[int], blank: int = 0) -> IntArray | None:
    """The token position each frame of the best forced path emits (0…N-1), or -1 for a blank frame; None when no
    path exists (fewer frames than the targets need: one per token plus a blank between equal neighbours).

    ``log_probs`` is (frames, vocabulary) log-softmax output; ``targets`` the token ids, none of them ``blank``."""
    lp = np.asarray(log_probs, dtype=np.float64)
    if lp.ndim != 2:
        raise ValueError("log_probs must be (frames, vocabulary)")
    frames = lp.shape[0]
    n = len(targets)
    if n == 0:
        return np.full(frames, -1, dtype=np.int64)
    if any(int(t) == blank for t in targets):
        raise ValueError("a target token is the blank")
    if frames == 0:
        return None
    ext = np.full(2 * n + 1, blank, dtype=np.int64)
    ext[1::2] = np.asarray(targets, dtype=np.int64)
    states = ext.size
    skip = np.zeros(states, dtype=bool)
    skip[3::2] = ext[3::2] != ext[1:-2:2]  # token s may be reached from token s-2 when the two differ
    emit = lp[:, ext]  # (frames, states)
    alpha = np.full(states, NEG_INF)
    alpha[0] = emit[0, 0]
    alpha[1] = emit[0, 1]
    back = np.zeros((frames, states), dtype=np.int8)
    stay = np.empty(states)
    step = np.empty(states)
    jump = np.empty(states)
    for t in range(1, frames):
        stay[:] = alpha
        step[0] = NEG_INF
        step[1:] = alpha[:-1]
        jump[:2] = NEG_INF
        jump[2:] = np.where(skip[2:], alpha[:-2], NEG_INF)
        best = np.argmax(np.stack((stay, step, jump)), axis=0)
        alpha = np.choose(best, (stay, step, jump)) + emit[t]
        back[t] = best
    ends = [states - 1, states - 2]
    s = max(ends, key=lambda e: alpha[e])
    if not np.isfinite(alpha[s]):
        return None
    path = np.empty(frames, dtype=np.int64)
    for t in range(frames - 1, -1, -1):
        path[t] = s
        s -= int(back[t, s])
    return np.where(path % 2 == 1, (path - 1) // 2, -1)


def positions_from_labels(labels: Sequence[int], blank: int = 0) -> IntArray:
    """Token positions per frame from a forced path given as token ids per frame (torchaudio's ``forced_align``
    output): a new token starts at a non-blank frame whose id differs from the frame before, or that follows a blank
    (two equal tokens are always separated by a blank on a valid path)."""
    out = np.full(len(labels), -1, dtype=np.int64)
    j = -1
    prev = blank
    for t, raw in enumerate(labels):
        lab = int(raw)
        if lab != blank:
            if lab != prev:
                j += 1
            out[t] = j
        prev = lab
    return out


@dataclass(frozen=True)
class Span:
    start: int  # first frame
    end: int  # one past the last frame
    score: float  # mean probability of the emitted token over the span's frames


def token_spans(positions: IntArray, log_probs: FloatArray, targets: Sequence[int]) -> list[Span]:
    """Each token's frames along a forced path (``positions`` from :func:`viterbi`)."""
    lp = np.asarray(log_probs, dtype=np.float64)
    spans: list[Span] = []
    for j, tok in enumerate(targets):
        idx = np.flatnonzero(positions == j)
        if idx.size == 0:
            raise ValueError(f"the path emits no frame for token {j}")
        spans.append(Span(int(idx[0]), int(idx[-1]) + 1, float(np.mean(np.exp(lp[idx, int(tok)])))))
    return spans


def word_spans(tokens: Sequence[Span], ranges: Sequence[tuple[int, int]]) -> list[Span]:
    """Merge token spans into word spans: word i covers tokens ``ranges[i][0]`` up to (not including)
    ``ranges[i][1]`` (separator tokens between words belong to no word); its score is the frame-weighted mean of its
    tokens' scores."""
    out: list[Span] = []
    for a, b in ranges:
        if not 0 <= a < b <= len(tokens):
            raise ValueError(f"word token range {a}:{b} is outside the {len(tokens)} tokens")
        part = tokens[a:b]
        frames = sum(s.end - s.start for s in part)
        score = sum(s.score * (s.end - s.start) for s in part) / frames
        out.append(Span(part[0].start, part[-1].end, score))
    return out
