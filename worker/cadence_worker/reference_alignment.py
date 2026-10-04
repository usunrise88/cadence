"""The ``alignment`` artifact (phase 4 · stream L; R51, R54): word timings of a dataset's reference texts, written by
``align_reference`` and read by ``latency_score`` (emission delay) and the audio view's reference track.

JSON lines; the first line is a header, then one row per utterance in dataset order, keyed by the BLAKE3 hash of the
audio file like ``hypotheses`` and ``vad``:

    {"alignment": {format: cadence.alignment/1, aligner: {auxiliary, versionId, hfRepo, revision}, method,
                   frameMs, utterances, aligned, unaligned, words}}
    {audio, text, language, aligned: true, words: [{index, word, start, end, score}], skipped: [index, …]}
    {audio, text, language, aligned: false, reason}

``text`` is the dataset's reference exactly as the manifest has it; ``index`` is the position of the word in
``text.split()`` and ``word`` that whitespace token unchanged, so a reader joins the timings to the reference it scores
with the same split. ``start`` and ``end`` are seconds from the start of the audio, ``score`` the mean probability of
the word's tokens over its frames (a low score marks a doubtful alignment). ``skipped`` lists the tokens with no
timing (punctuation only, or nothing the aligner's vocabulary can spell). An utterance is unaligned when its language
is not one the aligner covers, it is longer than the step allows, its audio is too short for its text, or it has no
text; the reason says which. Nothing is invented: an unaligned utterance has no times.
"""

from __future__ import annotations

import json
import unicodedata
from collections.abc import Mapping
from pathlib import Path
from typing import Any

from cadence_worker.steps.base import StepInputError

FORMAT = "cadence.alignment/1"
HEADER_KEY = "alignment"


def is_hebrew_point(ch: str) -> bool:
    """Hebrew cantillation marks and points (niqqud), U+0591 to U+05C7 non-spacing marks."""
    return "֑" <= ch <= "ׇ" and unicodedata.category(ch) == "Mn"


def clean_word(token: str) -> str:
    """A reference token as a CTC model's vocabulary spells it: NFC, lower case, without punctuation and symbols
    (Unicode categories P* and S*) and without Hebrew points. Letters, digits and other marks (č, ć, đ) stay."""
    out = [
        ch
        for ch in unicodedata.normalize("NFC", token).lower()
        if not unicodedata.category(ch).startswith(("P", "S", "Z", "C")) and not is_hebrew_point(ch)
    ]
    return "".join(out)


def read(path: Path) -> tuple[dict[str, Any], dict[str, dict[str, Any]]]:
    """The header and the rows by audio hash of an alignment artifact."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"cannot read the alignment input: {e}") from e
    header: dict[str, Any] = {}
    rows: dict[str, dict[str, Any]] = {}
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"alignment line {n} is not JSON") from e
        if isinstance(row, dict) and isinstance(row.get(HEADER_KEY), dict) and "audio" not in row:
            header = row[HEADER_KEY]
            if header.get("format") != FORMAT:
                raise StepInputError(f"the alignment input is not {FORMAT}")
            continue
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str):
            raise StepInputError(f"alignment line {n} lacks audio")
        rows[row["audio"]] = row
    if not header:
        raise StepInputError(f"the alignment input has no {FORMAT} header line")
    return header, rows


def write(path: Path, header: Mapping[str, Any], rows: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8") as f:
        f.write(json.dumps({HEADER_KEY: {"format": FORMAT, **header}}, ensure_ascii=False, separators=(",", ":")))
        f.write("\n")
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")


def timed_words(row: Mapping[str, Any]) -> dict[int, tuple[float, float]]:
    """An aligned row's word times by token index ({} for an unaligned row)."""
    if row.get("aligned") is not True or not isinstance(row.get("words"), list):
        return {}
    out: dict[int, tuple[float, float]] = {}
    for w in row["words"]:
        try:
            out[int(w["index"])] = (float(w["start"]), float(w["end"]))
        except (KeyError, TypeError, ValueError):
            continue
    return out
