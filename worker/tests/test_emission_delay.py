"""latency_score@3's emission delay (R54; phase 4 stream L): reference words timed by an ``alignment`` artifact against
the partials of a streaming decode, PR50/PR90, and n/a — never an estimate — without aligned references."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from test_metric_scorers import hyps_file, read
from test_wer_score import dataset

from cadence_worker.steps.context import StepContext
from cadence_worker.steps.latency_score import LatencyScoreParams, LatencyScoreStep, first_stable

BASIC_NORM = {
    "locale": "*",
    "unicode": "NFKC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": False,
    "mappings": [],
    "numbers": "keep",
}
TEXT = "One, two three."


def inputs_for(
    tmp_path: Path, words: list[dict[str, Any]] | None, row: dict[str, Any] | None = None
) -> dict[str, Path]:
    hashes = dataset(tmp_path / "data", [{"text": TEXT, "duration": 1.5}])
    partials = [
        {"audioOffsetMs": 320, "emitMs": 900, "text": "one", "step": 1},
        {"audioOffsetMs": 640, "emitMs": 1800, "text": "one too", "step": 3},
        {"audioOffsetMs": 960, "emitMs": 2700, "text": "one two three", "step": 5},
        {"audioOffsetMs": 1120, "emitMs": 3000, "text": "One two three.", "final": True, "step": 6},
    ]
    steps = [[160 * (k + 1), 10] for k in range(7)]
    hyps_file(tmp_path / "hyps.jsonl", hashes, [{"text": "One two three.", "partials": partials, "steps": steps}])
    (tmp_path / "vad.jsonl").write_text(json.dumps({"audio": hashes[0], "speechEndS": 1.0}) + "\n", encoding="utf-8")
    (tmp_path / "norm.json").write_text(json.dumps(BASIC_NORM), encoding="utf-8")
    inputs = {
        "hypotheses": tmp_path / "hyps.jsonl",
        "data": tmp_path / "data",
        "vad": tmp_path / "vad.jsonl",
        "normalizer": tmp_path / "norm.json",
    }
    if words is not None or row is not None:
        line = row or {"text": TEXT, "language": "en", "aligned": True, "words": words}
        header = {"alignment": {"format": "cadence.alignment/1", "aligner": {"auxiliary": "a"}}}
        (tmp_path / "align.jsonl").write_text(
            json.dumps(header) + "\n" + json.dumps({"audio": hashes[0], **line}) + "\n", encoding="utf-8"
        )
        inputs["alignment"] = tmp_path / "align.jsonl"
    return inputs


def run(tmp_path: Path, inputs: dict[str, Path]) -> tuple[dict[str, Any], list[dict[str, Any]], StepContext]:
    out = tmp_path / "scores"
    ctx = StepContext(lambda e: None, work_dir=tmp_path)
    LatencyScoreStep().run(LatencyScoreParams(), inputs, {"scores": out}, ctx)
    summary, rows = read(out)
    return summary, rows, ctx


def word(index: int, w: str, start: float, end: float) -> dict[str, Any]:
    return {"index": index, "word": w, "start": start, "end": end, "score": 0.9}


def test_first_stable() -> None:
    pw = [["one"], ["one", "too"], ["one", "two"], ["one", "two"]]
    assert first_stable(pw, 0, "one") == 0
    assert first_stable(pw, 1, "two") == 2
    assert first_stable(pw, 2, "three") == 3  # never shown in place: the last partial (the final) emits it


def test_emission_delay_from_aligned_word_ends(tmp_path: Path) -> None:
    words = [word(0, "One,", 0.1, 0.3), word(1, "two", 0.4, 0.8), word(2, "three.", 0.82, 0.9)]
    summary, rows, ctx = run(tmp_path, inputs_for(tmp_path, words))
    em = summary["emission"]
    # Chunk k is done at 160 (k + 1) + 10 ms. "one" appears at step 1 (330 ms) and stays: 330 - 300 = 30 ms; "two"
    # settles at step 5 (970 ms; "too" before it): 970 - 800 = 170 ms; "three" at step 5: 970 - 900 = 70 ms.
    assert em["available"] is True
    assert em["matchedWords"] == 3
    assert em["earlyWords"] == 0
    assert em["pr50Ms"] == pytest.approx(70.0)
    assert em["pr90Ms"] == pytest.approx(150.0)
    assert em["aligner"] == {"auxiliary": "a"}
    assert rows[0]["emission"] == {"words": 3, "pr50Ms": 70.0, "pr90Ms": 150.0}
    assert ctx.final_metrics["emission_delay_pr90_ms"] == pytest.approx(150.0)
    assert ctx.meta["scores"]["emission"]["pr50Ms"] == pytest.approx(70.0)


def test_emission_delay_skips_untimed_words_and_keeps_early_ones(tmp_path: Path) -> None:
    words = [word(0, "One,", 0.1, 0.3), word(2, "three.", 0.82, 1.0)]  # "two" has no timing
    em = run(tmp_path, inputs_for(tmp_path, words))[0]["emission"]
    assert em["matchedWords"] == 2
    assert em["earlyWords"] == 1  # three: 970 - 1000 = -30 ms, kept as is
    assert em["pr50Ms"] == pytest.approx(0.0)


def test_emission_delay_is_unavailable_without_an_alignment(tmp_path: Path) -> None:
    summary, rows, _ = run(tmp_path, inputs_for(tmp_path, None))
    assert summary["available"] is True  # latency to final does not need it
    assert summary["emission"]["available"] is False
    assert "align_reference" in summary["emission"]["reason"]
    assert summary["emission"].get("pr50Ms") is None
    assert all("emission" not in r for r in rows)


def test_emission_delay_never_estimates_unaligned_references(tmp_path: Path) -> None:
    row = {"text": TEXT, "aligned": False, "reason": "auxiliary/omniasr-ctc-1b does not cover th-TH"}
    em = run(tmp_path, inputs_for(tmp_path, None, row))[0]["emission"]
    assert em["available"] is False
    assert em["pr50Ms"] is None
    assert em["pr90Ms"] is None
    assert em["unalignedUtterances"] == 1
    assert "does not cover th-TH" in em["reason"]


def test_emission_delay_ignores_an_alignment_of_another_text(tmp_path: Path) -> None:
    row = {"text": "something else", "aligned": True, "words": [word(0, "something", 0.1, 0.3)]}
    em = run(tmp_path, inputs_for(tmp_path, None, row))[0]["emission"]
    assert em["available"] is False
    assert em["mismatchedUtterances"] == 1


def test_emission_delay_at_80ms_chunks_follows_word_completion() -> None:
    """Synthetic 80 ms partials (the 2026-10-04 check of the 80 ms emission delay): words grow token by token, and
    each word is emitted by the chunk that completes it, not by the final. Partials that split a word ("Dan iel")
    put every later word one place off, so only the final emits them — nemotron_transcribe@3's bug, not the
    scorer's."""
    from cadence_worker.normalize import Normalizer
    from cadence_worker.steps.latency_score import emission
    from cadence_worker.steps.wer_score import Reference

    norm = Normalizer.from_json(json.dumps(BASIC_NORM))
    ref = Reference(audio="b3:a", text="Daniel Lantane je", duration=3.0, language="hr")
    ends = [0.5, 1.1, 1.3]
    timed = [word(i, w, e - 0.3, e) for i, (w, e) in enumerate(zip(ref.text.split(), ends, strict=True))]
    alignment = ({"aligner": {"auxiliary": "a"}}, {"b3:a": {"text": ref.text, "aligned": True, "words": timed}})

    def hyp(texts: list[str]) -> dict[str, Any]:
        parts = [{"audioOffsetMs": 80 * (k + 5), "emitMs": 80 * (k + 5), "text": t} for k, t in enumerate(texts)]
        parts.append({"audioOffsetMs": 3000, "emitMs": 3000, "text": "Daniel Lantane je", "final": True})
        return {"text": "Daniel Lantane je", "partials": parts, "decoding": {"pace": "realtime"}}

    # one partial per 80 ms chunk from 400 ms: Daniel complete at 560, Lantane at 880, je at 1440
    good = ["Da", "Danie", "Daniel", "Daniel La", "Daniel Lan", "Daniel Lanta", "Daniel Lantane"]
    good += ["Daniel Lantane"] * 6 + ["Daniel Lantane je"]
    em, _ = emission([ref], {"b3:a": hyp(good)}, norm, alignment)
    # 560 - 500 = 60, 880 - 1100 = -220 (early), 1440 - 1300 = 140
    assert (em["matchedWords"], em["earlyWords"], em["pr50Ms"]) == (3, 1, 60.0)
    split = [t.replace("Daniel", "Dan iel") if t.startswith("Daniel ") else t for t in good]
    em, _ = emission([ref], {"b3:a": hyp(split)}, norm, alignment)
    assert em["pr50Ms"] == 3000 - 1100, "every word after the split is emitted only by the final"
