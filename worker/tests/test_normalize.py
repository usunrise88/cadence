"""The scoring normalizer interpreter and the alignment it feeds (cadence_worker.normalize, cadence_worker.align)."""

from __future__ import annotations

import json
import random
import unicodedata
from typing import Any

import pytest

from cadence_worker.align import align, char_distance
from cadence_worker.normalize import Normalizer, NormalizerError

NBSP = chr(0xA0)
LIGATURE = chr(0xFB01)  # fi
FULLWIDTH_ABC = chr(0xFF21) + chr(0xFF22) + chr(0xFF23)
MAQAF, GERESH, GERSHAYIM = chr(0x5BE), chr(0x5F3), chr(0x5F4)

BASIC: dict[str, Any] = {
    "locale": "*",
    "unicode": "NFKC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": False,
    "mappings": [],
    "numbers": "keep",
}


def norm(**over: Any) -> Normalizer:
    return Normalizer.from_json(json.dumps({**BASIC, **over}))


# ---------------------------------------------------------------- examples


@pytest.mark.parametrize(
    ("over", "text", "want"),
    [
        # Hebrew niqqud (Mn) go with removeMarks and stay without it.
        ({"removeMarks": True}, "שָׁלוֹם עוֹלָם", "שלום עולם"),
        ({"removeMarks": False, "unicode": "NFC"}, "שָׁלוֹם", unicodedata.normalize("NFC", "שָׁלוֹם")),
        # The maqaf (U+05BE, Pd) joins words in writing; stripped, they are two words.
        ({}, "בית־ספר", "בית ספר"),
        ({"punctuation": "keep"}, "בית־ספר", "בית־ספר"),
        # Geresh and gershayim (Po) are punctuation: a mapping keeps an acronym one word.
        ({}, "צה״ל", "צה ל"),
        ({"mappings": [{"from": "״", "to": ""}, {"from": '"', "to": ""}]}, 'צה״ל צה"ל', "צהל צהל"),
        # Serbian in both scripts: case-folded; removeMarks takes the caron and acute off, đ has no decomposition.
        ({}, "Ђорђе Đorđe ČAČAK", "ђорђе đorđe čačak"),
        ({"removeMarks": True}, "Ćevapčići Šabac Đakovo", "cevapcici sabac đakovo"),
        # Mixed scripts in one utterance.
        ({}, "Hello, עולם! Привет — мир.", "hello עולם привет мир"),
        # NFKC folds compatibility characters; NFC keeps them.
        ({}, LIGATURE + "le " + FULLWIDTH_ABC, "file abc"),
        ({"unicode": "NFC", "casefold": False}, LIGATURE + "le", LIGATURE + "le"),
        # Whitespace always collapses (tabs, newlines, no-break spaces).
        ({"punctuation": "keep"}, "  a\tb\n" + NBSP + "c  ", "a b c"),
        # casefold off keeps case; full case folding (ß → ss) on.
        ({"casefold": False}, "Tel Aviv", "Tel Aviv"),
        ({}, "Straße", "strasse"),
        # Turkish dotted capital I: NFD splits the dot off, so removeMarks then casefold gives a plain i.
        ({"removeMarks": True}, "İstanbul", "istanbul"),
    ],
)
def test_examples(over: dict[str, Any], text: str, want: str) -> None:
    assert norm(**over)(text) == want


def test_mappings_apply_in_order_after_unicode_and_before_punctuation() -> None:
    assert norm(mappings=[{"from": "a", "to": "b"}, {"from": "b", "to": "c"}])("a") == "c"
    assert norm(mappings=[{"from": "b", "to": "c"}, {"from": "a", "to": "b"}])("a") == "b"
    # Unicode first: the NFKC form of the ligature is what the mapping sees.
    assert norm(mappings=[{"from": "fi", "to": "phi"}])("ﬁ") == "phi"
    # Before punctuation: a mapping may remove or keep punctuation before the strip.
    assert norm(mappings=[{"from": "'", "to": ""}])("don't") == "dont"
    # Before casefold: mappings are case-sensitive.
    assert norm(mappings=[{"from": "A", "to": "x"}])("Aa") == "xa"


def test_without_punctuation_is_the_companion() -> None:
    keep = norm(punctuation="keep")
    assert keep("Hi, there.") == "hi, there."
    assert keep.without_punctuation()("Hi, there.") == "hi there"
    strip = norm()
    assert strip.without_punctuation() is strip


