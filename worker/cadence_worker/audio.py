"""Audio helpers for runtime-neutral step kinds: read WAV (PCM 8/16/24/32-bit, IEEE float 32/64-bit, extensible) in
pure Python, other formats through ``soundfile`` when it is installed (the NeMo Speech container has it), mix down to
mono, resample, and write the canonical form Cadence stores: 16-bit PCM WAV, mono.

The canonical WAV is written byte for byte the same everywhere (a 44-byte header and little-endian samples), so the
content hash of an imported utterance — its identity — does not depend on the library versions of the host that
imported it. NumPy (and SciPy for resampling) are used when present; the pure-Python paths keep tests independent
of them.
"""

from __future__ import annotations

import importlib
import io
import math
import struct
import sys
from array import array
from dataclasses import dataclass
from pathlib import Path
from typing import Any

PCM, IEEE_FLOAT, EXTENSIBLE = 1, 3, 0xFFFE


class AudioError(ValueError):
    """The audio cannot be read or converted."""


@dataclass(frozen=True)
class Audio:
    """Mono or interleaved float samples in [-1, 1]: a list, or a NumPy float64 array when NumPy is installed."""

    samples: Any
    sample_rate: int
    channels: int

    @property
    def frames(self) -> int:
        return len(self.samples) // self.channels

    @property
    def duration(self) -> float:
        return self.frames / self.sample_rate


def _optional(name: str) -> Any:
    try:
        return importlib.import_module(name)
    except ImportError:
        return None


def read(data: bytes | Path) -> Audio:
    """Decode WAV bytes or a file; non-WAV input needs ``soundfile``."""
    raw = data.read_bytes() if isinstance(data, Path) else data
    if raw[:4] == b"RIFF" and raw[8:12] == b"WAVE":
        return _read_wav(raw)
    sf = _optional("soundfile")
    if sf is None:
        raise AudioError("not a WAV file, and soundfile is not installed to decode other formats (FLAC, MP3, OGG)")
    try:
        frames, rate = sf.read(io.BytesIO(raw), dtype="float32", always_2d=True)
    except Exception as e:  # soundfile raises its own error types
        raise AudioError(f"cannot decode audio: {e}") from e
    channels = int(frames.shape[1])
    return Audio(samples=frames.reshape(-1).astype("float64"), sample_rate=int(rate), channels=channels)


def _read_wav(raw: bytes) -> Audio:
    fmt: tuple[int, int, int, int] | None = None  # tag, channels, rate, bits
    payload: bytes | None = None
    pos = 12
    while pos + 8 <= len(raw):
        cid, size = raw[pos : pos + 4], struct.unpack_from("<I", raw, pos + 4)[0]
        body = raw[pos + 8 : pos + 8 + size]
        if cid == b"fmt ":
            if len(body) < 16:
                raise AudioError("WAV fmt chunk is too short")
            tag, ch, rate, _, _, bits = struct.unpack_from("<HHIIHH", body)
            if tag == EXTENSIBLE and len(body) >= 26:
                tag = struct.unpack_from("<H", body, 24)[0]  # first two bytes of the subformat GUID
            fmt = (tag, ch, rate, bits)
        elif cid == b"data":
            payload = body
        pos += 8 + size + (size & 1)
    if fmt is None or payload is None:
        raise AudioError("WAV file without fmt or data chunk")
    tag, ch, rate, bits = fmt
    if ch < 1 or rate < 1:
        raise AudioError(f"WAV file with {ch} channels at {rate} Hz")
    return Audio(samples=_decode(payload, tag, bits), sample_rate=rate, channels=ch)


