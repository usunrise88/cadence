"""whisper_transcribe@1 without transformers: the pure parts of cadence_nemo.whisper, and the step's glue with the
model replaced by a fake (the real decode is the GPU test in test_whisper_gpu.py). Language identification on its own
is lid_classify@2 in the omni pack (VoxLingua107); Whisper's own detection is the member's ``detectedLanguage``."""

from __future__ import annotations

import json
from collections.abc import Sequence
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_nemo import whisper
from cadence_nemo.steps import whisper_member
from cadence_worker import audio as audio_io
from cadence_worker.cas import hash_file
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext


def test_language_tokens_and_ranking() -> None:
    assert whisper.language_code("<|he|>") == "he"
    top = whisper.rank_languages([0.1, 0.7, 0.2], ["en", "sr", "hr"], 2)
    assert top == [("sr", 0.7), ("hr", 0.2)]
    assert whisper.softmax(np.array([[0.0, 0.0]])).tolist() == [[0.5, 0.5]]


def test_batches_keep_long_audio_alone() -> None:
    assert whisper.batches([1, 2, 31, 3, 4, 5], 2) == [[0, 1], [2], [3, 4], [5]]


def test_whisper_language() -> None:
    assert whisper.whisper_language("he-IL", ["en", "he"]) == "he"
    with pytest.raises(StepInputError, match="no language 'xx'"):
        whisper.whisper_language("xx-YY", ["en"])


class FakeWhisper:
    codes: Sequence[str] = ("en", "sr", "hr", "he", "ru")

    def __init__(self, repo: str, revision: str, device: str) -> None:
        self.loaded = (repo, revision, device)
        self.calls: list[tuple[int, str, int]] = []
        self.detects: list[int] = []

    def detect(self, clips: Sequence[Any], top_k: int = 3) -> list[whisper.Detection]:
        self.detects.append(len(clips))
        return [whisper.Detection("sr", 0.9, [("sr", 0.9), ("hr", 0.08), ("bs", 0.02)][:top_k]) for _ in clips]

    def transcribe(self, clips: Sequence[Any], language: str, num_beams: int) -> list[str]:
        self.calls.append((len(clips), language, num_beams))
        return ["Добар дан" for _ in clips]


def _dataset(root: Path) -> list[str]:
    (root / "audio").mkdir(parents=True)
    rows, hashes = [], []
    for i in range(3):
        x = 0.3 * np.sin(2 * np.pi * (300 + 100 * i) * np.arange(16000) / 16000)
        f = root / "audio" / f"{i}.wav"
        f.write_bytes(audio_io.wav_bytes(audio_io.Audio(x, 16000, 1)))
        rows.append({"audio": f"audio/{i}.wav", "duration": 1.0, "language": "sr-RS"})
        hashes.append(hash_file(f))
    (root / "dataset.json").write_text(json.dumps({"format": "cadence.dataset/1", "name": "fx"}), encoding="utf-8")
    (root / "manifest.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    return hashes


def _aux(name: str, engine: str) -> dict[str, Any]:
    return {
        "versionId": "ver_w",
        "name": name,
        "version": "2026-10-03.abc",
        "payload": {
            "roles": ["pseudolabel", "lid"],
            "licence": "Apache-2.0",
            "outputsCommercialUse": True,
            "languages": ["*"],
            "hfRepo": "openai/whisper-large-v3",
            "revision": "06f233fe06e710322aca913c1bc4249a0d71fce1",
            "engine": engine,
        },
    }


@pytest.fixture
def fake_model(monkeypatch: pytest.MonkeyPatch) -> list[FakeWhisper]:
    made: list[FakeWhisper] = []

    def make(repo: str, revision: str, device: str) -> FakeWhisper:
        made.append(FakeWhisper(repo, revision, device))
        return made[-1]

    monkeypatch.setattr(whisper, "Whisper", make)
    monkeypatch.setattr(whisper_member, "card", lambda ctx, reserve, allow_cpu=False: {"device": "cpu"})
    monkeypatch.setattr(whisper_member, "device", lambda: "cpu")
    return made


def test_whisper_member_writes_hypotheses(tmp_path: Path, fake_model: list[FakeWhisper]) -> None:
    hashes = _dataset(tmp_path / "data")
    ctx = StepContext(
        lambda e: None,
        work_dir=tmp_path,
        auxiliaries={"auxiliary": _aux("auxiliary/whisper-large-v3", "transformers-whisper")},
    )
    out = tmp_path / "hyp.jsonl"
    params = whisper_member.WhisperParams(batch_size=2, transliterate="sr-Cyrl-Latn")
    whisper_member.WhisperTranscribeStep().run(params, {"data": tmp_path / "data"}, {"hypotheses": out}, ctx)
    rows = [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()]
    assert [r["audio"] for r in rows] == hashes
    assert {r["text"] for r in rows} == {"Dobar dan"}
    assert {r["member"] for r in rows} == {"whisper-large-v3"}
    assert {r["detectedLanguage"] for r in rows} == {"sr"}
    assert rows[0]["model"]["revision"] == "06f233fe06e710322aca913c1bc4249a0d71fce1"
    assert fake_model[0].calls == [(2, "sr", 1), (1, "sr", 1)]


def test_whisper_member_needs_weights(tmp_path: Path, fake_model: list[FakeWhisper]) -> None:
    _dataset(tmp_path / "data")
    aux = _aux("auxiliary/oasis", "")
    aux["payload"] = {**aux["payload"], "hfRepo": "", "revision": ""}
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": aux})
    with pytest.raises(StepInputError, match="names no weights"):
        whisper_member.WhisperTranscribeStep().run(
            whisper_member.WhisperParams(), {"data": tmp_path / "data"}, {"hypotheses": tmp_path / "h"}, ctx
        )


def test_a_retry_at_a_smaller_batch_scale_shrinks_the_batches(tmp_path: Path, fake_model: list[FakeWhisper]) -> None:
    # An OOM retry runs at batch_scale 0.75, a manual one at what the person asks: the members' batches follow it.
    _dataset(tmp_path / "data")
    aux = {"auxiliary": _aux("auxiliary/whisper-large-v3", "transformers-whisper")}
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries=aux, batch_scale=0.5)
    params = whisper_member.WhisperParams(batch_size=2, transliterate="")
    whisper_member.WhisperTranscribeStep().run(
        params, {"data": tmp_path / "data"}, {"hypotheses": tmp_path / "hyp"}, ctx
    )
    assert [n for n, _, _ in fake_model[-1].calls] == [1, 1, 1]
    assert fake_model[-1].detects == [1, 1, 1]
