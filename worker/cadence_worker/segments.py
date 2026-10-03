"""The ``segments`` artifact (``cadence.segments/1``; docs/review/2026-10-03-phase-4-plan.md "Interfaces between
streams", D → X and X → D): a directory with ``segments.jsonl``, one JSON row per segment, and an optional
``segments.json`` header ``{format: "cadence.segments/1", …}``.

A row carries ``uri`` (``mount://<mount>/<path>#t=<start>,<end>&ch=<n>``), ``hash`` (b3 of the canonical 16 kHz PCM16
segment — the utterance identity, equal to the hash of the WAV the freeze cuts), ``start`` and ``end`` (seconds),
``channel``, ``role`` (``caller | bot | mono``), ``vad`` and optionally ``language``, ``text``, ``origin`` and
``speaker``. The pseudo-label ensemble adds ``confidence`` and, for a disputed segment, ``dispute`` (reason,
candidates, lid). Rows keep every field this module does not know, so a later producer's fields pass through.

Stream D owns the producer (``sdp_ingest``); this reader checks only what the consumers here rely on.
"""

from __future__ import annotations

import json
from collections.abc import Iterable, Mapping
from pathlib import Path
from typing import Any

from cadence_worker.cas import valid_hash
from cadence_worker.steps.base import StepInputError

FORMAT = "cadence.segments/1"
ROWS = "segments.jsonl"
HEADER = "segments.json"
ROLES = ("caller", "bot", "mono")
REQUIRED = ("uri", "hash", "start", "end", "channel", "role")

ORIGIN_PSEUDO = "pseudo-label"
ORIGIN_DISPUTED = "pseudo-label:disputed"


def read_header(root: Path) -> dict[str, Any]:
    """The header of a segments directory ({} when it has none)."""
    if not root.is_dir() or not (root / HEADER).is_file():
        return {}
    try:
        header = json.loads((root / HEADER).read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"segments.json does not parse: {e}") from e
    if not isinstance(header, dict) or header.get("format", FORMAT) != FORMAT:
        raise StepInputError(f"segments.json is not {FORMAT}")
    return header


def read_segments(path: Path) -> list[dict[str, Any]]:
    """The rows of a segments artifact (a directory with segments.jsonl, or the file itself), in order."""
    f = path / ROWS if path.is_dir() else path
    read_header(path)
    try:
        lines = f.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"the segments input has no readable {ROWS}: {e}") from e
    out: list[dict[str, Any]] = []
    seen: set[str] = set()
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"segments line {n} is not JSON") from e
        if not isinstance(row, dict):
            raise StepInputError(f"segments line {n} is not an object")
        missing = [k for k in REQUIRED if k not in row]
        if missing:
            raise StepInputError(f"segments line {n} lacks {', '.join(missing)}")
        if not isinstance(row["hash"], str) or not valid_hash(row["hash"]):
            raise StepInputError(f"segments line {n}: hash {row['hash']!r} is not a b3 hash")
        if row["role"] not in ROLES:
            raise StepInputError(f"segments line {n}: role {row['role']!r} is not one of {', '.join(ROLES)}")
        if not isinstance(row["start"], int | float) or not isinstance(row["end"], int | float):
            raise StepInputError(f"segments line {n}: start and end are seconds")
        if row["end"] <= row["start"]:
            raise StepInputError(f"segments line {n}: end {row['end']} is not after start {row['start']}")
        if row["hash"] in seen:
            raise StepInputError(f"segments line {n}: segment {row['hash']} appears twice")
        seen.add(row["hash"])
        out.append(row)
    return out


def write_segments(root: Path, rows: Iterable[Mapping[str, Any]], header: Mapping[str, Any]) -> int:
    """Write a segments directory (header and rows); returns the number of rows."""
    root.mkdir(parents=True, exist_ok=True)
    (root / HEADER).write_text(
        json.dumps({**header, "format": FORMAT}, ensure_ascii=False, sort_keys=True) + "\n", encoding="utf-8"
    )
    n = 0
    with (root / ROWS).open("w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
            n += 1
    return n
