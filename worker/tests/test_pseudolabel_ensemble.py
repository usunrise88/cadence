"""pseudolabel_ensemble@1 against the hand-written segments fixture (cadence.segments/1, the D -> X interface):
agreement after the scoring normalizer, the pick, LID, and the disputes the control plane queues for triage."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from cadence_worker.segments import ORIGIN_DISPUTED, ORIGIN_PSEUDO, read_segments
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.pseudolabel_ensemble import (
    Candidate,
    EnsembleParams,
    PseudolabelEnsembleStep,
    decide,
    pairwise_wer,
)

FIXTURE = Path(__file__).resolve().parent / "fixtures" / "segments-sr"
H = {i: "b3:" + str(i) * 64 for i in range(1, 6)}
BASIC = {
    "locale": "*",
    "unicode": "NFKC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": False,
    "mappings": [],
    "numbers": "keep",
}


def _jsonl(path: Path, rows: list[dict[str, Any]]) -> Path:
    path.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")
    return path


def _members(tmp: Path) -> dict[str, Path]:
    whisper: list[dict[str, Any]] = [
        {"audio": H[1], "text": "Dobar dan, treba mi pomoć.", "member": "whisper-large-v3", "detectedLanguage": "sr",
         "languageConfidence": 0.81},
        {"audio": H[3], "text": "Zovem zbog računa za struju.", "member": "whisper-large-v3", "detectedLanguage": "sr"},
        {"audio": H[4], "text": "Hvala, doviđenja.", "member": "whisper-large-v3", "detectedLanguage": "ru",
         "languageConfidence": 0.7},
        {"audio": H[5], "text": "", "member": "whisper-large-v3"},
    ]  # fmt: skip
    oasis: list[dict[str, Any]] = [
        {"audio": H[1], "text": "dobar dan treba mi pomoć", "member": "oasis", "vote": True, "confidence": 0.93},
        {"audio": H[3], "text": "zovem zbog računa za vodu", "member": "oasis", "vote": True},
        {"audio": H[4], "text": "hvala doviđenja", "member": "oasis", "vote": True},
        {"audio": H[5], "text": "", "member": "oasis", "vote": True},
    ]
    nemo: list[dict[str, Any]] = [
        {"audio": H[1], "text": "Dobar dan, treba mi pomoc.", "member": "nemotron"},
        {"audio": H[3], "text": "Zovem se Marko.", "member": "nemotron"},
        {"audio": H[4], "text": "Hvala, doviđenja!", "member": "nemotron"},
    ]
    (tmp / "norm.json").write_text(json.dumps(BASIC), encoding="utf-8")
    return {
        "segments": FIXTURE,
        "hypotheses.0": _jsonl(tmp / "h0.jsonl", whisper),
        "hypotheses.1": _jsonl(tmp / "h1.jsonl", oasis),
        "hypotheses.2": _jsonl(tmp / "h2.jsonl", nemo),
        "normalizer": tmp / "norm.json",
    }


def _run(tmp: Path, inputs: dict[str, Path], **params: Any) -> tuple[dict[str, dict[str, Any]], StepContext]:
    ctx = StepContext(lambda e: None, work_dir=tmp)
    out = {"segments": tmp / "out-segments", "hypotheses": tmp / "out-hyp.jsonl"}
    PseudolabelEnsembleStep().run(EnsembleParams(**params), inputs, out, ctx)
    rows = {r["hash"]: r for r in read_segments(out["segments"])}
    return rows, ctx


def test_pairwise_wer_is_symmetric_over_the_longer_text() -> None:
    assert pairwise_wer(["a", "b"], ["a", "b"]) == 0
    assert pairwise_wer(["a", "b", "c", "d"], ["a", "b", "c"]) == pairwise_wer(["a", "b", "c"], ["a", "b", "c", "d"])
    assert pairwise_wer(["a", "b", "c", "d"], ["a", "b", "c"]) == 0.25
    assert pairwise_wer([], []) == 0
    assert pairwise_wer(["a"], []) == 1


def test_the_fixture_reads_as_segments() -> None:
    rows = read_segments(FIXTURE)
    assert [r["role"] for r in rows] == ["caller", "bot", "caller", "caller", "caller"]
    assert rows[0]["uri"].startswith("mount://corpora/")


def test_ensemble_labels_agreeing_segments_and_disputes_the_rest(tmp_path: Path) -> None:
    rows, ctx = _run(tmp_path, _members(tmp_path), require_lid=False)
    # 1: Whisper and OASIS agree exactly after the normalizer (pomoc/pomoć puts the third member 0.2 away from both),
    # and OASIS is a vote, so its text wins.
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[1]]["text"] == "dobar dan treba mi pomoć"
    assert 0 < rows[H[1]]["confidence"] <= 1
    # 2: the bot channel's TTS script passes unchanged.
    assert rows[H[2]]["origin"] == "tts-script"
    assert rows[H[2]]["text"] == "Dobar dan, kako mogu da vam pomognem?"
    # 3: struju/vodu is one edit in five words (0.2 > 0.15) and the third member heard something else.
    assert rows[H[3]]["origin"] == ORIGIN_DISPUTED
    assert rows[H[3]]["dispute"]["reason"] == "disagreement"
    assert {c["member"] for c in rows[H[3]]["dispute"]["candidates"]} == {"whisper-large-v3", "oasis", "nemotron"}
    # 4: the texts agree, but Whisper's own LID says Russian for a Serbian segment.
    assert rows[H[4]]["dispute"]["reason"] == "lid-mismatch"
    # 5: nobody heard speech; and only two members answered.
    assert rows[H[5]]["dispute"]["reason"] == "no-speech"
    assert ctx.meta["segments"]["disputed"] == 3
    assert ctx.meta["segments"]["pseudoLabelled"] == 1
    assert ctx.meta["segments"]["passedThrough"] == 1
    assert ctx.final_metrics["disputed_share"] == pytest.approx(0.75)
    hyp = [json.loads(line) for line in (tmp_path / "out-hyp.jsonl").read_text(encoding="utf-8").splitlines()]
    assert {h["audio"] for h in hyp} == {H[1], H[3], H[4], H[5]}
    assert next(h for h in hyp if h["audio"] == H[1])["pick"] == "oasis"


def test_lid_input_decides_and_equivalent_languages_agree(tmp_path: Path) -> None:
    inputs = _members(tmp_path)
    inputs["lid"] = _jsonl(
        tmp_path / "lid.jsonl",
        [
            {"audio": H[1], "language": "hr", "confidence": 0.8},  # sr, hr and bs count as one
            {"audio": H[3], "language": "sr", "confidence": 0.9},
            {"audio": H[4], "language": "sr", "confidence": 0.9},  # overrides Whisper's "ru"
        ],
    )
    rows, _ = _run(tmp_path, inputs)
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[4]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[4]]["text"] == "hvala doviđenja"


def test_require_lid_disputes_segments_without_evidence(tmp_path: Path) -> None:
    inputs = _members(tmp_path)
    # Without Whisper's detections and without a lid input there is no LID evidence at all.
    for name in ("hypotheses.0",):
        rows = [json.loads(x) for x in inputs[name].read_text(encoding="utf-8").splitlines()]
        _jsonl(
            inputs[name],
            [{k: v for k, v in r.items() if k not in ("detectedLanguage", "languageConfidence")} for r in rows],
        )
    rows_out, _ = _run(tmp_path, inputs)
    assert rows_out[H[1]]["dispute"]["reason"] == "lid-unknown"


def test_decide_prefers_the_lowest_mean_wer_without_a_voter() -> None:
    p = EnsembleParams(require_lid=False)

    def c(member: str, text: str) -> Candidate:
        return Candidate(member=member, text=text, words=text.lower().split())

    v = decide([c("a", "x y z w"), c("b", "x y z w"), c("c", "q r s t")], "", None, p)
    assert v.origin == ORIGIN_PSEUDO
    assert v.pick in ("a", "b")
    assert v.confidence == pytest.approx(2 / 3, abs=1e-4)
    assert decide([c("a", "x")], "", None, p).reason == "too-few-members"


def test_two_members_are_required(tmp_path: Path) -> None:
    inputs = _members(tmp_path)
    del inputs["hypotheses.1"], inputs["hypotheses.2"]
    with pytest.raises(StepInputError, match="at least two"):
        _run(tmp_path, inputs)


def test_context_carries_the_resolved_auxiliary(tmp_path: Path) -> None:
    ref = {
        "versionId": "ver_1",
        "name": "auxiliary/oasis",
        "version": "2026-10-03.abc",
        "payload": {"roles": ["pseudolabel"]},
    }
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": ref})
    assert ctx.auxiliary("auxiliary")["name"] == "auxiliary/oasis"
    with pytest.raises(StepInputError, match="registry version"):
        ctx.auxiliary("other")


def test_auxiliary_unavailable_is_a_retryable_step_error() -> None:
    from cadence_worker.errors import classify
    from cadence_worker.steps.base import AuxiliaryUnavailable

    err = classify(AuxiliaryUnavailable("GetModelInfo at host:50051: UNAVAILABLE"))
    assert err["type"] == "step"
    assert err.get("retryable") is True
    assert err["message"].startswith("auxiliary-unavailable: ")
