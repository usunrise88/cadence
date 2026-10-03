"""Levenshtein alignment for scorers: word alignment with its operations (match, substitution, deletion, insertion)
and a fast character edit distance.

Word alignment is the classic dynamic programme with unit costs, so S + D + I is always the edit distance; among the
alignments with that many errors it takes one with the fewest substitutions, i.e. the most matched words (what a
reader of a diff expects), and the backtrace prefers a match, then a substitution, then a deletion, then an
insertion. The character distance is
Myers' bit-parallel algorithm (Hyyrö's formulation for the Levenshtein distance), linear in the text for references of
any length (Python integers are bit vectors of any width).
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass, field

Op = tuple[str, str | None, str | None]  # ("=" | "S" | "D" | "I", ref word or None, hyp word or None)


@dataclass
class Alignment:
    sub: int = 0
    dele: int = 0
    ins: int = 0
    ops: list[Op] = field(default_factory=list)

    @property
    def errors(self) -> int:
        return self.sub + self.dele + self.ins


def align(ref: Sequence[str], hyp: Sequence[str]) -> Alignment:
    """The alignment with the fewest errors and, among those, the fewest substitutions (hence the most matches):
    ``the cat sat on the mat`` / ``the bat sat on mat today`` aligns as one substitution, one deletion and one
    insertion rather than three substitutions. Costs are ``errors * k + substitutions`` with k above any count."""
    n, m = len(ref), len(hyp)
    k = n + m + 1
    sub, gap = k + 1, k
    d = [[0] * (m + 1) for _ in range(n + 1)]
    for j in range(m + 1):
        d[0][j] = j * gap
    for i in range(1, n + 1):
        row, prev = d[i], d[i - 1]
        row[0] = i * gap
        r = ref[i - 1]
        for j in range(1, m + 1):
            row[j] = min(prev[j] + gap, row[j - 1] + gap, prev[j - 1] + (0 if r == hyp[j - 1] else sub))
    out = Alignment()
    i, j = n, m
    rev: list[Op] = []
    while i > 0 or j > 0:
        if i > 0 and j > 0 and ref[i - 1] == hyp[j - 1] and d[i][j] == d[i - 1][j - 1]:
            rev.append(("=", ref[i - 1], hyp[j - 1]))
            i, j = i - 1, j - 1
        elif i > 0 and j > 0 and ref[i - 1] != hyp[j - 1] and d[i][j] == d[i - 1][j - 1] + sub:
            rev.append(("S", ref[i - 1], hyp[j - 1]))
            out.sub += 1
            i, j = i - 1, j - 1
        elif i > 0 and d[i][j] == d[i - 1][j] + gap:
            rev.append(("D", ref[i - 1], None))
            out.dele += 1
            i -= 1
        else:
            rev.append(("I", None, hyp[j - 1]))
            out.ins += 1
            j -= 1
    rev.reverse()
    out.ops = rev
    return out


def char_distance(a: str, b: str) -> int:
    """The Levenshtein distance between two strings (Myers 1999, Hyyrö 2001)."""
    if not a:
        return len(b)
    if not b:
        return len(a)
    m = len(a)
    full = (1 << m) - 1
    top = 1 << (m - 1)
    peq: dict[str, int] = {}
    for i, c in enumerate(a):
        peq[c] = peq.get(c, 0) | (1 << i)
    pv, mv, score = full, 0, m
    for c in b:
        eq = peq.get(c, 0)
        xv = eq | mv
        xh = ((((eq & pv) + pv) & full) ^ pv) | eq
        ph = (mv | ~(xh | pv)) & full
        mh = pv & xh
        if ph & top:
            score += 1
        elif mh & top:
            score -= 1
        ph = ((ph << 1) | 1) & full
        mh = (mh << 1) & full
        pv = (mh | ~(xv | ph)) & full
        mv = ph & xv
    return score
