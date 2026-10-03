"""The ``segments`` artifact (format ``cadence.segments/1``) and the canonical audio the ingest steps share.

Phase 4 indexes audio in place (docs/review/2026-10-03-phase-4-plan.md, decision 3): ``sdp_ingest`` reads files on a
mount and writes only this manifest; ``dataset_freeze`` cuts the segments later. Both decode a file the same way, so
a segment's ``hash`` — the BLAKE3 of its canonical WAV (16 kHz, mono, 16-bit PCM, the 44-byte header of
:func:`cadence_worker.audio.wav_bytes`) — is known before anything is copied and equals the content hash of the WAV
cut at freeze.

A segments artifact is a directory:

    segments.json    header: format, source {name}, root, language?, files, counts {segments}, hours, roles,
                     splitRule?, sourceInfo?, steps [kind@version, …], filtered?
    segments.jsonl   one segment per line (keys sorted): uri, file, hash, bytes, start, end, duration, channel, role,
                     language?, text?, origin?, confidence?, speaker?, split?, vad {speech, ratio},
                     level {rmsDb, peakDb, clipping}, crosstalk?, sourceRate, codec?, lid?, hypotheses?, …
    files.jsonl      optional: per source file uri, duration, sampleRate, channels, roles, codec?, speech per channel

Every step that rewrites segments keeps the keys it does not know. Help: docs/help/steps/sdp-ingest.md.
"""

from __future__ import annotations

import json
import math
import shutil
import subprocess
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
from numpy.typing import NDArray

from cadence_worker import audio
from cadence_worker.cas import hash_bytes
from cadence_worker.steps.base import StepInputError

FORMAT = "cadence.segments/1"
HEADER = "segments.json"
LINES = "segments.jsonl"
FILES = "files.jsonl"
RATE = 16000
ROLES = ("caller", "bot", "mono")
AUDIO_SUFFIXES = (".wav", ".flac", ".mp3", ".ogg", ".opus", ".m4a", ".aac", ".webm")
FFMPEG_TIMEOUT_S = 1800
CLIP_LEVEL = 0.999

Signal = NDArray[np.float64]


# ---------------------------------------------------------------- the artifact