def _decode(payload: bytes, tag: int, bits: int) -> Any:
    np = _optional("numpy")
    if np is not None and bits in (16, 32, 64) and tag in (PCM, IEEE_FLOAT):
        width = bits // 8
        body = payload[: len(payload) - len(payload) % width]
        if tag == IEEE_FLOAT:
            if bits == 16:
                raise AudioError("16-bit float WAV is not supported")
            x = np.frombuffer(body, dtype="<f4" if bits == 32 else "<f8").astype(np.float64)
            return np.clip(np.nan_to_num(x, nan=0.0), -1.0, 1.0)
        if bits == 64:
            raise AudioError("64-bit PCM is not supported")
        scale = 32768.0 if bits == 16 else 2147483648.0
        return np.frombuffer(body, dtype="<i2" if bits == 16 else "<i4").astype(np.float64) / scale
    little = sys.byteorder == "little"
    if tag == IEEE_FLOAT and bits in (32, 64):
        a = array("f" if bits == 32 else "d")
        a.frombytes(payload[: len(payload) - len(payload) % a.itemsize])
        if not little:
            a.byteswap()
        return [0.0 if math.isnan(x) else max(-1.0, min(1.0, x)) for x in a]
    if tag != PCM:
        raise AudioError(f"WAV format tag {tag} is not PCM or IEEE float")
    if bits == 8:
        return [(b - 128) / 128.0 for b in payload]
    if bits == 16:
        a16 = array("h")
        a16.frombytes(payload[: len(payload) - len(payload) % 2])
        if not little:
            a16.byteswap()
        return [x / 32768.0 for x in a16]
    if bits == 24:
        n = len(payload) // 3
        return [int.from_bytes(payload[3 * i : 3 * i + 3], "little", signed=True) / 8388608.0 for i in range(n)]
    if bits == 32:
        a32 = array("i")
        a32.frombytes(payload[: len(payload) - len(payload) % 4])
        if not little:
            a32.byteswap()
        return [x / 2147483648.0 for x in a32]
    raise AudioError(f"{bits}-bit PCM is not supported")


def to_mono(a: Audio) -> Audio:
    if a.channels == 1:
        return a
    c = a.channels
    s = a.samples
    np = _optional("numpy")
    if np is not None:
        arr = np.asarray(s, dtype=np.float64)
        arr = arr[: len(arr) - len(arr) % c].reshape(-1, c)
        return Audio(samples=arr.sum(axis=1) / c, sample_rate=a.sample_rate, channels=1)
    mixed = [sum(s[i : i + c]) / c for i in range(0, len(s) - c + 1, c)]
    return Audio(samples=mixed, sample_rate=a.sample_rate, channels=1)


def resample(a: Audio, rate: int) -> Audio:
    """Resample mono audio: SciPy's polyphase filter when available, else linear interpolation."""
    if a.channels != 1:
        raise AudioError("resample expects mono audio")
    if a.sample_rate == rate:
        return a
    signal = _optional("scipy.signal")
    np = _optional("numpy")
    if signal is not None and np is not None:
        g = math.gcd(rate, a.sample_rate)
        out = signal.resample_poly(np.asarray(a.samples, dtype=np.float64), rate // g, a.sample_rate // g)
        return Audio(samples=np.clip(out, -1.0, 1.0), sample_rate=rate, channels=1)
    n_out = max(1, round(len(a.samples) * rate / a.sample_rate))
    src, step = a.samples, a.sample_rate / rate
    res = []
    last = len(src) - 1
    for i in range(n_out):
        x = i * step
        j = int(x)
        if j >= last:
            res.append(src[last])
            continue
        f = x - j
        res.append(src[j] * (1 - f) + src[j + 1] * f)
    return Audio(samples=res, sample_rate=rate, channels=1)


def canonical(a: Audio, rate: int) -> Audio:
    """Mono at ``rate``."""
    return resample(to_mono(a), rate)


def wav_bytes(a: Audio) -> bytes:
    """The canonical 16-bit PCM WAV of mono audio: a 44-byte header and little-endian samples."""
    if a.channels != 1:
        raise AudioError("canonical WAV is mono")
    np = _optional("numpy")
    if np is not None:
        # np.round and round() both round half to even, so both paths write the same bytes.
        data: bytes = (
            np.clip(np.round(np.asarray(a.samples, dtype=np.float64) * 32767), -32768, 32767).astype("<i2").tobytes()
        )
    else:
        pcm = array("h", (max(-32768, min(32767, round(x * 32767))) for x in a.samples))
        if sys.byteorder != "little":
            pcm.byteswap()
        data = pcm.tobytes()
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF",
        36 + len(data),
        b"WAVE",
        b"fmt ",
        16,
        PCM,
        1,
        a.sample_rate,
        a.sample_rate * 2,
        2,
        16,
        b"data",
        len(data),
    )
    return header + data
