"""spectrogram_tiles — the server tile pyramid of an audio's acoustic spectrogram, for audio too long for the browser
(R52; docs/spikes/S5-audio-view.md "Proposed spec change"). Runtime-neutral, CPU. Help:
docs/help/steps/spectrogram-tiles.md.

Input ``audio`` (an audio file artifact: the canonical WAV of an utterance, or any format ``soundfile`` reads).
Output ``tiles``, a directory artifact:

  manifest.json      schema cadence.spectrogram-tiles/1: sample rate served (16 kHz), the origin's rate, channels,
                     window, hop, FFT size, bins kept, tile frames, encoding, levels, peak dB per channel, duration
  c<ch>/l<L>/<i>.u8  one tile: uint8 dB, [bins x tile_frames] row-major (bin 0 first), R8-texture ready

Encoding: u8 = clamp(round((dB + 120) / 0.5)), dB re full scale (a full-scale sine's bin reads 0 dB). Level 0 is the
hop grid (10 ms); level L max-pools 2^L frames over time. Bins stop at the origin's Nyquist (audio of 8 kHz origin
keeps 129 of 257). The STFT matches the browser's (``web/src/shell/audio``): a periodic Hann window centred in the
FFT frame, centred frames with zero padding (librosa ``center=True``, constant pad).
"""

from __future__ import annotations

import importlib
import json
import math
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import audio
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

SCHEMA = "cadence.spectrogram-tiles/1"
SERVE_RATE = 16000
FLOOR_DB = -120.0
STEP_DB = 0.5
CHUNK_FRAMES = 8192  # STFT frames computed at once (bounds memory on long calls)


class SpectrogramTilesParams(BaseModel):
    window_ms: float = cadence_field(default_ref="views.audio.window_ms")
    hop_ms: float = cadence_field(default_ref="views.audio.hop_ms")
    n_fft: int = cadence_field(default_ref="views.audio.n_fft")
    tile_frames: int = cadence_field(default_ref="views.audio.tile_frames")


def _numpy() -> Any:
    try:
        return importlib.import_module("numpy")
    except ImportError as e:  # every runtime image has NumPy; a bare worker may not
        raise StepInputError("spectrogram_tiles needs NumPy in the runtime image") from e


