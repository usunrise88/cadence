"""Script conversion of transcripts at import (dataset_import's ``transliterate``).

A base model knows a language through its tokenizer's alphabet as well as its prompts: Serbian FLEURS is written in
Cyrillic, and Nemotron 3.5's tokenizer lacks six of its letters (dje, je, lje, nje, tshe, dzhe), while Serbian's
official Latin script (Gaj) maps one to one onto it (2026-10-01, the test stand's Serbian fine-tune). Only exact,
reversible schemes belong here.
"""

from __future__ import annotations

from collections.abc import Callable

_SR_CYRL = "абвгдђежзијклљмнњопрстћуфхцчџш"
_SR_LATN = ["a", "b", "v", "g", "d", "đ", "e", "ž", "z", "i", "j", "k", "l", "lj", "m", "n", "nj", "o", "p", "r", "s",
            "t", "ć", "u", "f", "h", "c", "č", "dž", "š"]  # fmt: skip
_SR = dict(zip(_SR_CYRL, _SR_LATN, strict=True))


def sr_cyrl_to_latn(text: str) -> str:
    """Serbian Cyrillic → Gaj Latin. A capital digraph letter is Lj/Nj/Dž before lower case and LJ/NJ/DŽ inside an
    all-caps word; anything that is not Serbian Cyrillic passes unchanged."""
    out: list[str] = []
    for i, ch in enumerate(text):
        lo = ch.lower()
        t = _SR.get(lo)
        if t is None:
            out.append(ch)
            continue
        if ch != lo:
            nxt = text[i + 1] if i + 1 < len(text) else ""
            t = t.upper() if len(t) > 1 and nxt.isupper() else t[0].upper() + t[1:]
        out.append(t)
    return "".join(out)


SCHEMES: dict[str, Callable[[str], str]] = {"sr-Cyrl-Latn": sr_cyrl_to_latn}


def transliterate(text: str, scheme: str) -> str:
    """Apply scheme ("" leaves the text as it is)."""
    return SCHEMES[scheme](text) if scheme else text