def read(root: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """Header and segments of a segments artifact."""
    if not (root / HEADER).is_file() or not (root / LINES).is_file():
        raise StepInputError(f"the input is not a segments artifact ({HEADER}, {LINES})")
    try:
        header = json.loads((root / HEADER).read_text(encoding="utf-8"))
    except ValueError as e:
        raise StepInputError(f"{HEADER} is not JSON: {e}") from e
    if not isinstance(header, dict) or header.get("format") != FORMAT:
        raise StepInputError(f"{HEADER} is not {FORMAT}")
    lines: list[dict[str, Any]] = []
    for n, raw in enumerate((root / LINES).read_text(encoding="utf-8").splitlines(), 1):
        if not raw.strip():
            continue
        try:
            doc = json.loads(raw)
        except ValueError as e:
            raise StepInputError(f"{LINES} line {n} is not JSON: {e}") from e
        if not isinstance(doc, dict):
            raise StepInputError(f"{LINES} line {n} is not an object")
        lines.append(doc)
    return header, lines


def write(
    out: Path,
    header: Mapping[str, Any],
    lines: Iterable[Mapping[str, Any]],
    files_from: Path | None = None,
    files: Iterable[Mapping[str, Any]] | None = None,
) -> dict[str, Any]:
    """Write a segments artifact into the directory out; counts and hours are recomputed. files.jsonl is copied from
    files_from (an input artifact) or written from files."""
    out.mkdir(parents=True, exist_ok=True)
    rows = list(lines)
    h = dict(header)
    h["format"] = FORMAT
    h["counts"] = {"segments": len(rows)}
    h["hours"] = round(sum(float(x.get("duration", 0.0)) for x in rows) / 3600, 6)
    with (out / LINES).open("w", encoding="utf-8") as f:
        for x in rows:
            f.write(dumps(x) + "\n")
    if files is not None:
        with (out / FILES).open("w", encoding="utf-8") as f:
            for x in files:
                f.write(dumps(x) + "\n")
    elif files_from is not None and (files_from / FILES).is_file():
        shutil.copyfile(files_from / FILES, out / FILES)
    (out / HEADER).write_text(json.dumps(h, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return h


def dumps(x: Mapping[str, Any]) -> str:
    return json.dumps(x, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def with_step(header: Mapping[str, Any], kind: str) -> dict[str, Any]:
    """The header with kind@version appended to its steps."""
    h = dict(header)
    h["steps"] = [*list(h.get("steps") or []), kind]
    return h


# ---------------------------------------------------------------- decoding


@dataclass(frozen=True)
class Decoded:
    """A file's channels at its native rate, as float samples in [-1, 1]."""

    channels: list[Signal]
    rate: int
    codec: str | None = None

    @property
    def duration(self) -> float:
        return len(self.channels[0]) / self.rate if self.channels else 0.0


def decode(path: Path, work: Path) -> Decoded:
    """Decode every channel of a file: PCM and float WAV in pure Python, anything else (μ-law WAV, FLAC, MP3, OGG/Opus,
    M4A) through ffmpeg when the image has it, else soundfile. The same function serves ingest and freeze, so a
    segment's samples — and its hash — are the same at both."""
    raw = path.read_bytes()
    if raw[:4] == b"RIFF" and raw[8:12] == b"WAVE":
        try:
            return _split(audio.read(raw), _wav_codec(raw))
        except audio.AudioError as e:
            if shutil.which("ffmpeg") is None:
                raise StepInputError(f"{path.name}: {e} (and this image has no ffmpeg to decode it)") from e
    ffmpeg = shutil.which("ffmpeg")
    if ffmpeg is None:
        try:
            return _split(audio.read(raw), None)
        except audio.AudioError as e:
            raise StepInputError(f"{path.name}: {e}") from e
    work.mkdir(parents=True, exist_ok=True)
    out = work / (path.name + ".decoded.wav")
    cmd = [ffmpeg, "-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file", "-i", "file:" + str(path)]
    cmd += ["-map", "0:a:0", "-c:a", "pcm_f32le", "-f", "wav", str(out)]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=FFMPEG_TIMEOUT_S, check=False)
        if res.returncode != 0:
            raise StepInputError(f"{path.name}: ffmpeg could not decode it: {res.stderr.strip()[-300:]}")
        return _split(audio.read(out), _probe_codec(path))
    finally:
        out.unlink(missing_ok=True)


def _split(a: audio.Audio, codec: str | None) -> Decoded:
    x = np.asarray(a.samples, dtype=np.float64)
    c = a.channels
    x = x[: x.size - x.size % c].reshape(-1, c)
    return Decoded(channels=[np.ascontiguousarray(x[:, i]) for i in range(c)], rate=a.sample_rate, codec=codec)


def _wav_codec(raw: bytes) -> str | None:
    pos = 12
    while pos + 8 <= len(raw):
        cid, size = raw[pos : pos + 4], int.from_bytes(raw[pos + 4 : pos + 8], "little")
        if cid == b"fmt " and size >= 16:
            tag = int.from_bytes(raw[pos + 8 : pos + 10], "little")
            bits = int.from_bytes(raw[pos + 22 : pos + 24], "little")
            if tag == 0xFFFE and size >= 26:
                tag = int.from_bytes(raw[pos + 32 : pos + 34], "little")
            return {
                1: f"pcm_s{bits}le" if bits > 8 else "pcm_u8",
                3: f"pcm_f{bits}le",
                6: "pcm_alaw",
                7: "pcm_mulaw",
            }.get(tag, f"wav-{tag}")
        pos += 8 + size + (size & 1)
    return None


def _probe_codec(path: Path) -> str | None:
    ffprobe = shutil.which("ffprobe")
    if ffprobe is None:
        return None
    cmd = [ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "json"]
    res = subprocess.run([*cmd, "file:" + str(path)], capture_output=True, text=True, timeout=60, check=False)
    try:
        streams = json.loads(res.stdout or "{}").get("streams") or []
        return str(streams[0]["codec_name"]) if streams else None
    except (ValueError, KeyError, TypeError):
        return None


def to_rate(x: Signal, rate: int) -> Signal:
    """One channel at 16 kHz: the import resampler (scipy's resample_poly) over the whole channel."""
    if rate == RATE:
        return np.asarray(x, dtype=np.float64)
    out = audio.resample(audio.Audio(samples=x, sample_rate=rate, channels=1), RATE)
    return np.asarray(out.samples, dtype=np.float64)


MIXED = -1  # the channel of a segment cut from all of a file's channels mixed down (no ``ch`` in its URI)


def track(d: Decoded, channel: int) -> Signal:
    """One channel of a decoded file at 16 kHz; MIXED is the mean of every channel (at the native rate, then
    resampled), the same at ingest and at freeze."""
    if channel == MIXED:
        x = d.channels[0] if len(d.channels) == 1 else np.mean(np.stack(d.channels), axis=0)
        return to_rate(x, d.rate)
    if not 0 <= channel < len(d.channels):
        raise StepInputError(f"the file has {len(d.channels)} channel(s); channel {channel} does not exist")
    return to_rate(d.channels[channel], d.rate)


# ---------------------------------------------------------------- one segment


def bounds(start: float, end: float) -> tuple[int, int]:
    return round(start * RATE), round(end * RATE)


def wav_of(x16: Signal, i0: int, i1: int) -> bytes:
    """The canonical WAV of samples [i0, i1) of a 16 kHz channel."""
    return audio.wav_bytes(audio.Audio(samples=x16[i0:i1], sample_rate=RATE, channels=1))


def hash_of(wav: bytes) -> str:
    return hash_bytes(wav)


def level(x: Signal) -> dict[str, float]:
    """rms and peak in dBFS and the share of samples at full scale."""
    if x.size == 0:
        return {"rmsDb": -120.0, "peakDb": -120.0, "clipping": 0.0}
    a = np.abs(x)
    rms = float(np.sqrt(np.mean(x * x)))
    return {
        "rmsDb": round(_db(rms), 2),
        "peakDb": round(_db(float(a.max())), 2),
        "clipping": round(float(np.mean(a >= CLIP_LEVEL)), 6),
    }


def _db(v: float) -> float:
    return max(-120.0, 20 * math.log10(v)) if v > 0 else -120.0


# ---------------------------------------------------------------- energy VAD and segmentation


@dataclass(frozen=True)
class VadParams:
    frame_ms: int
    margin_db: float
    floor_db: float
    min_speech_ms: int
    min_silence_ms: int
    pad_ms: int
    max_segment_s: float
    min_segment_s: float


Interval = tuple[float, float]


def frame_levels(x16: Signal, frame_ms: int) -> Signal:
    n = max(1, RATE * frame_ms // 1000)
    frames = x16.size // n
    if frames == 0:
        return np.zeros(0)
    f = x16[: frames * n].reshape(frames, n)
    return np.asarray(10 * np.log10(np.mean(f * f, axis=1) + 1e-12), dtype=np.float64)


def speech(x16: Signal, p: VadParams) -> list[Interval]:
    """Speech runs of one 16 kHz channel in absolute seconds: frames above max(noise floor + margin, floor_db),
    pauses shorter than min_silence closed, runs shorter than min_speech dropped."""
    lv = frame_levels(x16, p.frame_ms)
    if lv.size == 0:
        return []
    noise = float(np.percentile(lv, 10))
    on = lv > max(noise + p.margin_db, p.floor_db)
    fs = p.frame_ms / 1000
    runs: list[list[float]] = []
    i = 0
    while i < on.size:
        if on[i]:
            j = i
            while j < on.size and on[j]:
                j += 1
            runs.append([i * fs, j * fs])
            i = j
        else:
            i += 1
    merged: list[list[float]] = []
    for r in runs:
        if merged and r[0] - merged[-1][1] < p.min_silence_ms / 1000:
            merged[-1][1] = r[1]
        else:
            merged.append(r)
    dur = x16.size / RATE
    return [(round(a, 6), round(min(b, dur), 6)) for a, b in merged if b - a >= p.min_speech_ms / 1000]


def segments_of(x16: Signal, runs: Sequence[Interval], p: VadParams) -> list[Interval]:
    """Segments from speech runs: padded, overlapping ones joined, long ones split at their quietest frame, short ones
    dropped. Bounds are on the 16 kHz sample grid."""
    dur = x16.size / RATE
    pad = p.pad_ms / 1000
    joined: list[list[float]] = []
    for a, b in runs:
        a, b = max(0.0, a - pad), min(dur, b + pad)
        if joined and a <= joined[-1][1]:
            joined[-1][1] = max(joined[-1][1], b)
        else:
            joined.append([a, b])
    lv = frame_levels(x16, p.frame_ms)
    out: list[Interval] = []
    for a, b in joined:
        out.extend(_split_long(a, b, lv, p))
    snapped = [(i0 / RATE, i1 / RATE) for i0, i1 in (bounds(a, b) for a, b in out)]
    return [(a, b) for a, b in snapped if b - a >= p.min_segment_s and b > a]


def _split_long(a: float, b: float, lv: Signal, p: VadParams) -> list[Interval]:
    if b - a <= p.max_segment_s:
        return [(a, b)]
    fs = p.frame_ms / 1000
    lo = math.ceil((a + max(p.min_segment_s, 1.0)) / fs)
    hi = math.floor((b - max(p.min_segment_s, 1.0)) / fs)
    # Prefer a cut that leaves the first part at most max_segment_s long.
    hi = min(hi, math.floor((a + p.max_segment_s) / fs))
    if hi <= lo or lo >= lv.size:
        cut = a + p.max_segment_s
    else:
        window = lv[lo : min(hi, lv.size)]
        cut = (lo + int(np.argmin(window))) * fs + fs / 2 if window.size else a + p.max_segment_s
    return [(a, cut), *_split_long(cut, b, lv, p)]


def within(runs: Sequence[Interval], a: float, b: float) -> list[Interval]:
    """The parts of runs inside [a, b], relative to a."""
    out: list[Interval] = []
    for s, e in runs:
        lo, hi = max(s, a), min(e, b)
        if hi > lo:
            out.append((round(lo - a, 6), round(hi - a, 6)))
    return out


def covered(runs: Sequence[Interval], a: float, b: float) -> float:
    """Share of [a, b] the runs cover (runs do not overlap each other)."""
    if b <= a:
        return 0.0
    return min(1.0, sum(e - s for s, e in within(runs, a, b)) / (b - a))


def vad_of(runs: Sequence[Interval], a: float, b: float) -> dict[str, Any]:
    parts = within(runs, a, b)
    return {"speech": [[s, e] for s, e in parts], "ratio": round(covered(runs, a, b), 4)}
