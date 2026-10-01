"""Word error rate, the simple scorer the conformance suite and the toy pack use (the versioned scorer step kinds of
phase 3 replace it for evaluations)."""

from __future__ import annotations

from collections.abc import Iterable


def edit_distance(ref: list[str], hyp: list[str]) -> int:
    prev = list(range(len(hyp) + 1))
    for i, r in enumerate(ref, 1):
        cur = [i] + [0] * len(hyp)
        for j, h in enumerate(hyp, 1):
            cur[j] = min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (r != h))
        prev = cur
    return prev[-1]


def wer(pairs: Iterable[tuple[str, str]]) -> float:
    """Corpus WER over (reference, hypothesis) pairs: word edits / reference words."""
    edits = words = 0
    for ref, hyp in pairs:
        r, h = ref.split(), hyp.split()
        edits += edit_distance(r, h)
        words += len(r)
    return edits / words if words else float(edits > 0)