@pytest.mark.parametrize(
    "doc",
    [
        {**BASIC, "unicode": "NFD"},
        {**BASIC, "punctuation": "maybe"},
        {**BASIC, "numbers": "spoken"},
        {**BASIC, "mappings": [{"from": "", "to": "x"}]},
        {k: v for k, v in BASIC.items() if k != "casefold"},
    ],
)
def test_invalid_payloads_are_refused(doc: dict[str, Any]) -> None:
    with pytest.raises(NormalizerError):
        Normalizer.from_json(json.dumps(doc))
    with pytest.raises(NormalizerError):
        Normalizer.from_json(b"not json")


def test_rendered_artifact_carries_its_version() -> None:
    n = Normalizer.from_json(json.dumps({**BASIC, "versionId": "ver_x", "collection": "normalizer/basic", "extra": 1}))
    assert n.payload.versionId == "ver_x"


# ---------------------------------------------------------------- properties

NIQQUD = chr(0x5B0) + chr(0x5B4) + chr(0x5B7) + chr(0x5BC) + chr(0x5C1) + chr(0x5C2)  # Mn
COMBINING = chr(0x301) + chr(0x30C)  # acute, caron
POOL = (
    "abcXYZ ĐđČćŠ"  # Latin, Serbian Latin
    "АБВгдђјљњћџ"  # Cyrillic, Serbian Cyrillic
    "אבגדהוזחטיכלמנסעפצקרשת"  # Hebrew letters
    + NIQQUD
    + MAQAF
    + GERESH
    + GERSHAYIM
    + ".,!?;:-()\"'"  # punctuation
    + "0123 \t\n"  # digits and whitespace
    + NBSP
    + COMBINING
)


def _random_text(rng: random.Random) -> str:
    return "".join(rng.choice(POOL) for _ in range(rng.randint(0, 40)))


@pytest.mark.parametrize("over", [{}, {"removeMarks": True}, {"unicode": "NFC"}, {"casefold": False}])
def test_properties(over: dict[str, Any]) -> None:
    rng = random.Random(7)
    n = norm(**over)
    for _ in range(2000):
        out = n(_random_text(rng))
        assert n(out) == out, "idempotent"
        assert out == out.strip()
        assert "  " not in out
        assert not any(c in out for c in "\t\n" + NBSP)
        assert not any(unicodedata.category(c).startswith("P") for c in out)
        if over.get("removeMarks"):
            assert not any(unicodedata.category(c) == "Mn" for c in out)
        assert unicodedata.normalize(n.payload.unicode, out) == out


def _dp(a: list[str] | str, b: list[str] | str) -> int:
    prev = list(range(len(b) + 1))
    for i, x in enumerate(a, 1):
        cur = [i] + [0] * len(b)
        for j, y in enumerate(b, 1):
            cur[j] = min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (x != y))
        prev = cur
    return prev[-1]


def test_alignment_examples() -> None:
    a = align(["the", "cat", "sat", "on", "the", "mat"], ["the", "bat", "sat", "on", "mat", "today"])
    assert (a.sub, a.dele, a.ins) == (1, 1, 1)
    assert a.ops == [
        ("=", "the", "the"),
        ("S", "cat", "bat"),
        ("=", "sat", "sat"),
        ("=", "on", "on"),
        ("D", "the", None),
        ("=", "mat", "mat"),
        ("I", None, "today"),
    ]
    assert align([], ["x", "y"]).ins == 2
    assert align(["x"], []).dele == 1
    assert align([], []).ops == []


def test_alignment_and_char_distance_properties() -> None:
    rng = random.Random(11)
    words = ["a", "b", "c", "שלום", "мир"]
    for _ in range(1500):
        ref = [rng.choice(words) for _ in range(rng.randint(0, 9))]
        hyp = [rng.choice(words) for _ in range(rng.randint(0, 9))]
        a = align(ref, hyp)
        assert a.errors == _dp(ref, hyp)
        assert [r for op, r, _ in a.ops if op != "I"] == ref
        assert [h for op, _, h in a.ops if op != "D"] == hyp
        assert all((r == h) == (op == "=") for op, r, h in a.ops if op in "=S")
    for _ in range(1500):
        x = "".join(rng.choice("abשל ") for _ in range(rng.randint(0, 90)))
        y = "".join(rng.choice("abשל ") for _ in range(rng.randint(0, 90)))
        assert char_distance(x, y) == _dp(x, y)
