"""pseudolabel_ensemble@2 against the hand-written segments fixture (cadence.segments/1, the D -> X interface):
agreement after the scoring normalizer, the pick, LID, and the disputes the control plane queues for triage."""

from __future__ import annotations

import json
import shutil
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
    written_form,
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
    rows = {r["hash"]: r for r in read_segments(out["segments"])[1]}
    return rows, ctx


def test_pairwise_wer_is_symmetric_over_the_longer_text() -> None:
    assert pairwise_wer(["a", "b"], ["a", "b"]) == 0
    assert pairwise_wer(["a", "b", "c", "d"], ["a", "b", "c"]) == pairwise_wer(["a", "b", "c"], ["a", "b", "c", "d"])
    assert pairwise_wer(["a", "b", "c", "d"], ["a", "b", "c"]) == 0.25
    assert pairwise_wer([], []) == 0
    assert pairwise_wer(["a"], []) == 1


def test_the_fixture_reads_as_segments() -> None:
    _, rows = read_segments(FIXTURE)
    assert [r["role"] for r in rows] == ["caller", "bot", "caller", "caller", "caller"]
    assert rows[0]["uri"].startswith("mount://corpora/")


def test_ensemble_labels_agreeing_segments_and_disputes_the_rest(tmp_path: Path) -> None:
    rows, ctx = _run(tmp_path, _members(tmp_path), require_lid=False)
    # 1: Whisper and OASIS agree exactly after the normalizer (pomoc/pomoć puts the third member 0.2 away from both);
    # Whisper's text is in written form (capitals, punctuation), so it wins over OASIS's spoken form, a vote or not.
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[1]]["text"] == "Dobar dan, treba mi pomoć."
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
    assert next(h for h in hyp if h["audio"] == H[1])["pick"] == "whisper-large-v3"


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
    assert rows[H[4]]["text"] == "Hvala, doviđenja."
    # Every labelled row carries its LID verdict for manifest_filter, kept or disputed.
    assert rows[H[1]]["lid"] == {"language": "hr", "confidence": 0.8, "agrees": True, "source": "lid"}
    assert rows[H[3]]["lid"]["language"] == "sr"
    assert "lid" not in rows[H[2]], "a passed-through row without LID evidence gets none"


def test_lid_reaches_manifest_filter(tmp_path: Path) -> None:
    from cadence_worker.steps.manifest_filter import ManifestFilterParams, reason

    rows, _ = _run(tmp_path, _members(tmp_path), require_lid=False)
    # Whisper heard Russian in segment 4: disputed, and its row says so where manifest_filter reads it.
    assert rows[H[4]]["lid"] == {"language": "ru", "confidence": 0.7, "agrees": False, "source": "members"}
    p = ManifestFilterParams(drop_origins=[])  # past the origin rule, the LID rule must still drop it
    assert reason(p, {**rows[H[4]], "duration": 2.4}) == "lid_mismatch"
    assert rows[H[1]]["lid"]["language"] == "sr"
    assert reason(p, {**rows[H[1]], "duration": 2.7}) is None


def test_header_steps_files_and_repeated_segments(tmp_path: Path) -> None:
    segs = tmp_path / "segments"
    shutil.copytree(FIXTURE, segs)
    lines = (segs / "segments.jsonl").read_text(encoding="utf-8").splitlines()
    (segs / "segments.jsonl").write_text("\n".join([*lines, lines[0]]) + "\n", encoding="utf-8")
    (segs / "files.jsonl").write_text('{"uri":"mount://corpora/calls-synth-sr/r1/call-0001.wav"}\n', encoding="utf-8")
    inputs = {**_members(tmp_path), "segments": segs}
    ctx = StepContext(lambda e: None, work_dir=tmp_path)
    out = {"segments": tmp_path / "out-segments", "hypotheses": tmp_path / "out-hyp.jsonl"}
    PseudolabelEnsembleStep().run(EnsembleParams(require_lid=False), inputs, out, ctx)
    header, rows = read_segments(out["segments"])
    assert header["steps"][-1] == "pseudolabel_ensemble@2"
    assert header["counts"] == {"segments": 6}
    assert (out["segments"] / "files.jsonl").is_file(), "files.jsonl goes on (annotation samples by file)"
    twins = [r for r in rows if r["hash"] == H[1]]
    assert len(twins) == 2
    assert twins[0]["text"] == twins[1]["text"]
    assert twins[0]["origin"] == twins[1]["origin"] == ORIGIN_PSEUDO
    assert ctx.meta["segments"]["pseudoLabelled"] == 2
    hyp = [json.loads(x)["audio"] for x in out["hypotheses"].read_text(encoding="utf-8").splitlines()]
    assert len(hyp) == len(set(hyp)), "one hypotheses row per audio"


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
    with pytest.raises(StepInputError, match="at least 2 members"):
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


