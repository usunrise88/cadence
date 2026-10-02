"""wer_score@1: the scores artifact (docs/review/2026-10-02-phase-3-plan.md "The scores artifact")."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from test_normalize import BASIC, norm

from cadence_worker.cas import hash_file
from cadence_worker.registry import registry
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import WerScoreParams, WerScoreStep, partial_stability

SUMMARY_KEYS = {
    "schema",
    "scorer",
    "normalizer",
    "language",
    "utterances",
    "refWords",
    "refChars",
    "wer",
    "cer",
    "werNoPunct",
    "sub",
    "del",
    "ins",
    "charErrors",
    "buckets",
}
ROW_KEYS = {
    "audio",
    "group",
    "durationS",
    "ref",
    "hyp",
    "refWords",
    "sub",
    "del",
    "ins",
    "refChars",
    "charErrors",
    "ops",
}


def dataset(root: Path, rows: list[dict[str, Any]]) -> list[str]:
    """A dataset artifact whose audio files are distinct bytes; returns the audio hashes in order."""
    root.mkdir(parents=True)
    (root / "dataset.json").write_text(json.dumps({"format": "cadence.dataset/1"}), encoding="utf-8")
    hashes = []
    with (root / "manifest.jsonl").open("w", encoding="utf-8") as f:
        for i, r in enumerate(rows):
            rel = f"audio/{i:02d}.wav"
            (root / "audio").mkdir(exist_ok=True)
            (root / rel).write_bytes(f"clip {i}".encode())
            hashes.append(hash_file(root / rel))
            f.write(json.dumps({"audio": rel, "language": "he-IL", **r}, ensure_ascii=False) + "\n")
    return hashes


def run(tmp: Path, refs: list[dict[str, Any]], hyps: list[str | dict[str, Any] | None], **over: Any) -> Path:
    hashes = dataset(tmp / "data", refs)
    with (tmp / "hyps.jsonl").open("w", encoding="utf-8") as f:
        for h, hyp in zip(hashes, hyps, strict=True):
            if hyp is None:
                continue
            row: dict[str, Any] = {"text": hyp} if isinstance(hyp, str) else dict(hyp)
            row.update({"audio": h, "family": "fam", "weightsHash": "b3:w", "decodingHash": "sha256:d"})
            row.setdefault("decoding", {"profile": "160ms"})
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    (tmp / "norm.json").write_text(json.dumps({**BASIC, "versionId": "ver_n", **over}), encoding="utf-8")
    out = tmp / "scores"
    events: list[dict[str, Any]] = []
    WerScoreStep().run(
        WerScoreParams(),
        {"hypotheses": tmp / "hyps.jsonl", "data": tmp / "data", "normalizer": tmp / "norm.json"},
        {"scores": out},
        StepContext(events.append, work_dir=tmp),
    )
    meta = next(e for e in events if e["e"] == "meta")
    assert meta["output"] == "scores"
    assert meta["meta"]["schema"] == "cadence.scores/1"
    return out


def read(out: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    summary = json.loads((out / "summary.json").read_text(encoding="utf-8"))
    rows = [json.loads(x) for x in (out / "utterances.jsonl").read_text(encoding="utf-8").splitlines()]
    return summary, rows


def test_registered_as_a_neutral_eval_kind() -> None:
    d = registry()["wer_score"]
    assert d["version"] == "1"
    assert d.get("neutral") is True
    assert "role" not in d
    assert d["consumes"] == {"hypotheses": "hypotheses", "data": "dataset", "normalizer": "normalizer"}
    assert d["produces"] == {"scores": "scores"}
    assert d["resources"]["jobKind"] == "eval"
    assert d["resources"]["gpu"] is False
    assert d["params"]["properties"]["duration_buckets_s"]["x-cadence"]["defaultRef"] == "eval.duration_buckets_s"
    assert WerScoreParams().duration_buckets_s == [0, 2, 5, 10, 20]


def test_scores_wer_cer_and_operations(tmp_path: Path) -> None:
    refs = [
        {"text": "Shalom, olam!", "duration": 1.0, "speaker": "s1"},
        {"text": "the cat sat on the mat", "duration": 3.0, "speaker": "s2"},
        {"text": "שָׁלוֹם עולם", "duration": 12.0, "speaker": "s1"},
    ]
    out = run(tmp_path, refs, ["shalom olam", "The bat sat on mat today.", "שלום"], removeMarks=True)
    summary, rows = read(out)
    assert set(summary) >= SUMMARY_KEYS
    assert summary["schema"] == "cadence.scores/1"
    assert summary["scorer"] == "wer_score@1"
    assert summary["normalizer"] == {"versionId": "ver_n", "hash": hash_file(tmp_path / "norm.json")}
    assert summary["language"] == "he-IL"
    assert summary["utterances"] == 3
    assert (summary["sub"], summary["del"], summary["ins"]) == (1, 2, 1)
    assert summary["refWords"] == 2 + 6 + 2
    assert summary["wer"] == pytest.approx(4 / 10)
    assert summary["werNoPunct"] == summary["wer"]  # the normalizer strips punctuation already
    assert summary["groups"] == "speaker"
    assert summary["hypotheses"] == {
        "family": "fam",
        "weightsHash": "b3:w",
        "decodingHash": "sha256:d",
        "profile": "160ms",
    }
    assert [r["group"] for r in rows] == ["s1", "s2", "s1"]
    assert all(set(r) >= ROW_KEYS for r in rows)
    assert rows[1]["ref"] == "the cat sat on the mat"
    assert rows[1]["hyp"] == "the bat sat on mat today"
    assert rows[1]["ops"] == [
        ["=", "the", "the"],
        ["S", "cat", "bat"],
        ["=", "sat", "sat"],
        ["=", "on", "on"],
        ["D", "the", None],
        ["=", "mat", "mat"],
        ["I", None, "today"],
    ]
    assert rows[2]["ref"] == "שלום עולם"
    assert rows[2]["del"] == 1
    # CER over the normalized texts, spaces included.
    assert rows[0]["charErrors"] == 0
    assert rows[2]["charErrors"] == len(" עולם")
    assert summary["charErrors"] == sum(r["charErrors"] for r in rows)
    assert summary["cer"] == pytest.approx(summary["charErrors"] / summary["refChars"])
    # Buckets [0,2) [2,5) [5,10) [10,20) [20,∞).
    b = summary["buckets"]
    assert [(x["lo"], x["hi"]) for x in b] == [(0, 2), (2, 5), (5, 10), (10, 20), (20, None)]
    assert [x["utterances"] for x in b] == [1, 1, 0, 1, 0]
    assert b[1]["wer"] == pytest.approx(3 / 6)
    assert b[2]["wer"] is None
    assert "stability" not in summary


def test_wer_no_punct_when_the_normalizer_keeps_punctuation(tmp_path: Path) -> None:
    out = run(tmp_path, [{"text": "Hello, world.", "duration": 1}], ["hello world"], punctuation="keep")
    summary, rows = read(out)
    assert summary["wer"] == 1.0
    assert summary["werNoPunct"] == 0.0
    assert rows[0]["ref"] == "hello, world."


def test_groups_prefer_calls_then_speakers_then_audio(tmp_path: Path) -> None:
    out = run(
        tmp_path / "a",
        [{"text": "a", "callId": "c1", "speaker": "x"}, {"text": "b", "callId": "c2", "speaker": "x"}],
        ["a", "b"],
    )
    summary, rows = read(out)
    assert summary["groups"] == "call"
    assert [r["group"] for r in rows] == ["c1", "c2"]
    out = run(tmp_path / "b", [{"text": "a", "speaker": "x"}, {"text": "b"}], ["a", "b"])
    summary, rows = read(out)
    assert summary["groups"] == "utterance"
    assert all(r["group"] == r["audio"] for r in rows)
    assert "speaker" in rows[0]
    assert "speaker" not in rows[1]


def test_missing_hypotheses_are_an_input_error(tmp_path: Path) -> None:
    with pytest.raises(StepInputError, match="1 of 2 utterances have no hypothesis"):
        run(tmp_path, [{"text": "a"}, {"text": "b"}], ["a", None])


def test_duplicate_hypotheses_and_bad_buckets_are_input_errors(tmp_path: Path) -> None:
    hashes = dataset(tmp_path / "data", [{"text": "a"}])
    (tmp_path / "hyps.jsonl").write_text(
        "\n".join(json.dumps({"audio": hashes[0], "text": "a"}) for _ in range(2)), encoding="utf-8"
    )
    (tmp_path / "norm.json").write_text(json.dumps(BASIC), encoding="utf-8")
    inputs = {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "normalizer": tmp_path / "norm.json"}
    ctx = StepContext(lambda e: None, work_dir=tmp_path)
    with pytest.raises(StepInputError, match="second hypothesis"):
        WerScoreStep().run(WerScoreParams(), inputs, {"scores": tmp_path / "s1"}, ctx)
    (tmp_path / "hyps.jsonl").write_text(json.dumps({"audio": hashes[0], "text": "a"}), encoding="utf-8")
    with pytest.raises(StepInputError, match="ascending"):
        WerScoreStep().run(WerScoreParams(duration_buckets_s=[0, 5, 2]), inputs, {"scores": tmp_path / "s2"}, ctx)
    (tmp_path / "norm.json").write_text("{}", encoding="utf-8")
    with pytest.raises(StepInputError, match="normalizer"):
        WerScoreStep().run(WerScoreParams(), inputs, {"scores": tmp_path / "s3"}, ctx)


def test_partial_stability(tmp_path: Path) -> None:
    n = norm()
    # "I" "scream" → "ice" "cream": of the four words shown, two (I, scream) are changed by a later partial.
    partials = [
        {"audioOffsetMs": 160, "text": "I"},
        {"audioOffsetMs": 320, "text": "I scream"},
        {"audioOffsetMs": 480, "text": "Ice cream"},
        {"audioOffsetMs": 640, "text": "Ice cream", "final": True},
    ]
    st = partial_stability(partials, "Ice cream", n, 2.0)
    assert (st.partial_words, st.unstable_words, st.edits) == (4, 2, 2)
    assert st.to_json() == {"partialWords": 4, "unstableWords": 2, "ratio": 0.5, "editsPerSecond": 1.0}
    # A stable stream: every shown word survives.
    stable = [{"text": "a"}, {"text": "a b"}, {"text": "a b c"}]
    assert partial_stability(stable, "a b c", n, 1.0).unstable_words == 0
    # The final differs from the last partial (dropped word): the final counts as one more emission.
    st = partial_stability([{"text": "a b"}], "a", n, 1.0)
    assert (st.partial_words, st.unstable_words, st.edits) == (2, 1, 1)
    # Through the step: stability appears when hypotheses carry partials.
    out = run(tmp_path, [{"text": "ice cream", "duration": 2.0}], [{"text": "Ice cream", "partials": partials}])
    summary, _ = read(out)
    assert summary["stability"] == {"partialWords": 4, "unstableWords": 2, "ratio": 0.5, "editsPerSecond": 1.0}
