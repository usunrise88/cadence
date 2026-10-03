"""spectrogram_tiles@1: the S5 tile format (docs/spikes/S5-audio-view.md; docs/help/steps/spectrogram-tiles.md)."""

from __future__ import annotations

import json
import math
import struct
from pathlib import Path
from typing import Any

import pytest

from cadence_worker.cas import hash_file
from cadence_worker.registry import registry
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.spectrogram_tiles import SpectrogramTilesParams, SpectrogramTilesStep, pool, stft_db

np = pytest.importorskip("numpy")


def wav(path: Path, channels: list[list[float]], rate: int) -> Path:
    n, nc = len(channels[0]), len(channels)
    data = b"".join(
        struct.pack("<h", max(-32768, min(32767, round(channels[c][i] * 32768)))) for i in range(n) for c in range(nc)
    )
    fmt = struct.pack("<IHHIIHH", 16, 1, nc, rate, rate * nc * 2, nc * 2, 16)
    hdr = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVEfmt " + fmt
    path.write_bytes(hdr + b"data" + struct.pack("<I", len(data)) + data)
    return path


def tone(seconds: float, rate: int, hz: float, amp: float) -> list[float]:
    return [amp * math.sin(2 * math.pi * hz * i / rate) for i in range(int(seconds * rate))]


def run(tmp: Path, src: Path) -> tuple[Path, list[dict[str, Any]]]:
    out = tmp / "tiles"
    events: list[dict[str, Any]] = []
    ctx = StepContext(events.append, work_dir=tmp)
    SpectrogramTilesStep().run(SpectrogramTilesParams(), {"audio": src}, {"tiles": out}, ctx)
    return out, events


def tile(out: Path, ch: int, level: int, i: int, bins: int) -> Any:
    raw = (out / f"c{ch}" / f"l{level}" / f"{i}.u8").read_bytes()
    assert len(raw) == bins * 512
    return np.frombuffer(raw, dtype=np.uint8).reshape(bins, 512)  # [bins x frames]


def test_registered_as_a_neutral_cpu_kind() -> None:
    d = registry()["spectrogram_tiles"]
    assert d["version"] == "1"
    assert d.get("neutral") is True
    assert d["consumes"] == {"audio": "audio"}
    assert d["produces"] == {"tiles": "spectrogram_tiles"}
    assert d["resources"]["gpu"] is False
    props = d["params"]["properties"]
    assert props["hop_ms"]["x-cadence"]["defaultRef"] == "views.audio.hop_ms"
    assert SpectrogramTilesParams().n_fft == 512
    assert SpectrogramTilesParams().tile_frames == 512


def test_call_at_8khz_pyramid(tmp_path: Path) -> None:
    # 12 s stereo at 8 kHz: a 1 kHz tone at half scale on the left, silence on the right.
    src = wav(tmp_path / "call.wav", [tone(12, 8000, 1000, 0.5), [0.0] * 96000], 8000)
    out, events = run(tmp_path, src)
    m = json.loads((out / "manifest.json").read_text())
    assert m["schema"] == "cadence.spectrogram-tiles/1"
    assert m["audio"] == hash_file(src)
    assert (m["sampleRate"], m["originSampleRate"], m["channels"]) == (16000, 8000, 2)
    assert (m["windowSamples"], m["hopSamples"], m["nFft"], m["tileFrames"]) == (400, 160, 512, 512)
    assert m["bins"] == 129  # 0-4 kHz of 257: the origin's Nyquist
    assert m["encoding"] == {"floorDb": -120.0, "stepDb": 0.5}
    # 1 + 192000 // 160 = 1201 frames → 3 tiles; 601 → 2; 301 → 1.
    assert [(lv["level"], lv["frames"], lv["tiles"]) for lv in m["levels"]] == [(0, 1201, 3), (1, 601, 2), (2, 301, 1)]
    assert [lv["hopS"] for lv in m["levels"]] == [0.01, 0.02, 0.04]
    assert abs(m["peakDb"][0] - 20 * math.log10(0.5)) < 0.5  # a half-scale sine's bin reads -6 dB
    assert m["peakDb"][1] == -120.0 or m["peakDb"][1] < -100
    assert abs(m["durationS"] - 12) < 1e-6
    t = tile(out, 0, 0, 1, 129)
    col = t[:, 100]  # one frame in the middle
    assert int(np.argmax(col)) == 32  # 1000 Hz / 31.25 Hz
    assert abs((int(col[32]) * 0.5 - 120) - (-6.02)) <= 0.75
    assert int(tile(out, 1, 0, 1, 129).max()) == 0  # silence sits at the floor
    # Level 1 is the max over pairs of level-0 frames.
    l0 = np.hstack([tile(out, 0, 0, i, 129) for i in range(3)])[:, :1201]
    l1 = np.hstack([tile(out, 0, 1, i, 129) for i in range(2)])[:, :601]
    padded = np.hstack([l0, l0[:, -1:]])
    assert np.array_equal(l1, np.maximum(padded[:, 0::2], padded[:, 1::2]))
    # The last tile of a level is zero-padded past its frames.
    assert int(tile(out, 0, 0, 2, 129)[:, 1201 - 1024 :].max()) == 0
    meta = next(e for e in events if e["e"] == "meta")
    assert meta["output"] == "tiles"
    assert meta["meta"]["audio"] == m["audio"]
    assert meta["meta"]["levels"] == 3


def test_clip_at_16khz_keeps_all_bins(tmp_path: Path) -> None:
    src = wav(tmp_path / "clip.wav", [tone(1, 16000, 6000, 0.25)], 16000)
    out, _ = run(tmp_path, src)
    m = json.loads((out / "manifest.json").read_text())
    assert m["bins"] == 257
    assert m["channels"] == 1
    assert [(lv["frames"], lv["tiles"]) for lv in m["levels"]] == [(101, 1)]
    assert int(np.argmax(tile(out, 0, 0, 0, 257)[:, 50])) == 192  # 6000 / 31.25


def test_stft_matches_a_direct_dft() -> None:
    rng = np.random.default_rng(1)
    y = rng.standard_normal(1600) * 0.1
    db = stft_db(np, y, 400, 160, 512)
    assert db.shape == (11, 257)
    # Frame 5 by hand: centred at sample 800, periodic Hann of 400 in the middle of 512.
    win = np.zeros(512)
    hann = 0.5 - 0.5 * np.cos(2 * np.pi * np.arange(400) / 400)
    win[56:456] = hann
    seg = y[800 - 256 : 800 + 256] * win
    mag = np.abs(np.fft.rfft(seg)) * 2 / hann.sum()
    assert np.allclose(db[5], 20 * np.log10(np.maximum(mag, 1e-7)), atol=1e-3)


def test_pool_pads_odd_levels() -> None:
    q = np.array([[1, 5], [3, 2], [4, 4]], dtype=np.uint8)
    assert pool(np, q).tolist() == [[3, 5], [4, 4]]


def test_refuses_a_missing_or_foreign_input(tmp_path: Path) -> None:
    step = SpectrogramTilesStep()
    ctx = StepContext(lambda e: None, work_dir=tmp_path)
    with pytest.raises(StepInputError):
        step.run(SpectrogramTilesParams(), {}, {"tiles": tmp_path / "t"}, ctx)
    bad = tmp_path / "x.wav"
    bad.write_bytes(b"not audio at all")
    with pytest.raises(StepInputError):
        step.run(SpectrogramTilesParams(), {"audio": bad}, {"tiles": tmp_path / "t"}, ctx)
