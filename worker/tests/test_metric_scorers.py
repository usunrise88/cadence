"""entity_score@1 and latency_score@3: the metric_scores artifact (phase 3 stream R; emission delay: phase 4)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from test_wer_score import dataset

from cadence_worker.registry import registry
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.entity_score import EntityScoreParams, EntityScoreStep, entities, read_itn
from cadence_worker.steps.latency_score import LatencyScoreParams, LatencyScoreStep, final_index, paced_emits

ITN = {
    "format": "cadence.itn/1",
    "locale": "he-IL",
    "commit": "abc1234",
    "classes": [
        {
            "name": "number",
            "pattern": r"\d+(?:[.,]\d+)?",
            "examples": [{"spoken": "שלוש מאות ועשרים", "written": "320"}],
        },
        {"name": "phone", "pattern": r"0(?:5\d|[2-489]|7\d)-?\d{7}", "examples": []},
        {"name": "time", "pattern": r"\d{1,2}:\d{2}", "examples": [{"spoken": "שמונה וחצי", "written": "8:30"}]},
    ],
}


def hyps_file(path: Path, hashes: list[str], rows: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8") as f:
        for h, r in zip(hashes, rows, strict=True):
            f.write(json.dumps({"audio": h, **r}, ensure_ascii=False) + "\n")


def read(out: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    summary = json.loads((out / "summary.json").read_text(encoding="utf-8"))
    rows = [json.loads(x) for x in (out / "utterances.jsonl").read_text(encoding="utf-8").splitlines()]
    return summary, rows


def test_registered_as_neutral_metric_scorers() -> None:
    reg = registry()
    assert reg["entity_score"]["consumes"] == {"hypotheses": "hypotheses", "data": "dataset", "itn": "itn"}
    assert reg["latency_score"]["consumes"] == {
        "hypotheses": "hypotheses",
        "data": "dataset",
        "vad": "vad",
        "normalizer": "normalizer",
        "alignment": "alignment",
    }
    assert reg["latency_score"]["optionalInputs"] == ["alignment", "normalizer"]
    for k in ("entity_score", "latency_score"):
        assert reg[k]["produces"] == {"scores": "metric_scores"}
        assert reg[k].get("neutral") is True
        assert reg[k]["resources"]["gpu"] is False


def test_entities_resolve_overlaps_longest_first(tmp_path: Path) -> None:
    (tmp_path / "itn.json").write_text(json.dumps(ITN), encoding="utf-8")
    itn = read_itn(tmp_path / "itn.json")
    assert entities("התקשר ל 054-1234567 בשעה 8:30 עם 3 אנשים", itn) == [
        ("phone", "054-1234567"),
        ("time", "8:30"),
        ("number", "3"),
    ]


def test_entity_accuracy_per_class(tmp_path: Path) -> None:
    hashes = dataset(
        tmp_path / "data",
        [
            {"text": "המספר הוא 054-1234567 והסכום 320"},
            {"text": "נפגשים ב 8:30"},
            {"text": "אין כאן מספרים"},
        ],
    )
    hyps_file(
        tmp_path / "hyps.jsonl",
        hashes,
        [{"text": "המספר הוא 054-1234568 והסכום שלוש מאות ועשרים"}, {"text": "נפגשים ב שמונה וחצי"}, {"text": "אין 5"}],
    )
    (tmp_path / "itn.json").write_text(json.dumps(ITN), encoding="utf-8")
    out = tmp_path / "scores"
    EntityScoreStep().run(
        EntityScoreParams(),
        {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "itn": tmp_path / "itn.json"},
        {"scores": out},
        StepContext(lambda e: None, work_dir=tmp_path),
    )
    summary, rows = read(out)
    assert summary["schema"] == "cadence.metric-scores/1"
    assert summary["scorer"] == "entity_score@1"
    assert summary["metric"] == "entities"
    assert (summary["refEntities"], summary["hypEntities"], summary["correct"]) == (3, 4, 2)
    assert summary["accuracy"] == pytest.approx(2 / 3)
    by = {c["class"]: c for c in summary["classes"]}
    assert (by["phone"]["correct"], by["phone"]["refEntities"]) == (0, 1)
    assert (by["number"]["correct"], by["time"]["correct"]) == (1, 1)
    assert [r["index"] for r in rows] == [0, 1, 2]
    assert rows[2]["ref"] == []
    assert rows[2]["extra"] == [{"class": "number", "text": "5"}]


def test_entity_accuracy_counts_annotated_spans(tmp_path: Path) -> None:
    # References annotated in a batch carry spans (names, addresses) no ITN pattern finds.
    hashes = dataset(
        tmp_path / "data",
        [
            {"text": "קוראים לי דנה כהן", "entities": [{"start": 10, "end": 17, "class": "name", "text": "דנה כהן"}]},
            {
                "text": "אני גר ברחוב הרצל",
                "entities": [{"start": 6, "end": 17, "class": "address", "text": "ברחוב הרצל"}],
            },
        ],
    )
    hyps_file(tmp_path / "hyps.jsonl", hashes, [{"text": "קוראים לי  דנה   כהן"}, {"text": "אני גר ברחוב הרצליה"}])
    (tmp_path / "itn.json").write_text(json.dumps(ITN), encoding="utf-8")
    out = tmp_path / "scores"
    EntityScoreStep().run(
        EntityScoreParams(),
        {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "itn": tmp_path / "itn.json"},
        {"scores": out},
        StepContext(lambda e: None, work_dir=tmp_path),
    )
    summary, rows = read(out)
    by = {c["class"]: c for c in summary["classes"]}
    assert (by["name"]["refEntities"], by["name"]["correct"]) == (1, 1)
    # "ברחוב הרצל" is a substring of "ברחוב הרצליה": a containment check counts it found.
    assert (by["address"]["refEntities"], by["address"]["correct"]) == (1, 1)
    assert summary["utterancesWithEntities"] == 2
    assert rows[0]["ref"] == [{"class": "name", "text": "דנה כהן", "found": True, "annotated": True}]


def test_paced_emits_simulate_real_time() -> None:
    partials = [
        {"audioOffsetMs": 160, "emitMs": 10},
        {"audioOffsetMs": 320, "emitMs": 20},
        {"audioOffsetMs": 480, "emitMs": 400},  # a slow chunk: 380 ms of compute queues the next one
        {"audioOffsetMs": 640, "emitMs": 410},
    ]
    assert paced_emits(partials, realtime=False) == [170, 330, 860, 870]
    assert paced_emits(partials, realtime=True) == [10, 20, 400, 410]


def test_paced_emits_from_chunk_steps_charge_each_chunk_its_own_compute() -> None:
    """A batched file decode: four chunks of 160 ms, two silent (no event). From the events alone the third partial
    would be charged the wall time since the second (the silent chunk and the batch's other streams: 400 ms); from the
    chunk steps each chunk pays only its own share."""
    partials = [
        {"audioOffsetMs": 160, "emitMs": 50, "step": 0},
        {"audioOffsetMs": 480, "emitMs": 150, "step": 2},
        {"audioOffsetMs": 640, "emitMs": 550, "step": 3},
    ]
    steps = [(160.0, 10.0), (320.0, 10.0), (480.0, 10.0), (640.0, 300.0)]  # the last chunk is slower than real time
    assert paced_emits(partials, realtime=False, steps=steps) == [170, 490, 940]
    assert paced_emits(partials, realtime=False) == [210, 580, 1040]  # version 1's estimate, without the steps


def test_latency_from_chunk_steps(tmp_path: Path) -> None:
    hashes = dataset(tmp_path / "data", [{"text": "one two", "duration": 1.0}])
    partials = [
        {"audioOffsetMs": 320, "emitMs": 900, "text": "one", "step": 1},
        {"audioOffsetMs": 640, "emitMs": 1800, "text": "one two", "step": 3},
        {"audioOffsetMs": 960, "emitMs": 2700, "text": "one two", "final": True, "step": 5},
    ]
    # Six chunks of 160 ms decoded in a batch of 30: 900 ms per batch step, 30 ms each stream's share.
    steps = [[160 * (k + 1), 30] for k in range(6)]
    hyps_file(tmp_path / "hyps.jsonl", hashes, [{"text": "one two", "partials": partials, "steps": steps}])
    (tmp_path / "vad.jsonl").write_text(json.dumps({"audio": hashes[0], "speechEndS": 0.6}) + "\n", encoding="utf-8")
    out = tmp_path / "scores"
    LatencyScoreStep().run(
        LatencyScoreParams(),
        {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "vad": tmp_path / "vad.jsonl"},
        {"scores": out},
        StepContext(lambda e: None, work_dir=tmp_path),
    )
    summary, rows = read(out)
    # Settles at partial 1, chunk 3: available at 640 ms, done 30 ms later; speech ended at 600 ms → 70 ms, not the
    # 1240 ms the batch's wall clock would say.
    assert summary["scorer"] == "latency_score@3"
    assert summary["timing"] == "steps"
    assert rows[0]["finalAtMs"] == 670.0
    assert rows[0]["latencyMs"] == 70.0


def test_final_index_is_where_the_text_settles() -> None:
    ps = [{"text": "a"}, {"text": "a b"}, {"text": "a c"}, {"text": "a b"}, {"text": "a b"}]
    assert final_index(ps, "a b") == 3


def test_latency_to_final(tmp_path: Path) -> None:
    hashes = dataset(tmp_path / "data", [{"text": "one two", "duration": 1.0}, {"text": "three", "duration": 1.0}])
    p0 = [
        {"audioOffsetMs": 320, "emitMs": 5, "text": "one"},
        {"audioOffsetMs": 640, "emitMs": 10, "text": "one two"},
        {"audioOffsetMs": 960, "emitMs": 15, "text": "one two", "final": True},
    ]
    p1 = [{"audioOffsetMs": 320, "emitMs": 5, "text": ""}, {"audioOffsetMs": 640, "emitMs": 10, "text": "three"}]
    hyps_file(
        tmp_path / "hyps.jsonl",
        hashes,
        [
            {"text": "one two", "partials": p0, "decoding": {"profile": "320ms"}},
            {"text": "three", "partials": p1, "decoding": {"profile": "320ms"}},
        ],
    )
    with (tmp_path / "vad.jsonl").open("w", encoding="utf-8") as f:
        f.write(json.dumps({"vad": {"kind": "frame_vad@1", "model": "m", "revision": "r"}}) + "\n")
        f.write(json.dumps({"audio": hashes[0], "speechEndS": 0.5, "speech": [[0.1, 0.5]]}) + "\n")
        f.write(json.dumps({"audio": hashes[1], "speechEndS": 0.7, "speech": [[0.2, 0.7]]}) + "\n")
    out = tmp_path / "scores"
    LatencyScoreStep().run(
        LatencyScoreParams(),
        {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "vad": tmp_path / "vad.jsonl"},
        {"scores": out},
        StepContext(lambda e: None, work_dir=tmp_path),
    )
    summary, rows = read(out)
    assert summary["metric"] == "latency"
    assert summary["pace"] == "simulated"
    assert summary["vad"]["kind"] == "frame_vad@1"
    # utterance 0: settles at partial 1, emitted at max(640, 325) + 5 = 645; end 500 → 145 ms.
    # utterance 1: settles at partial 1, emitted at 645; end 700 → early, counted as 0.
    assert [r["latencyMs"] for r in rows] == [145.0, 0.0]
    assert summary["earlyFinals"] == 1
    assert summary["p50Ms"] == pytest.approx(72.5)
    assert summary["profile"] == "320ms"


def test_latency_without_partials_is_unavailable(tmp_path: Path) -> None:
    hashes = dataset(tmp_path / "data", [{"text": "one"}])
    hyps_file(tmp_path / "hyps.jsonl", hashes, [{"text": "one"}])
    (tmp_path / "vad.jsonl").write_text(json.dumps({"audio": hashes[0], "speechEndS": 0.5}) + "\n", encoding="utf-8")
    out = tmp_path / "scores"
    LatencyScoreStep().run(
        LatencyScoreParams(),
        {"hypotheses": tmp_path / "hyps.jsonl", "data": tmp_path / "data", "vad": tmp_path / "vad.jsonl"},
        {"scores": out},
        StepContext(lambda e: None, work_dir=tmp_path),
    )
    summary, rows = read(out)
    assert summary["available"] is False
    assert "partial events" in summary["reason"]
    assert rows == []