def stft_db(np: Any, y: Any, win: int, hop: int, n_fft: int) -> Any:
    """dB magnitudes [frames, n_fft/2 + 1] of mono float64 samples."""
    if win > n_fft:
        raise StepInputError(f"a {win}-sample window does not fit a {n_fft}-point FFT")
    n = np.arange(win)
    hann = 0.5 - 0.5 * np.cos(2 * np.pi * n / win)  # periodic Hann
    window = np.zeros(n_fft)
    off = (n_fft - win) // 2
    window[off : off + win] = hann
    scale = 2.0 / hann.sum()
    padded = np.pad(np.asarray(y, dtype=np.float64), (n_fft // 2, n_fft // 2))
    frames = 1 + len(y) // hop
    out = np.empty((frames, n_fft // 2 + 1), dtype=np.float32)
    for a in range(0, frames, CHUNK_FRAMES):
        b = min(frames, a + CHUNK_FRAMES)
        idx = np.arange(a, b)[:, None] * hop + np.arange(n_fft)[None, :]
        mag = np.abs(np.fft.rfft(padded[idx] * window, axis=1)) * scale
        out[a:b] = 20 * np.log10(np.maximum(mag, 1e-7))
    return out


def encode(np: Any, db: Any) -> Any:
    """uint8 dB: 0 is -120 dB, each step 0.5 dB."""
    return np.clip(np.round((db - FLOOR_DB) / STEP_DB), 0, 255).astype(np.uint8)


def pool(np: Any, q: Any) -> Any:
    """The next level: max over pairs of frames (an odd last frame pairs with itself)."""
    if q.shape[0] % 2:
        q = np.vstack([q, q[-1:]])
    return np.maximum(q[0::2], q[1::2])


def write_pyramid(np: Any, q: Any, out: Path, ch: int, tile: int) -> list[dict[str, Any]]:
    """Writes channel ch's levels; returns the level table (frames and tiles per level)."""
    levels: list[dict[str, Any]] = []
    level = 0
    while True:
        frames = int(q.shape[0])
        tiles = max(1, math.ceil(frames / tile))
        d = out / f"c{ch}" / f"l{level}"
        d.mkdir(parents=True, exist_ok=True)
        for i in range(tiles):
            t = np.zeros((tile, q.shape[1]), dtype=np.uint8)
            part = q[i * tile : (i + 1) * tile]
            t[: len(part)] = part
            (d / f"{i}.u8").write_bytes(np.ascontiguousarray(t.T).tobytes())  # [bins x tile]
        levels.append({"level": level, "frames": frames, "tiles": tiles})
        if frames <= tile:
            return levels
        q = pool(np, q)
        level += 1


class SpectrogramTilesStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"audio": "audio"}
    produces: ClassVar[Mapping[str, str]] = {"tiles": "spectrogram_tiles"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = SpectrogramTilesParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = SpectrogramTilesParams.model_validate(params.model_dump())
        src = inputs.get("audio")
        if src is None or not src.is_file():
            raise StepInputError("spectrogram_tiles needs an audio input (an audio file artifact)")
        np = _numpy()
        try:
            a = audio.read(src)
        except audio.AudioError as e:
            raise StepInputError(str(e)) from e
        win = round(p.window_ms * SERVE_RATE / 1000)
        hop = round(p.hop_ms * SERVE_RATE / 1000)
        if hop < 1 or win < 2:
            raise StepInputError(f"window {p.window_ms} ms and hop {p.hop_ms} ms are too short")
        bin_hz = SERVE_RATE / p.n_fft
        nyquist = min(a.sample_rate / 2, SERVE_RATE / 2)
        bins = min(p.n_fft // 2 + 1, math.floor(nyquist / bin_hz) + 1)
        samples = np.asarray(a.samples, dtype=np.float64)
        out = outputs["tiles"]
        out.mkdir(parents=True, exist_ok=True)
        levels: list[dict[str, Any]] = []
        peak_db: list[float] = []
        for ch in range(a.channels):
            mono = audio.Audio(samples=samples[ch :: a.channels], sample_rate=a.sample_rate, channels=1)
            y = audio.resample(mono, SERVE_RATE).samples
            db = stft_db(np, y, win, hop, p.n_fft)[:, :bins]
            peak_db.append(round(float(db.max()), 2) if db.size else FLOOR_DB)
            table = write_pyramid(np, encode(np, db), out, ch, p.tile_frames)
            if ch == 0:
                levels = [{**lv, "hopS": round(hop / SERVE_RATE * 2 ** lv["level"], 6)} for lv in table]
            ctx.progress((ch + 1) / a.channels, f"channel {ch + 1}/{a.channels}")
        manifest = {
            "schema": SCHEMA,
            "audio": hash_file(src),
            "sampleRate": SERVE_RATE,
            "originSampleRate": a.sample_rate,
            "channels": a.channels,
            "window": "hann",
            "windowSamples": win,
            "hopSamples": hop,
            "nFft": p.n_fft,
            "bins": bins,
            "binHz": bin_hz,
            "tileFrames": p.tile_frames,
            "encoding": {"floorDb": FLOOR_DB, "stepDb": STEP_DB},
            "levels": levels,
            "peakDb": peak_db,
            "durationS": round(a.duration, 6),
        }
        (out / "manifest.json").write_text(json.dumps(manifest, indent=1) + "\n", encoding="utf-8")
        size = sum(f.stat().st_size for f in out.rglob("*.u8"))
        ctx.log("tiles written", levels=len(levels), channels=a.channels, bins=bins, bytes=size)
        ctx.set_meta(
            "tiles",
            {"audio": manifest["audio"], "levels": len(levels), "channels": a.channels, "bins": bins, "bytes": size},
        )
