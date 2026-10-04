"""cadence_worker.ctc_align: the forced CTC path against brute force on small cases, frame labels → positions, spans,
and the alignment artifact's reading and token cleaning."""

from __future__ import annotations

import itertools
import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_worker import ctc_align
from cadence_worker import reference_alignment as ra
from cadence_worker.steps.base import StepInputError


def collapse(path: tuple[int, ...], blank: int) -> list[int]:
    out: list[int] = []
    prev = None
    for p in path:
        if p != prev and p != blank:
            out.append(p)
        prev = p
    return out


def brute(lp: np.ndarray[Any, Any], targets: list[int], blank: int = 0) -> float:
    """The best score over every frame labelling that collapses to the targets."""
    best = -np.inf
    for path in itertools.product(range(lp.shape[1]), repeat=lp.shape[0]):
        if collapse(path, blank) == targets:
            best = max(best, float(sum(lp[t, p] for t, p in enumerate(path))))
    return best


def path_score(lp: np.ndarray[Any, Any], positions: np.ndarray[Any, Any], targets: list[int], blank: int = 0) -> float:
    return float(sum(lp[t, blank if j < 0 else targets[j]] for t, j in enumerate(positions)))


@pytest.mark.parametrize("targets", [[1], [1, 2], [1, 1], [2, 1, 2], [1, 2, 2]])
def test_viterbi_is_the_best_forced_path(targets: list[int]) -> None:
    rng = np.random.default_rng(7)
    for _ in range(3):
        lp = np.log(rng.dirichlet(np.ones(3), size=6))
        pos = ctc_align.viterbi(lp, targets)
        assert pos is not None
        labels = tuple(0 if j < 0 else targets[j] for j in pos)
        assert collapse(labels, 0) == targets
        assert path_score(lp, pos, targets) == pytest.approx(brute(lp, targets))


def test_viterbi_impossible_and_empty() -> None:
    lp = np.log(np.full((2, 3), 1 / 3))
    assert ctc_align.viterbi(lp, [1, 1]) is None  # two equal tokens need a blank between: three frames
    assert ctc_align.viterbi(lp, [1, 2]) is not None
    empty = ctc_align.viterbi(lp, [])
    assert empty is not None
    assert list(empty) == [-1, -1]
    with pytest.raises(ValueError, match="blank"):
        ctc_align.viterbi(lp, [0])


def test_positions_from_labels() -> None:
    # tokens a a b: the blank between the two a's starts the second one
    assert list(ctc_align.positions_from_labels([0, 1, 1, 0, 1, 2, 2, 0])) == [-1, 0, 0, -1, 1, 2, 2, -1]


def test_token_and_word_spans() -> None:
    lp = np.log(np.full((6, 3), 0.5))
    pos = np.array([-1, 0, 0, 1, -1, 2])
    tokens = ctc_align.token_spans(pos, lp, [1, 2, 1])
    assert [(s.start, s.end) for s in tokens] == [(1, 3), (3, 4), (5, 6)]
    words = ctc_align.word_spans(tokens, [(0, 2), (2, 3)])
    assert [(w.start, w.end) for w in words] == [(1, 4), (5, 6)]
    assert words[0].score == pytest.approx(0.5)
    with pytest.raises(ValueError, match="outside"):
        ctc_align.word_spans(tokens, [(2, 4)])


def test_clean_word() -> None:
    assert ra.clean_word("Međutim,") == "međutim"
    assert ra.clean_word("«ДА»") == "да"
    assert ra.clean_word("שָׁלוֹם!") == "שלום"
    assert ra.clean_word("—") == ""
    assert ra.clean_word("400.") == "400"


def test_read_alignment(tmp_path: Path) -> None:
    p = tmp_path / "a.jsonl"
    ra.write(
        p,
        {"aligner": {"auxiliary": "auxiliary/x"}},
        [
            {
                "audio": "b3:1",
                "text": "a b",
                "aligned": True,
                "words": [{"index": 1, "word": "b", "start": 0.5, "end": 0.7, "score": 0.9}],
                "skipped": [0],
            }
        ],
    )
    header, rows = ra.read(p)
    assert header["format"] == ra.FORMAT
    assert ra.timed_words(rows["b3:1"]) == {1: (0.5, 0.7)}
    assert ra.timed_words({"aligned": False, "reason": "x"}) == {}
    (tmp_path / "bad.jsonl").write_text(json.dumps({"audio": "b3:1"}) + "\n", encoding="utf-8")
    with pytest.raises(StepInputError, match="header"):
        ra.read(tmp_path / "bad.jsonl")
