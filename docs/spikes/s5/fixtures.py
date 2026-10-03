"""Spike S5 fixtures (throwaway): audio, word tracks, the synthetic `analysis` array and colormap LUTs.

Run by docs/spikes/s5/run.sh inside the uv image. Reads FLEURS clips read-only from /fleurs when mounted (the
stand's `imports/fleurs-sr-latn`), otherwise synthesises speech-like chirps. Writes into OUT (gitignored):

  clip.wav / clip.f32 / clip.webm      20 s, 16 kHz mono: speech + a 1 kHz tone + a 100 Hz-7 kHz chirp
  call.wav / call16.f32 / call.webm    30 min, 8 kHz stereo call (caller left, bot right), telephony band
  words-clip.json / words-call.json    two word tracks each (caller: FLEURS words; bot: Hebrew with digits/Latin)
  analysis/clip.f16, analysis/call/<i>.f16   80 log-mel features, float16, [80 x frames] row-major per chunk
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

import av
import librosa
import numpy as np
import soundfile as sf
from scipy.signal import butter, sosfilt

OUT = Path(sys.argv[1] if len(sys.argv) > 1 else "out")
FLEURS = Path(os.environ.get("FLEURS", "/fleurs"))
RNG = np.random.default_rng(5)
CALL_S = float(os.environ.get("S5_CALL_SECONDS", 1800))
WORDS_PER_TRACK = 5000
CHUNK = 4096  # analysis frames per chunk file

HEBREW = (
    "שלום תודה בבקשה כן לא אני רוצה לבדוק את החשבון שלי המספר הוא בסדר רגע אחד אפשר לעזור לך היום "
    "ההזמנה נשלחה אתמול הכתובת ברחוב הרצל בתל אביב מתי זה יגיע מחר בבוקר החבילה אצל השליח "
    "אני מבין תודה רבה להתראות יום טוב שיחה מוקלטת לצורך שיפור השירות"
).split()
MIXED = ["050-1234567", "3.5", "WhatsApp", "iPhone 15", "12:30", "₪249.90", "#4471", "Visa", "ת.ז. 123456782", "Wi-Fi"]


def fleurs_clips() -> list[tuple[np.ndarray, str]]:
    manifest = FLEURS / "manifest.jsonl"
    if not manifest.exists():
        return []
    rows = [json.loads(line) for line in manifest.read_text().splitlines()[:400]]
    out = []
    for r in rows:
        y, sr = sf.read(FLEURS / r["audio_filepath"], dtype="float32")
        if y.ndim > 1:
            y = y.mean(axis=1)
        if sr != 16000:
            y = librosa.resample(y, orig_sr=sr, target_sr=16000)
        out.append((y.astype(np.float32), r["text"]))
    return out


def chirp_speech(seconds: float) -> np.ndarray:
    """Speech-like stand-in: formant-ish chirps with a 4 Hz syllable envelope."""
    t = np.arange(int(seconds * 16000)) / 16000
    f0 = 120 + 30 * np.sin(2 * np.pi * 0.7 * t)
    phase = 2 * np.pi * np.cumsum(f0) / 16000
    y = sum(np.sin(k * phase) / k for k in range(1, 25))
    env = np.clip(np.sin(2 * np.pi * 4 * t), 0, None) ** 2
    return (0.2 * y * env).astype(np.float32)


def write_webm(path: Path, pcm: np.ndarray, sr: int, kbps_per_channel: int = 24) -> None:
    """Opus in WebM, the format Chromium's Media Source Extensions accept everywhere (8 kHz input is resampled)."""
    channels = 1 if pcm.ndim == 1 else pcm.shape[1]
    with av.open(str(path), "w", format="webm") as c:
        s = c.add_stream("libopus", rate=48000)
        s.layout = "mono" if channels == 1 else "stereo"
        s.bit_rate = kbps_per_channel * 1000 * channels
        res = av.AudioResampler(format="s16", layout=s.layout, rate=48000)
        data = (np.clip(pcm, -1, 1) * 32767).astype(np.int16).reshape(-1, channels)
        step = sr  # one second per frame batch
        for i in range(0, len(data), step):
            chunk = np.ascontiguousarray(data[i : i + step].T.reshape(1, -1) if channels > 1 else data[i : i + step].reshape(1, -1))
            frame = av.AudioFrame.from_ndarray(chunk, format="s16", layout=s.layout)
            frame.sample_rate = sr
            for f in res.resample(frame):
                for p in s.encode(f):
                    c.mux(p)
        for f in res.resample(None):
            for p in s.encode(f):
                c.mux(p)
        for p in s.encode(None):
            c.mux(p)


def logmel(y16: np.ndarray) -> np.ndarray:
    m = librosa.feature.melspectrogram(
        y=y16, sr=16000, n_fft=512, hop_length=160, win_length=400, n_mels=80, fmin=0, fmax=8000, power=2.0
    )
    rng = np.random.default_rng(1)
    m = m + 1e-5 * rng.random(m.shape)  # dither, as NeMo's preprocessor adds
    return np.log(m + 2**-24).astype(np.float16)  # [80, frames], unnormalised (cache-aware configs: normalize NA)


def place_words(segments: list[tuple[float, float]], vocab: list[str], n: int, seed: int) -> list[dict]:
    rng = np.random.default_rng(seed)
    total = sum(e - s for s, e in segments)
    words: list[dict] = []
    k = 0
    for s, e in segments:
        count = max(1, round(n * (e - s) / total))
        dur = (e - s) / count
        for j in range(count):
            if len(words) >= n:
                break
            w = vocab[k % len(vocab)]
            k += 1
            ws = s + j * dur
            op = rng.choice(["", "", "", "", "", "", "", "", "S", "I"])
            words.append(
                {"s": round(ws, 3), "e": round(ws + dur * 0.85, 3), "w": w, "c": round(float(rng.uniform(0.35, 1)), 2), "op": op}
            )
    while len(words) < n:  # rounding shortfall: pad inside the last segment
        s, e = segments[-1]
        words.append({"s": e - 0.01, "e": e, "w": vocab[0], "c": 1.0, "op": ""})
    return words[:n]


