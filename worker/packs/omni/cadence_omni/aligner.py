"""Aligning one reference text to one utterance's audio with a CTC model's emissions (no framework imports here: the
model is anything with :class:`Emissions`'s shape, so the glue is tested without a card).

The reference is split on whitespace; each token is spelled the way the CTC vocabulary writes text
(:func:`cadence_worker.reference_alignment.clean_word`) and encoded on its own. Tokens that clean to nothing
(punctuation) or encode to nothing are skipped: they get no timing. The remaining tokens' pieces, with the model's
word-boundary token between words when its vocabulary has one, are the forced alignment's targets; a word's span runs
from its first piece's first frame to its last piece's last frame, and frames become seconds at the model's frame
rate (samples per frame from the emission length).
"""

from __future__ import annotations

from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any, Protocol

import numpy as np

from cadence_worker import ctc_align
from cadence_worker.reference_alignment import clean_word

SR = 16000


class Emissions(Protocol):
    """A CTC model as the aligner sees it."""

    blank: int
    separator: int | None  # the word-boundary token between words, when the vocabulary has one

    def encode(self, text: str) -> list[int]:
        """Token ids of a cleaned word (no special tokens)."""
        ...

    def log_probs(self, samples: np.ndarray[Any, np.dtype[np.float32]]) -> np.ndarray[Any, np.dtype[np.float32]]:
        """(frames, vocabulary) log-softmax emissions of 16 kHz mono samples."""
        ...


# A forced aligner: (log_probs, targets, blank) → token position per frame (-1 blank), or None when impossible.
ForcedAlign = Callable[[np.ndarray[Any, np.dtype[np.float32]], Sequence[int], int], "np.ndarray[Any, Any] | None"]


class UnalignedError(Exception):
    """The utterance cannot be aligned; the message is the row's reason."""


@dataclass(frozen=True)
class Result:
    words: list[dict[str, Any]]
    skipped: list[int]
    frame_s: float


def align_utterance(
    model: Emissions,
    samples: np.ndarray[Any, np.dtype[np.float32]],
    text: str,
    forced: ForcedAlign = ctc_align.viterbi,
) -> Result:
    tokens = text.split()
    if not tokens:
        raise UnalignedError("the reference text is empty")
    targets: list[int] = []
    ranges: list[tuple[int, int]] = []
    timed: list[int] = []
    skipped: list[int] = []
    for i, tok in enumerate(tokens):
        cleaned = clean_word(tok)
        ids = [t for t in model.encode(cleaned) if t != model.blank] if cleaned else []
        if not ids:
            skipped.append(i)
            continue
        if targets and model.separator is not None:
            targets.append(model.separator)  # the word boundary the model emits between words
        ranges.append((len(targets), len(targets) + len(ids)))
        targets.extend(ids)
        timed.append(i)
    if not targets:
        raise UnalignedError("no word of the reference is in the aligner's vocabulary")
    lp = model.log_probs(samples)
    frames = int(lp.shape[0])
    if frames == 0:
        raise UnalignedError("the audio is too short for the aligner")
    positions = forced(lp, targets, model.blank)
    if positions is None:
        raise UnalignedError(f"the audio ({frames} frames) is too short for the reference ({len(targets)} tokens)")
    frame_s = len(samples) / SR / frames
    spans = ctc_align.word_spans(ctc_align.token_spans(np.asarray(positions), lp, targets), ranges)
    words = [
        {
            "index": i,
            "word": tokens[i],
            "start": round(sp.start * frame_s, 3),
            "end": round(sp.end * frame_s, 3),
            "score": round(sp.score, 4),
        }
        for i, sp in zip(timed, spans, strict=True)
    ]
    return Result(words=words, skipped=skipped, frame_s=frame_s)
