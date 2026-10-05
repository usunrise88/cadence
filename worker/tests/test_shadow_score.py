"""shadow_score@1, the neutral judge of a night's shadow replay (docs/spec/03 "Export, parity and benchmark", Shadow
replay; phase 5 · D4)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from cadence_worker.registry import registry
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.shadow_score import ShadowScoreParams, ShadowScoreStep, bootstrap, normalise

CALL_A = "mount://calls/night/a.wav"
CALL_B = "mount://calls/night/b.wav"


def segments(root: Path, rows: list[dict[str, Any]], files: list[dict[str, Any]] | None = None) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    header = {"format": "cadence.segments/1", "source": {"name": "calls"}, "root": "mount://calls/night"}
    (root / "segments.json").write_text(json.dumps(header), encoding="utf-8")
    (root / "segments.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    if files is not None:
        (root / "files.jsonl").write_text("".join(json.dumps(f) + "\n" for f in files), encoding="utf-8")
    return root


def seg_row(h: str, call: str, start: float, end: float, role: str = "caller") -> dict[str, Any]:
    return {
        "hash": h,
        "uri": f"{call}#t={start},{end}&ch=0",
        "file": call,
        "start": start,
        "end": end,
        "duration": end - start,
        "channel": 0,
        "role": role,
    }


def hyps(path: Path, rows: dict[str, dict[str, Any]]) -> Path:
    path.write_text("".join(json.dumps({"audio": h, **r}) + "\n" for h, r in rows.items()), encoding="utf-8")
    return path


def run(tmp: Path, cand: dict[str, dict[str, Any]], cur: dict[str, dict[str, Any]], **over: Any) -> dict[str, Any]:
    rows = [
        seg_row("b3:1", CALL_A, 0.5, 2.0),
        seg_row("b3:2", CALL_A, 3.0, 5.0),
        seg_row("b3:3", CALL_B, 0.2, 1.8),
        seg_row("b3:bot", CALL_B, 2.0, 3.0, role="bot"),
    ]
    files = [{"uri": CALL_A, "duration": 60.0}, {"uri": CALL_B, "duration": 30.0}]
    inputs = {
        "segments": segments(tmp / "seg", rows, files),
        "candidate": hyps(tmp / "cand.jsonl", cand),
        "current": hyps(tmp / "cur.jsonl", cur),
    }
    events: list[dict[str, Any]] = []
    ShadowScoreStep().run(
        ShadowScoreParams(**over), inputs, {"report": tmp / "report"}, StepContext(events.append, work_dir=tmp)
    )
    doc: dict[str, Any] = json.loads((tmp / "report" / "report.json").read_text(encoding="utf-8"))
    return doc


def test_registered_as_neutral() -> None:
    d = registry()["shadow_score"]
    assert d.get("neutral") is True
    assert d["version"] == "1"
    assert d["consumes"] == {"segments": "segments", "candidate": "hypotheses", "current": "hypotheses"}
    assert d["produces"] == {"report": "shadow_report"}
    props = d["params"]["properties"]
    assert props["worst_segments"]["x-cadence"]["defaultRef"] == "deploy.shadow_worst_segments"
    assert props["bootstrap_samples"]["x-cadence"]["defaultRef"] == "eval.bootstrap_samples"
    assert ShadowScoreParams().worst_segments == 50


def test_normalise_basic_ignores_case_and_punctuation() -> None:
    assert normalise("Zdravo, SVETE!  kako", "basic") == ["zdravo", "svete", "kako"]
    assert normalise("Zdravo, SVETE!", "none") == ["Zdravo,", "SVETE!"]


def test_identical_decodes_do_not_diverge(tmp_path: Path) -> None:
    same: dict[str, dict[str, Any]] = {
        "b3:1": {"text": "dobar dan", "confidence": 0.9},
        "b3:2": {"text": "hvala lepo"},
        "b3:3": {"text": "Da."},
    }
    cur = {**same, "b3:3": {"text": "da"}}
    doc = run(tmp_path, same, cur)
    assert doc["schema"] == "cadence.shadow/1"
    assert doc["scorer"] == "shadow_score@1"
    assert doc["calls"] == 2
    assert doc["hours"] == pytest.approx(90 / 3600)
    assert doc["utterances"] == 3
    assert doc["missing"] == {"candidate": 0, "current": 0}
    assert doc["divergence"]["wer"] == 0.0
    assert doc["divergence"]["ci"] == [0.0, 0.0]
    assert doc["worst"] == []
    assert doc["confidence"] == {"candidate": 0.9, "current": 0.9}
    lines = (tmp_path / "report" / "segments.jsonl").read_text(encoding="utf-8").splitlines()
    assert len(lines) == 3


def test_divergence_worst_and_missing(tmp_path: Path) -> None:
    cand = {"b3:1": {"text": "dobar dan"}, "b3:2": {"text": "hvala"}, "b3:3": {"text": "ne znam ništa"}}
    cur = {"b3:1": {"text": "dobar dan"}, "b3:2": {"text": "hvala lepo"}}
    doc = run(tmp_path, cand, cur, worst_segments=5, bootstrap_samples=200)
    assert doc["missing"] == {"candidate": 0, "current": 1}
    assert doc["calls"] == 1  # call B's only decoded segment lacks the current model's text
    assert doc["hours"] == pytest.approx(60 / 3600)
    assert doc["divergence"]["errors"] == 1
    assert doc["divergence"]["words"] == 4
    assert doc["divergence"]["wer"] == pytest.approx(0.25)
    lo, hi = doc["divergence"]["ci"]
    assert 0.0 <= lo <= 0.25 <= hi <= 1.0
    assert [w["audio"] for w in doc["worst"]] == ["b3:2"]
    w = doc["worst"][0]
    assert w["call"] == CALL_A
    assert (w["candidate"], w["current"], w["wer"]) == ("hvala", "hvala lepo", 0.5)
    assert doc["callList"] == [
        {"call": CALL_A, "duration": 60.0, "segments": 2, "wer": 0.25, "errors": 1, "words": 4},
    ]


def test_nothing_decoded_by_both_fails(tmp_path: Path) -> None:
    with pytest.raises(StepInputError, match="no segment was decoded by both"):
        run(tmp_path, {"b3:1": {"text": "a"}}, {"b3:2": {"text": "b"}})


def test_bootstrap_is_seeded() -> None:
    from cadence_worker.steps.shadow_score import Call

    calls = [Call("a", errors=1, words=10), Call("b", errors=5, words=10), Call("c", errors=0, words=10)]
    assert bootstrap(calls, 500, 0.95, 1) == bootstrap(calls, 500, 0.95, 1)
    lo, hi = bootstrap(calls, 500, 0.95, 1)
    assert lo < 0.2 < hi
