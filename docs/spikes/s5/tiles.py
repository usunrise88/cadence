"""Spike S5: peaks and the uint8 dB spectrogram tile pyramid, in the shape of the future worker step (throwaway).

Step shape: inputs {audio}, params {window_ms, hop_ms, n_fft, tile_frames, db_floor, db_step}, output a directory
artifact `spectrogram_tiles`:

  manifest.json          schema, sample rate (served: 16 kHz), channels, bins kept, hop, levels, encoding, peak dB
  c<ch>/l<L>/<i>.u8      one tile: uint8 dB, [bins x tile_frames] row-major (bin 0 first), R8-texture ready
  peaks.i8               10 ms min/max per channel, int8, [frames x channels x 2]

Encoding: u8 = clamp(round((dB - db_floor) / db_step)), dB re full scale (a full-scale sine's bin reads 0 dB).
Level 0 is the 10 ms frame grid; level L max-pools 2^L frames over time. Bins stop at the origin's Nyquist
(an 8 kHz call keeps 129 of 257 bins).
"""

from __future__ import annotations

import json
import sys
import time
from pathlib import Path

import librosa
import numpy as np
import soundfile as sf

SR = 16000
WIN, HOP, NFFT = 400, 160, 512
TILE = 512
FLOOR, STEP = -120.0, 0.5


def stft_db(y: np.ndarray) -> np.ndarray:
    """dB magnitudes [frames, 257]: centred frames, zero padding, periodic Hann 400 zero-padded to 512."""
    win = np.zeros(NFFT, np.float64)
    hann = 0.5 - 0.5 * np.cos(2 * np.pi * np.arange(WIN) / WIN)
    off = (NFFT - WIN) // 2
    win[off : off + WIN] = hann
    pad = np.pad(y.astype(np.float64), (NFFT // 2, NFFT // 2))
    frames = 1 + len(y) // HOP
    out = np.empty((frames, NFFT // 2 + 1), np.float32)
    scale = 2.0 / hann.sum()
    for a in range(0, frames, 8192):  # chunked to bound memory
        b = min(frames, a + 8192)
        idx = np.arange(a, b)[:, None] * HOP + np.arange(NFFT)[None, :]
        mag = np.abs(np.fft.rfft(pad[idx] * win, axis=1)) * scale
        out[a:b] = 20 * np.log10(np.maximum(mag, 1e-7))
    return out


def encode(db: np.ndarray) -> np.ndarray:
    return np.clip(np.round((db - FLOOR) / STEP), 0, 255).astype(np.uint8)


def run(audio: Path, out: Path) -> dict:
    t0 = time.perf_counter()
    y, sr = sf.read(audio, dtype="float32", always_2d=True)
    origin_nyquist = sr / 2
    bins = int(np.floor(min(origin_nyquist, SR / 2) / (SR / NFFT))) + 1
    channels = y.shape[1]
    manifest: dict = {
        "schema": "cadence.spectrogram-tiles/0",
        "sampleRate": SR,
        "originSampleRate": sr,
        "channels": channels,
        "window": "hann",
        "windowSamples": WIN,
        "hopSamples": HOP,
        "nFft": NFFT,
        "bins": bins,
        "binHz": SR / NFFT,
        "tileFrames": TILE,
        "encoding": {"floorDb": FLOOR, "stepDb": STEP},
        "levels": [],
        "peakDb": [],
        "bytes": 0,
    }
    peaks = []
    stft_s = 0.0
    for ch in range(channels):
        x = y[:, ch]
        # Peaks: 10 ms min/max at the original rate.
        hop0 = sr // 100
        n = len(x) // hop0
        blk = x[: n * hop0].reshape(n, hop0)
        peaks.append(np.stack([blk.min(1), blk.max(1)], 1))
        x16 = librosa.resample(x, orig_sr=sr, target_sr=SR) if sr != SR else x
        a = time.perf_counter()
        db = stft_db(x16)[:, :bins]
        stft_s += time.perf_counter() - a
        manifest["peakDb"].append(float(db.max()))
        q = encode(db)  # [frames, bins]
        level = 0
        while True:
            frames = q.shape[0]
            tiles = (frames + TILE - 1) // TILE
            d = out / f"c{ch}" / f"l{level}"
            d.mkdir(parents=True, exist_ok=True)
            for i in range(tiles):
                t = np.zeros((TILE, bins), np.uint8)
                part = q[i * TILE : (i + 1) * TILE]
                t[: len(part)] = part
                buf = np.ascontiguousarray(t.T).tobytes()  # [bins x TILE]
                (d / f"{i}.u8").write_bytes(buf)
                manifest["bytes"] += len(buf)
            if ch == 0:
                manifest["levels"].append({"level": level, "hopS": HOP / SR * 2**level, "frames": frames, "tiles": tiles})
            if frames <= TILE:
                break
            if frames % 2:
                q = np.vstack([q, q[-1:]])
            q = np.maximum(q[0::2], q[1::2])  # max-pool over time
            level += 1
    pk = np.stack(peaks, 1)  # [frames, ch, 2]
    pk8 = np.clip(np.round(pk * 127), -127, 127).astype(np.int8)
    (out / "peaks.i8").write_bytes(pk8.tobytes())
    manifest["peaks"] = {"hopS": 0.01, "frames": int(pk.shape[0]), "bytes": int(pk8.nbytes), "layout": "frames x channels x [min,max], int8 /127"}
    manifest["duration"] = len(y) / sr
    manifest["computeSeconds"] = round(time.perf_counter() - t0, 2)
    manifest["stftSeconds"] = round(stft_s, 2)
    (out / "manifest.json").write_text(json.dumps(manifest, indent=1))
    return manifest


if __name__ == "__main__":
    m = run(Path(sys.argv[1]), Path(sys.argv[2]))
    m.pop("levels")
    print(json.dumps(m))
