"""The ``boost_list`` artifact (docs/spec/08-resolutions.md R24; docs/spec/03-pipelines-defaults.md "Hot words"): the
phrases a transcribe step boosts at decode time, family-neutral. A family's transcribe kind that declares a boosting
capability consumes it as the optional input ``boost``.

Two encodings, told apart by content:

- JSON ``{"terms": ["Tel Aviv", ...], "weight": 0.5}`` (``weight`` optional; ``format``, when present, is
  ``cadence.boost_list/1``; other keys such as ``name`` or ``locale`` are ignored) — what the control plane renders
  from a language pack's ``lang/<locale>/boost/<domain>.txt``;
- that pack file itself: plain UTF-8 text, one phrase per line; blank lines and lines starting with ``#`` are skipped,
  except a line ``# weight: <number>``, which sets the list's weight.

Phrases are NFC, trimmed, with inner whitespace collapsed, deduplicated in order. Without a weight in the list the
step's own default applies. The list's identity in a decoding configuration is the BLAKE3 hash of the artifact file.
"""

from __future__ import annotations

import json
import re
import unicodedata
from dataclasses import dataclass
from pathlib import Path

from cadence_worker.cas import hash_file

FORMAT = "cadence.boost_list/1"
MAX_TERMS = 10000
MAX_TERM_CHARS = 100
WEIGHT_LINE = re.compile(r"^#\s*weight\s*:\s*(\S+)\s*$", re.IGNORECASE)


class BoostListError(ValueError):
    pass


@dataclass(frozen=True)
class BoostList:
    terms: tuple[str, ...]
    weight: float | None
    hash: str


def _weight(v: object) -> float:
    if isinstance(v, bool) or not isinstance(v, int | float | str):
        raise BoostListError(f"the boost weight {v!r} is not a number")
    try:
        w = float(v)
    except ValueError as e:
        raise BoostListError(f"the boost weight {v!r} is not a number") from e
    if not 0 <= w <= 100:
        raise BoostListError(f"the boost weight {w} is outside 0 to 100")
    return w


def _clean(terms: list[str]) -> tuple[str, ...]:
    out: dict[str, None] = {}
    for t in terms:
        c = " ".join(unicodedata.normalize("NFC", t).split())
        if not c:
            continue
        if len(c) > MAX_TERM_CHARS:
            raise BoostListError(f"the boost phrase {c[:40]!r}… is longer than {MAX_TERM_CHARS} characters")
        out.setdefault(c, None)
    if not out:
        raise BoostListError("the boost list has no phrases")
    if len(out) > MAX_TERMS:
        raise BoostListError(f"the boost list has {len(out)} phrases; at most {MAX_TERMS}")
    return tuple(out)


def parse(raw: bytes) -> tuple[tuple[str, ...], float | None]:
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as e:
        raise BoostListError("the boost list is not UTF-8") from e
    stripped = text.lstrip("﻿").strip()
    if stripped.startswith("{"):
        try:
            doc = json.loads(stripped)
        except ValueError as e:
            raise BoostListError(f"the boost list is not valid JSON: {e}") from e
        if not isinstance(doc, dict) or doc.get("format", FORMAT) != FORMAT:
            raise BoostListError(f"a JSON boost list is an object {{terms, weight?}} (format {FORMAT} when named)")
        terms = doc.get("terms")
        if not isinstance(terms, list) or not all(isinstance(t, str) for t in terms):
            raise BoostListError("a JSON boost list needs terms, a list of strings")
        weight = doc.get("weight")
        return _clean(terms), None if weight is None else _weight(weight)
    weight_: float | None = None
    lines: list[str] = []
    for line in stripped.splitlines():
        s = line.strip()
        if m := WEIGHT_LINE.match(s):
            weight_ = _weight(m.group(1))
        elif s and not s.startswith("#"):
            lines.append(s)
    return _clean(lines), weight_


def read(path: Path) -> BoostList:
    try:
        raw = path.read_bytes()
    except OSError as e:
        raise BoostListError(f"cannot read the boost list: {e}") from e
    terms, weight = parse(raw)
    return BoostList(terms=terms, weight=weight, hash=hash_file(path))
