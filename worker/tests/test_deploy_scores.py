"""parity_score@1 and benchmark_score@1, the neutral judges of phase 5 (docs/spec/03 "Export, parity and benchmark")."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from test_normalize import BASIC
from test_wer_score import dataset

from cadence_worker.registry import registry
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.benchmark_score import BenchmarkScoreParams, BenchmarkScoreStep, judge, percentiles
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.parity_score import ParityScoreParams, ParityScoreStep

REFS = [{"text": "one two three four"}, {"text": "five six seven eight"}, {"text": "nine ten"}, {"text": "a b c d"}]


def hyps(path: Path, hashes: list[str], rows: list[dict[str, Any]]) -> Path:
    with path.open("w", encoding="utf-8") as f:
        for h, r in zip(hashes, rows, strict=True):
            f.write(
                json.dumps({"audio": h, "family": "fam", "weightsHash": "b3:w", "decodingHash": "sha256:d", **r}) + "\n"
            )
    return path


def parity(
    tmp: Path, ref: list[dict[str, Any]], srv: list[dict[str, Any]], smoke: bool = False, **over: Any
) -> dict[str, Any]:
    hashes = dataset(tmp / "data", REFS)
    (tmp / "norm.json").write_text(json.dumps({**BASIC, "versionId": "ver_n"}), encoding="utf-8")
    inputs = {
        "reference": hyps(tmp / "ref.jsonl", hashes, ref),
        "served": hyps(tmp / "srv.jsonl", hashes, srv),
        "data": tmp / "data",
        "normalizer": tmp / "norm.json",
    }
    if smoke:
        s = tmp / "smoke"
        s.mkdir()
        items = []
        for i, h in enumerate(hashes[:3]):
            (s / f"{i + 1:02d}.json").write_text(json.dumps({"chunks": [i]}), encoding="utf-8")
            items.append({"audio": h, "file": f"{i + 1:02d}.json"})
        (s / "smoke.json").write_text(
            json.dumps({"schema": "cadence.smoke-inputs/1", "format": "cadence.nemo-chunks/1", "items": items}),
            encoding="utf-8",
        )
        inputs["smoke"] = s
    out = tmp / "report"
    events: list[dict[str, Any]] = []
    ParityScoreStep().run(ParityScoreParams(**over), inputs, {"report": out}, StepContext(events.append, work_dir=tmp))
    doc: dict[str, Any] = json.loads((out / "report.json").read_text(encoding="utf-8"))
    return doc


def test_parity_registered_as_neutral() -> None:
    d = registry()["parity_score"]
    assert d.get("neutral") is True
    assert d["optionalInputs"] == ["smoke"]
    assert d["produces"] == {"report": "parity_report"}
    props = d["params"]["properties"]
    assert props["min_identical_share"]["x-cadence"]["defaultRef"] == "deploy.parity_min_identical_share"
    assert ParityScoreParams().min_identical_share == 0.97


def test_parity_passes_on_identical_tokens(tmp_path: Path) -> None:
    rows = [{"text": r["text"], "tokens": [i, i + 1]} for i, r in enumerate(REFS)]
    doc = parity(tmp_path, rows, rows, smoke=True)
    assert doc["schema"] == "cadence.parity/1"
    assert doc["compared"] == "tokens"
    assert doc["identicalShare"] == 1.0
    assert doc["werDelta"] == 0.0
    assert doc["verdict"] == "passed"
    assert [i["expected"] for i in doc["smoke"]["items"]] == ["0 1", "1 2", "2 3"]
    assert (tmp_path / "report" / "smoke" / "01.json").is_file()


def test_parity_fails_on_identical_share_and_disagreement(tmp_path: Path) -> None:
    ref = [{"text": r["text"]} for r in REFS]
    srv = [{"text": "one two tree four"}, {"text": "five six seven eight"}, {"text": "nine ten"}, {"text": "a b c d"}]
    doc = parity(tmp_path, ref, srv)
    assert doc["compared"] == "text"
    assert doc["identical"] == 3
    assert doc["disagreement"] == pytest.approx(1 / 14)
    assert doc["werDelta"] == pytest.approx(1 / 14)
    assert doc["verdict"] == "failed"
    assert len(doc["reasons"]) == 3
    assert doc["differing"][0]["served"] == "one two tree four"


def test_parity_refuses_a_partial_served_decode(tmp_path: Path) -> None:
    hashes = dataset(tmp_path / "data", REFS)
    (tmp_path / "norm.json").write_text(json.dumps({**BASIC, "versionId": "ver_n"}), encoding="utf-8")
    full = [{"text": r["text"]} for r in REFS]
    inputs = {
        "reference": hyps(tmp_path / "ref.jsonl", hashes, full),
        "served": hyps(tmp_path / "srv.jsonl", hashes[:2], full[:2]),
        "data": tmp_path / "data",
        "normalizer": tmp_path / "norm.json",
    }
    with pytest.raises(StepInputError, match="served decode lacks 2"):
        ParityScoreStep().run(
            ParityScoreParams(), inputs, {"report": tmp_path / "r"}, StepContext(lambda e: None, work_dir=tmp_path)
        )


def timings(path: Path, streams: int, lat: list[float], foreign: float = 0.0, errors: int = 0) -> Path:
    rows: list[dict[str, Any]] = [
        {
            "schema": "cadence.serving-timings/1",
            "profile": "80ms",
            "chunkMs": 80,
            "concurrency": streams,
            "warmupMs": 100,
            "server": {"kind": "x", "version": "1"},
            "cardClass": "c",
        }
    ]
    rows.append(
        {"type": "chunk", "stream": 0, "audio": "b3:a", "index": 0, "availableMs": 50, "doneMs": 5000, "last": False}
    )
    for i, v in enumerate(lat):
        rows.append(
            {
                "type": "chunk",
                "stream": 0,
                "audio": "b3:a",
                "index": i + 1,
                "availableMs": 200.0 + i * 80,
                "doneMs": 200.0 + i * 80 + v,
                "last": i == len(lat) - 1,
            }
        )
    rows.append({"type": "telemetry", "atMs": 0, "utilizationPct": 50, "memoryUsedMb": 7000, "foreignUtilPct": foreign})
    rows += [{"type": "error", "stream": 1, "message": "x"}] * errors
    path.write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    return path


def test_percentiles_interpolate() -> None:
    assert percentiles([]) == {"p50": None, "p95": None, "p99": None}
    assert percentiles([10.0, 20.0, 30.0])["p50"] == 20.0
    assert percentiles(list(map(float, range(101))))["p95"] == 95.0


def test_benchmark_streams_per_card_and_verdict(tmp_path: Path) -> None:
    inputs = {
        "timings.0": timings(tmp_path / "1.jsonl", 1, [10.0] * 20),
        "timings.1": timings(tmp_path / "32.jsonl", 32, [20.0] * 20),
        "timings.2": timings(tmp_path / "64.jsonl", 64, [150.0] * 20),
        "timings.3": timings(tmp_path / "128.jsonl", 128, [50.0] * 20),
    }
    out = tmp_path / "report.json"
    BenchmarkScoreStep().run(
        BenchmarkScoreParams(), inputs, {"report": out}, StepContext(lambda e: None, work_dir=tmp_path)
    )
    doc = json.loads(out.read_text(encoding="utf-8"))
    assert doc["schema"] == "cadence.benchmark/1"
    assert [lv["streams"] for lv in doc["levels"]] == [1, 32, 64, 128]
    assert doc["levels"][0]["chunks"] == 20  # the warm-up chunk is not counted
    assert doc["levels"][1]["chunkLatencyMs"]["p95"] == 20.0
    assert doc["levels"][1]["rtf"] == 0.25
    assert doc["maxStreamsWithinBudget"] == 32  # 64 fails; 128 within the budget does not count after it
    assert doc["verdict"] == "passed"
    assert doc["levels"][0]["servingMemoryMb"] == 7000


def test_benchmark_contended_and_errors() -> None:
    def lv(streams: int, p95: float, contended: bool = False, errors: int = 0) -> dict[str, Any]:
        return {
            "streams": streams,
            "chunkLatencyMs": {"p95": p95},
            "withinBudget": p95 <= 100 and errors == 0,
            "contended": contended,
            "errors": errors,
            "foreignUtilPct": 40 if contended else 0,
        }

    assert judge([lv(32, 20, contended=True)], 32)[1] == "inconclusive"
    assert judge([lv(32, 20, errors=2)], 32)[1] == "failed"
    assert judge([lv(16, 20)], 32)[1] == "inconclusive"
    assert judge([lv(1, 10), lv(32, 120)], 32)[:2] == (1, "failed")