def _two_members(tmp: Path) -> dict[str, Path]:
    """The default template's ensemble (owner decision 2026-10-04): Whisper and OASIS, no base model."""
    inputs = _members(tmp)
    del inputs["hypotheses.2"]
    return inputs


def test_written_form_is_capitals_or_sentence_punctuation() -> None:
    assert written_form("Dobar dan, treba mi pomoć.")
    assert written_form("dobar dan.")
    assert written_form("Marko")
    assert written_form("שלום, מה שלומך?")  # no case in Hebrew: punctuation decides
    assert not written_form("dobar dan treba mi pomoć")
    assert not written_form("don't e-mail me")  # apostrophes and hyphens sit inside spoken-form words too
    assert not written_form("שלום מה שלומך")
    assert not written_form("")


def test_whisper_and_oasis_keep_whispers_written_text(tmp_path: Path) -> None:
    rows, ctx = _run(tmp_path, _two_members(tmp_path), require_lid=False)
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[1]]["text"] == "Dobar dan, treba mi pomoć."
    assert rows[H[1]]["confidence"] == pytest.approx(1.0)
    assert rows[H[3]]["dispute"]["reason"] == "disagreement"  # struju/vodu: 0.2 apart
    assert rows[H[4]]["dispute"]["reason"] == "lid-mismatch"  # Whisper's own LID says Russian
    assert rows[H[5]]["dispute"]["reason"] == "no-speech"
    assert ctx.meta["segments"]["members"] == ["oasis", "whisper-large-v3"]
    hyp = [json.loads(x) for x in (tmp_path / "out-hyp.jsonl").read_text(encoding="utf-8").splitlines()]
    assert next(h for h in hyp if h["audio"] == H[1])["pick"] == "whisper-large-v3"


def test_without_the_written_form_rule_the_vote_wins(tmp_path: Path) -> None:
    rows, _ = _run(tmp_path, _two_members(tmp_path), require_lid=False, prefer_written_form=False)
    assert rows[H[1]]["text"] == "dobar dan treba mi pomoć"


def test_two_members_with_lid_agree_on_both_languages(tmp_path: Path) -> None:
    inputs = _two_members(tmp_path)
    inputs["lid"] = _jsonl(
        tmp_path / "lid.jsonl",
        [{"audio": H[1], "language": "hr", "confidence": 0.8}, {"audio": H[4], "language": "sr", "confidence": 0.9}],
    )
    rows, _ = _run(tmp_path, inputs)
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[1]]["lid"]["agrees"] is True
    # The lid row decides over Whisper's "ru"; Whisper and OASIS agree, Whisper's text is kept.
    assert rows[H[4]]["origin"] == ORIGIN_PSEUDO
    assert rows[H[4]]["text"] == "Hvala, doviđenja."
    # Segment 3 has no lid row: Whisper's own detection ("sr") is the evidence.
    assert rows[H[3]]["lid"]["source"] == "members"


def test_a_segment_one_member_skipped_is_disputed(tmp_path: Path) -> None:
    inputs = _two_members(tmp_path)
    oasis = [json.loads(x) for x in inputs["hypotheses.1"].read_text(encoding="utf-8").splitlines()]
    _jsonl(inputs["hypotheses.1"], [r for r in oasis if r["audio"] != H[1]])
    rows, _ = _run(tmp_path, inputs, require_lid=False)
    assert rows[H[1]]["dispute"]["reason"] == "too-few-members"
    assert rows[H[1]]["confidence"] == 0


def test_min_members_refuses_a_smaller_ensemble(tmp_path: Path) -> None:
    with pytest.raises(StepInputError, match="at least 3 members"):
        _run(tmp_path, _two_members(tmp_path), min_members=3)
    # Wired with three, a segment only two members answered is too few.
    rows, _ = _run(tmp_path, _members(tmp_path), require_lid=False, min_members=3)
    assert rows[H[5]]["dispute"]["reason"] == "too-few-members"
    assert rows[H[1]]["origin"] == ORIGIN_PSEUDO
