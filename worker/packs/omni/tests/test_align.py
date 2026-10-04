"""align_reference without a card: the aligner glue over a fake CTC model whose emissions spell a planned path, the
step's bookkeeping (coverage, reasons, header and meta) with the model loader replaced, and the published schema."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_omni import omniasr
from cadence_omni.aligner import UnalignedError, align_utterance
from cadence_omni.steps.align import AlignReferenceParams, AlignReferenceStep, covers
from cadence_worker import ctc_align
from cadence_worker import reference_alignment as ra
from cadence_worker.registry import registry
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[2] / "nemo" / "cadence_nemo" / "fixtures"
SAMPLES_PER_FRAME = 320  # 20 ms at 16 kHz
ALIGNER: dict[str, Any] = {
    "versionId": "ver_omni",
    "name": "auxiliary/omniasr-ctc-1b",
    "version": "2026-10-03.000000000000",
    "payload": {
        "roles": ["align"],
        "licence": "Apache-2.0",
        "outputsCommercialUse": True,
        "languages": ["he", "sr", "hr"],
        "hfRepo": "facebook/omniASR-CTC-1B",
        "revision": "8c22e3ffdaa4aab6431b128b84b991a7d9c2515c",
    },
}


class Fake:
    """Characters a…z are ids 1…26, the word boundary 27, the blank 0; the emissions favour ``path`` (one id per
    frame) with probability 0.9."""

    blank = 0
    separator: int | None = 27

    def __init__(self, path: list[int] | None = None) -> None:
        self.path = path

    def encode(self, text: str) -> list[int]:
        return [ord(c) - 96 for c in text if "a" <= c <= "z"]

    def emissions(self, path: list[int]) -> np.ndarray[Any, np.dtype[np.float32]]:
        p = np.full((len(path), 28), 0.1 / 27, dtype=np.float64)
        p[np.arange(len(path)), path] = 0.9
        return np.log(p).astype(np.float32)

    def log_probs(self, samples: np.ndarray[Any, np.dtype[np.float32]]) -> np.ndarray[Any, np.dtype[np.float32]]:
        frames = len(samples) // SAMPLES_PER_FRAME
        path = self.path or [0] * frames
        return self.emissions(path[:frames])


def test_align_utterance_times_words_on_the_planned_path() -> None:
    a, b, sep = 1, 2, 27
    #       frame: 0  1  2  3  4  5  6    7  8  9 10 11
    path = [0, 0, a, a, 0, b, sep, b, b, 0, a, 0]
    model = Fake(path)
    res = align_utterance(model, np.zeros(len(path) * SAMPLES_PER_FRAME, dtype=np.float32), "Ab, — ba!")
    assert res.frame_s == pytest.approx(0.02)
    assert res.skipped == [1]  # the dash spells nothing
    assert [(w["index"], w["word"], w["start"], w["end"]) for w in res.words] == [
        (0, "Ab,", 0.04, 0.12),
        (2, "ba!", 0.14, 0.22),
    ]
    assert all(w["score"] == pytest.approx(0.9) for w in res.words)


def test_align_utterance_reasons() -> None:
    model = Fake()
    with pytest.raises(UnalignedError, match="empty"):
        align_utterance(model, np.zeros(3200, dtype=np.float32), "  ")
    with pytest.raises(UnalignedError, match="vocabulary"):
        align_utterance(model, np.zeros(3200, dtype=np.float32), "… 123")
    with pytest.raises(UnalignedError, match="too short"):
        align_utterance(model, np.zeros(3 * SAMPLES_PER_FRAME, dtype=np.float32), "abcdef")


def test_covers() -> None:
    assert covers(["he", "sr"], "sr-Latn-RS")
    assert covers(["*"], "th-TH")
    assert not covers(["he", "sr"], "hr-HR")
    assert not covers(["he"], "")
    assert not covers(None, "he")


def _dataset(tmp_path: Path, locale: str) -> Path:
    params = DatasetImportParams(
        format="folder-csv",
        path=str(FIXTURES),
        source_name="fixtures",
        licence="CC-BY-4.0",
        locale=locale,
        split_rule="all-test",
    )
    data = tmp_path / "data"
    write_dataset(params, records(params), data)
    return data


def test_uncovered_language_never_loads_the_model(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    def boom(*_: Any) -> None:
        raise AssertionError("the model was loaded for a language the aligner does not cover")

    monkeypatch.setattr(omniasr, "snapshot", boom)
    data = _dataset(tmp_path, "th-TH")
    meta: dict[str, Any] = {}
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"aligner": ALIGNER})
    out = tmp_path / "alignment.jsonl"
    AlignReferenceStep().run(AlignReferenceParams(), {"data": data}, {"alignment": out}, ctx)
    meta = ctx.meta["alignment"]
    header, rows = ra.read(out)
    assert header["aligned"] == 0
    assert header["unaligned"] == 10
    assert header["method"] is None
    assert {r["reason"] for r in rows.values()} == {"auxiliary/omniasr-ctc-1b does not cover th-TH"}
    assert all("words" not in r for r in rows.values())
    assert meta["aligned"] == 0
    assert meta["reasons"] == ["auxiliary/omniasr-ctc-1b does not cover th-TH"]


def test_step_aligns_covered_utterances(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    class Loaded(Fake):
        def __init__(self, snap: Path, device: str, dtype: str) -> None:
            super().__init__()
            assert (device, dtype) == ("cpu", "bfloat16")

        def log_probs(self, samples: np.ndarray[Any, np.dtype[np.float32]]) -> np.ndarray[Any, np.dtype[np.float32]]:
            return np.log(np.full((len(samples) // SAMPLES_PER_FRAME, 28), 1 / 28)).astype(np.float32)

        def encode(self, text: str) -> list[int]:
            return [1 + (ord(c) % 26) for c in text]  # Hebrew letters → some ids

    monkeypatch.setattr(omniasr, "snapshot", lambda repo, rev: tmp_path)
    monkeypatch.setattr(omniasr, "device", lambda: "cpu")
    monkeypatch.setattr(omniasr, "OmniCtc", Loaded)
    monkeypatch.setattr(omniasr, "forced_aligner", lambda: ("ctc-viterbi", ctc_align.viterbi))
    data = _dataset(tmp_path, "he-IL")
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"aligner": ALIGNER})
    out = tmp_path / "alignment.jsonl"
    AlignReferenceStep().run(AlignReferenceParams(max_duration_s=3.5), {"data": data}, {"alignment": out}, ctx)
    header, rows = ra.read(out)
    assert header["method"] == "ctc-viterbi"
    assert header["aligner"]["versionId"] == "ver_omni"
    assert header["frameMs"] == pytest.approx(20, abs=0.5)
    longer = [r for r in rows.values() if not r["aligned"]]
    assert {r["reason"] for r in longer} <= {"longer than 3.5 s (packs.omni.align_max_duration_s)"}
    assert header["aligned"] == 10 - len(longer) > 0
    for r in rows.values():
        if r["aligned"]:
            assert [w["index"] for w in r["words"]] == list(range(len(r["text"].split())))
    assert ctx.final_metrics["aligned_utterances"] == header["aligned"]


def test_published_schema() -> None:
    reg = registry()
    d = reg["align_reference"]
    assert d["version"] == "1"
    assert AlignReferenceStep.runtime == "omni"
    assert d["consumes"] == {"data": "dataset"}
    assert d["produces"] == {"alignment": "alignment"}
    x = d["params"]["properties"]["aligner"]["x-cadence"]
    assert x["registryRef"] == {"kind": "auxiliary", "role": "align"}
    assert x["default"] == "auxiliary/omniasr-ctc-1b"
    assert json.dumps(d)  # serialisable as published
