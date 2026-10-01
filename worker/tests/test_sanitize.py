from __future__ import annotations

import json
import math

import pytest

from cadence_worker.sanitize import REDACTED, Redactor, finite, finite_metrics


@pytest.mark.parametrize(
    ("text", "want"),
    [
        ("agent cst_AbCdEf1234567890 here", f"agent {REDACTED} here"),
        ("key=cdk_0123456789abcdef,", f"key={REDACTED},"),
        ("cwk_x9Y8z7W6v5U4 cah_abcdefgh12 cep_ZYXWVUTS99", f"{REDACTED} {REDACTED} {REDACTED}"),
        ("hf_AbCdEfGhIjKlMnOpQrSt", REDACTED),
        ("Authorization: Bearer abc.def-ghi_jkl==", f"Authorization: Bearer {REDACTED}"),
        ("authorization: bearer eyJhbGciOiJIUzI1NiJ9.e30.x", f"authorization: bearer {REDACTED}"),
        ("ghp_0123456789abcdefghijABCDEFGHIJ", REDACTED),
        ("sk-ant-api03-abcdefghijklmnopqrstuvwxyz", REDACTED),
        # Not credentials: short prefixes and ordinary words stay.
        ("cst_ab hf_x Bearer short step_10 val_wer", "cst_ab hf_x Bearer short step_10 val_wer"),
    ],
)
def test_credential_shaped_tokens_are_redacted(text: str, want: str) -> None:
    assert Redactor().text(text) == want


def test_secret_values_are_redacted_longest_first() -> None:
    r = Redactor(["s3cret", "s3cret-and-more", "ab"])  # "ab" is too short to redact without mangling text
    assert r.text("x s3cret-and-more y s3cret z ab") == f"x {REDACTED} y {REDACTED} z ab"


def test_values_are_cleaned_recursively_and_stay_json() -> None:
    r = Redactor(["topsecret"])
    got = r.value(
        {
            "url": "https://u:topsecret@hub",
            "topsecret": [1, 2.5, math.nan, {"inf": -math.inf, "ok": True, "none": None}],
            "obj": ValueError("topsecret"),
        }
    )
    assert got["url"] == f"https://u:{REDACTED}@hub"
    assert got[REDACTED] == [1, 2.5, None, {"inf": None, "ok": True, "none": None}]
    assert got["obj"] == REDACTED
    json.dumps(got, allow_nan=False)


def test_only_finite_metrics_survive() -> None:
    assert finite("0.5") == 0.5
    assert finite(math.nan) is None
    assert finite(math.inf) is None
    assert finite("x") is None
    assert finite(None) is None
    assert finite_metrics({"loss": 0.5, "val_wer": math.nan, "lr": -math.inf, "n": 3, "bad": "x"}) == {
        "loss": 0.5,
        "n": 3.0,
    }
    assert finite_metrics(None) == {}
