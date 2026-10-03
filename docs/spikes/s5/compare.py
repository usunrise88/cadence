"""Spike S5: compare the browser STFT and the tile pyramid with librosa (throwaway).

Inputs (from run.sh): the fixtures dir, the browser's dump dir (Playwright writes stft-clip.f32 [frames x 257] dB and
stft-clip.u8), and the call's tile directory. Bins below the display range (more than 80 dB under the peak) are
excluded: they render as the colormap's floor whatever their value.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import librosa
import numpy as np
import soundfile as sf

WIN, HOP, NFFT, RANGE = 400, 160, 512, 80.0


def librosa_db(y: np.ndarray) -> np.ndarray:
    s = librosa.stft(y.astype(np.float64), n_fft=NFFT, hop_length=HOP, win_length=WIN, window="hann", center=True, pad_mode="constant")
    hann = librosa.filters.get_window("hann", WIN, fftbins=True)
    mag = np.abs(s).T * 2.0 / hann.sum()
    return (20 * np.log10(np.maximum(mag, 1e-7))).astype(np.float32)


def stats(ref: np.ndarray, got: np.ndarray, label: str) -> dict:
    n = min(len(ref), len(got))
    ref, got = ref[:n], got[:n]
    mask = ref >= ref.max() - RANGE
    d = np.abs(ref - got)[mask]
    return {
        "what": label,
        "frames": int(n),
        "binsCompared": int(mask.sum()),
        "maxAbsDb": round(float(d.max()), 3),
        "p999AbsDb": round(float(np.quantile(d, 0.999)), 3),
        "meanAbsDb": round(float(d.mean()), 4),
        "within0_5dB": round(float((d <= 0.5).mean()), 6),
    }


def main() -> None:
    fx, dumps, tiles = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
    out = []
    clip, _ = sf.read(fx / "clip.wav", dtype="float32")
    ref = librosa_db(clip)
    for name in ("stft-clip-wasm", "stft-clip-js"):
        f = dumps / f"{name}.f32"
        if f.exists():
            got = np.fromfile(f, "<f4").reshape(-1, NFFT // 2 + 1)
            out.append(stats(ref, got, f"browser float dB ({name.split('-')[-1]}) vs librosa, clip"))
    f = dumps / "stft-clip-u8.u8"
    if f.exists():
        got = np.fromfile(f, np.uint8).reshape(-1, NFFT // 2 + 1).astype(np.float32) * 0.5 - 120
        out.append(stats(ref, got, "browser uint8 dB (0.5 dB steps) vs librosa, clip"))
    # Tiles, level 0, channel 0: the 8 kHz call upsampled to 16 kHz, first 5 minutes.
    m = json.loads((tiles / "manifest.json").read_text())
    call, sr = sf.read(fx / "call.wav", dtype="float32", always_2d=True)
    y16 = librosa.resample(call[: 300 * sr, 0], orig_sr=sr, target_sr=16000)
    ref = librosa_db(y16)[:, : m["bins"]]
    frames = len(ref)
    cols = []
    for i in range((frames + m["tileFrames"] - 1) // m["tileFrames"]):
        t = np.fromfile(tiles / "c0" / "l0" / f"{i}.u8", np.uint8).reshape(m["bins"], m["tileFrames"]).T
        cols.append(t)
    got = np.vstack(cols)[:frames].astype(np.float32) * m["encoding"]["stepDb"] + m["encoding"]["floorDb"]
    # Tiles were computed over the whole call: the last frame of the 5-minute cut sees the next samples; drop it.
    out.append(stats(ref[:-2], got[:-2], "tile pyramid L0 (uint8) vs librosa, call ch0 first 5 min"))
    print(json.dumps(out, indent=1))
    (dumps / "compare.json").write_text(json.dumps(out, indent=1))


if __name__ == "__main__":
    main()