def bot_vocab(seed: int) -> list[str]:
    rng = np.random.default_rng(seed)
    v = []
    for i in range(4000):
        v.append(MIXED[i % len(MIXED)] if rng.random() < 0.12 else HEBREW[int(rng.integers(len(HEBREW)))])
    return v


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / "analysis" / "call").mkdir(parents=True, exist_ok=True)
    clips = fleurs_clips()
    source = "fleurs-sr-latn" if clips else "synthetic"
    if not clips:
        clips = [(chirp_speech(6 + (i % 5)), "sintetički govor primer reči") for i in range(60)]
    caller_vocab = " ".join(t for _, t in clips).split()

    # 20 s clip at 16 kHz: speech, a 1 kHz tone at -12 dBFS, a linear chirp, -60 dBFS noise.
    sr = 16000
    speech = np.concatenate([c for c, _ in clips[:4]])[: 17 * sr]
    t = np.arange(sr) / sr
    tone = 0.25 * np.sin(2 * np.pi * 1000 * t)
    t2 = np.arange(2 * sr) / sr
    chirp = 0.2 * np.sin(2 * np.pi * (100 * t2 + (7000 - 100) / 4 * t2**2))
    clip = np.concatenate([speech, np.zeros(17 * sr - len(speech)), tone, chirp]).astype(np.float32)
    clip += (10 ** (-60 / 20) * RNG.standard_normal(len(clip))).astype(np.float32)
    sf.write(OUT / "clip.wav", clip, sr, subtype="FLOAT")
    clip.astype("<f4").tofile(OUT / "clip.f32")
    write_webm(OUT / "clip.webm", clip, sr)
    seg_clip = [(0.0, 17.0)]
    json.dump(
        {
            "tracks": [
                {"id": "caller", "label": "Hypothesis · caller", "lang": "sr-Latn", "words": place_words(seg_clip, caller_vocab, 40, 1)},
                {"id": "bot", "label": "Hypothesis · he", "lang": "he", "words": place_words(seg_clip, bot_vocab(2), 40, 2)},
            ]
        },
        open(OUT / "words-clip.json", "w"),
        ensure_ascii=False,
    )
    clip_feats = logmel(clip)
    clip_feats.tofile(OUT / "analysis" / "clip.f16")

    # 30-minute 8 kHz stereo call: caller turn on the left, bot turn on the right, gaps between turns.
    sr8 = 8000
    n = int(CALL_S * sr8)
    call = np.zeros((n, 2), dtype=np.float32)
    band = butter(4, [300, 3400], btype="bandpass", fs=sr8, output="sos")
    segs: list[list[tuple[float, float]]] = [[], []]
    pos, k, ch = int(0.5 * sr8), 0, 0
    while pos < n - sr8:
        y = librosa.resample(clips[k % len(clips)][0], orig_sr=16000, target_sr=sr8)
        y = sosfilt(band, y).astype(np.float32) * (0.7 if ch == 0 else 0.5)
        end = min(n, pos + len(y))
        call[pos:end, ch] += y[: end - pos]
        segs[ch].append((pos / sr8, end / sr8))
        pos = end + int(RNG.uniform(0.3, 1.5) * sr8)
        k += 1
        ch ^= 1
    call += (10 ** (-55 / 20) * RNG.standard_normal(call.shape)).astype(np.float32)
    sf.write(OUT / "call.wav", call, sr8, subtype="PCM_16")
    # 8 kbps per channel: a 30-minute call fits one MSE SourceBuffer (Chromium refused 48 kbps stereo at ~7.4 MB).
    write_webm(OUT / "call.webm", call, sr8, kbps_per_channel=8)
    json.dump(
        {
            "tracks": [
                {"id": "caller", "label": "Hypothesis · caller", "lang": "sr-Latn", "words": place_words(segs[0], caller_vocab, WORDS_PER_TRACK, 3)},
                {"id": "bot", "label": "Hypothesis · bot (he)", "lang": "he", "words": place_words(segs[1], bot_vocab(4), WORDS_PER_TRACK, 4)},
            ]
        },
        open(OUT / "words-call.json", "w"),
        ensure_ascii=False,
    )
    # The model sees the caller channel upsampled to 16 kHz: about 18 of 80 mel filters sit above 4 kHz (dither only).
    call16 = librosa.resample(call[:, 0], orig_sr=sr8, target_sr=16000)
    call16.astype("<f4").tofile(OUT / "call16.f32")
    feats = logmel(call16)
    for i in range(0, feats.shape[1], CHUNK):
        np.ascontiguousarray(feats[:, i : i + CHUNK]).tofile(OUT / "analysis" / "call" / f"{i // CHUNK}.f16")
    meta = {
        "source": source,
        "clip": {"sampleRate": sr, "channels": 1, "duration": len(clip) / sr, "analysisFrames": int(clip_feats.shape[1])},
        "call": {"sampleRate": sr8, "channels": 2, "duration": n / sr8, "analysisFrames": int(feats.shape[1]), "analysisChunk": CHUNK},
    }
    json.dump(meta, open(OUT / "fixtures.json", "w"), indent=1)
    print(json.dumps(meta))


if __name__ == "__main__":
    main()
