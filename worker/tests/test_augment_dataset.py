"""augment_dataset@1: deterministic augmentation of a golden dataset (phase 3 stream R)."""

from __future__ import annotations

import json
import math
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_worker import audio
from cadence_worker import augment as aug
from cadence_worker.cas import hash_file
from cadence_worker.registry import registry
from cadence_worker.steps.augment_dataset import AugmentDatasetParams, AugmentDatasetStep
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

RATE = 16000


def tone(seconds: float, freq: float, amp: float = 0.3) -> np.ndarray:
    t = np.arange(int(seconds * RATE)) / RATE
    return np.asarray(amp * np.sin(2 * math.pi * freq * t), dtype=np.float64)


def wav(path: Path, x: np.ndarray) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(audio.wav_bytes(audio.Audio(samples=x, sample_rate=RATE, channels=1)))


def dataset(root: Path, clips: list[np.ndarray], purpose: str = "") -> None:
    root.mkdir(parents=True)
    header: dict[str, Any] = {"format": "cadence.dataset/1", "name": "golden", "hours": 0.0, "evalOnly": True}
    if purpose:
        header["purpose"] = purpose
    (root / "dataset.json").write_text(json.dumps(header), encoding="utf-8")
    with (root / "manifest.jsonl").open("w", encoding="utf-8") as f:
        for i, x in enumerate(clips):
            rel = f"audio/{i:02d}.wav"
            wav(root / rel, x)
            row = {"audio": rel, "duration": x.size / RATE, "sampleRate": RATE, "language": "he-IL", "text": f"utt {i}"}
            row["speaker"] = f"s{i % 2}"
            f.write(json.dumps(row) + "\n")


def profile(**transforms: Any) -> dict[str, Any]:
    return {"format": aug.FORMAT, "name": "telephony", "seed": 7, "hash": "sha256:p", "transforms": transforms}


TELEPHONY = {
    "codec": {"probability": 1.0, "codecs": ["g711-ulaw", "g711-alaw", "gsm-fr", "opus"]},
    "band_limit": {"probability": 1.0, "cutoff_hz": 3400},
    "level": {"probability": 1.0, "gain_db": [-6, -6]},
    "speed": {"probability": 0.5, "factor": [0.9, 1.1]},
}


def run(tmp: Path, prof: dict[str, Any], clips: list[np.ndarray], noise: list[np.ndarray] | None = None) -> Path:
    dataset(tmp / "data", clips)
    (tmp / "profile.json").write_text(json.dumps(prof), encoding="utf-8")
    inputs = {"data": tmp / "data", "profile": tmp / "profile.json"}
    if noise is not None:
        dataset(tmp / "noise", noise, purpose="noise")
        inputs["noise"] = tmp / "noise"
    out = tmp / "out"
    AugmentDatasetStep().run(AugmentDatasetParams(), inputs, {"data": out}, StepContext(lambda e: None, work_dir=tmp))
    return out


def rows(out: Path) -> list[dict[str, Any]]:
    return [json.loads(x) for x in (out / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]


def samples(out: Path, row: dict[str, Any]) -> np.ndarray:
    return np.asarray(audio.read(out / row["audio"]).samples, dtype=np.float64)


def test_registered_as_a_neutral_cpu_kind() -> None:
    d = registry()["augment_dataset"]
    assert d["version"] == "1"
    assert d.get("neutral") is True
    assert d["consumes"] == {"data": "dataset", "profile": "augment_profile", "noise": "dataset"}
    assert d["optionalInputs"] == ["noise"]
    assert d["produces"] == {"data": "dataset"}
    assert d["resources"]["gpu"] is False


def test_same_profile_and_seed_give_the_same_audio(tmp_path: Path) -> None:
    clips = [tone(1.0, 440), tone(0.7, 1000), tone(1.2, 5000)]
    a = run(tmp_path / "a", profile(**TELEPHONY), clips)
    b = run(tmp_path / "b", profile(**TELEPHONY), clips)
    assert [r["audio"] for r in rows(a)] == [r["audio"] for r in rows(b)]
    other = profile(**TELEPHONY)
    other["seed"] = 8
    c = run(tmp_path / "c", other, clips)
    assert [r["audio"] for r in rows(a)] != [r["audio"] for r in rows(c)]


def test_rows_keep_text_and_trace_back(tmp_path: Path) -> None:
    clips = [tone(1.0, 440), tone(0.5, 800)]
    out = run(tmp_path, profile(**TELEPHONY), clips)
    header = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    assert header["purpose"] == "augmented"
    assert header["augmentation"]["hash"] == "sha256:p"
    assert header["augmentation"]["unavailable"] == ["gsm-fr", "opus"]
    assert header["augmentation"]["applied"]["codec"] == 2
    for i, r in enumerate(rows(out)):
        assert r["text"] == f"utt {i}"
        assert r["speaker"] == f"s{i % 2}"
        assert r["augmentedFrom"] == hash_file(tmp_path / "data" / f"audio/{i:02d}.wav")
        assert any(s.startswith("codec:g711") for s in r["augment"])
        assert hash_file(out / r["audio"]) != r["augmentedFrom"]


def test_band_limit_removes_high_frequencies(tmp_path: Path) -> None:
    out = run(tmp_path, profile(band_limit={"probability": 1.0, "cutoff_hz": 3400}), [tone(1.0, 6000), tone(1.0, 500)])
    high, low = (samples(out, r) for r in rows(out))
    assert np.sqrt(np.mean(high**2)) < 0.01  # a 6 kHz tone does not pass a telephone channel
    assert np.sqrt(np.mean(low**2)) > 0.15


def test_speed_changes_the_duration(tmp_path: Path) -> None:
    out = run(tmp_path, profile(speed={"probability": 1.0, "factor": [1.1, 1.1]}), [tone(1.1, 300)])
    (r,) = rows(out)
    assert r["duration"] == pytest.approx(1.0, abs=0.001)


def test_level_and_noise(tmp_path: Path) -> None:
    prof = profile(noise={"probability": 1.0, "snr_db": [10, 10], "bank": "ver_n"})
    rng = np.random.default_rng(0)
    out = run(tmp_path, prof, [tone(1.0, 300)], noise=[0.1 * rng.standard_normal(RATE // 2)])
    (r,) = rows(out)
    x = samples(out, r)
    clean = tone(1.0, 300)
    snr = 10 * np.log10(np.mean(clean**2) / np.mean((x - clean) ** 2))
    assert snr == pytest.approx(10, abs=0.5)
    assert r["augment"] == ["noise:10.0dB"]


def test_noise_without_a_bank_is_refused(tmp_path: Path) -> None:
    with pytest.raises(StepInputError, match="noise bank"):
        run(tmp_path, profile(noise={"probability": 0.5, "snr_db": [0, 20]}), [tone(0.5, 300)])


def test_unknown_keys_are_refused(tmp_path: Path) -> None:
    with pytest.raises(StepInputError, match="augmentation profile"):
        run(tmp_path, profile(reverb={"probability": 1.0}), [tone(0.5, 300)])


def test_g711_round_trip_is_close() -> None:
    x = tone(0.1, 440) * 2
    for f in (aug.mulaw, aug.alaw):
        y = f(x)
        assert np.max(np.abs(y - x)) < 0.03
