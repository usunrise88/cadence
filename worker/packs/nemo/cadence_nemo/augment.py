"""Telephony augmentation on the fly (docs/spec/03-pipelines-defaults.md "Augmentation"), applied per clip after the
dataloader reads it; nothing is written to disk.

A profile (the ``augmentation`` parameter; ``packs.nemo.augmentation`` is the ``telephony`` default, ``{"profile":
"clean"}`` turns everything off) is a chain, each stage drawn per clip with its probability:

1. ``telephone``: band-limit to 8 kHz (resample down), optionally through one codec — G.711 μ-law or A-law (computed
   here) or GSM 06.10 (libsndfile, present in the NeMo Speech image) — then back to the model's rate. AMR-NB and Opus
   need ffmpeg, which the image lacks: a profile naming them is refused at the start, not skipped silently.
2. ``gain``: a level change drawn uniformly in dB, with clipping at full scale.
3. ``speed``: speed perturbation by resampling (tempo and pitch together, as Kaldi and NeMo do), factor drawn uniformly.
   A factor below 1 lengthens the clip, so training scales the calibrated bucket batch sizes by the smallest factor.

Noise from a noise bank and room impulse responses join when the noise bank registry asset exists (phase 4).
"""

from __future__ import annotations

import io
from typing import Any, Literal

import numpy as np
from numpy.typing import NDArray
from pydantic import BaseModel, ConfigDict, Field, ValidationError, model_validator

from cadence_worker.steps.base import StepInputError

Codec = Literal["ulaw", "alaw", "gsm"]
UNAVAILABLE_CODECS = {"amr": "AMR-NB needs ffmpeg, which the NeMo Speech image lacks", "opus": "Opus needs ffmpeg"}
Audio = NDArray[np.float32]


class Telephone(BaseModel):
    model_config = ConfigDict(extra="forbid")
    p: float = Field(ge=0, le=1)
    sample_rate: int = Field(default=8000, ge=4000, le=16000)
    codecs: list[Codec] = Field(default_factory=list)
    codec_p: float = Field(default=1.0, ge=0, le=1)


class Gain(BaseModel):
    model_config = ConfigDict(extra="forbid")
    p: float = Field(ge=0, le=1)
    min_db: float = Field(ge=-40, le=20)
    max_db: float = Field(ge=-40, le=20)


class Speed(BaseModel):
    model_config = ConfigDict(extra="forbid")
    p: float = Field(ge=0, le=1)
    min: float = Field(ge=0.8, le=1.2)
    max: float = Field(ge=0.8, le=1.2)


class Profile(BaseModel):
    model_config = ConfigDict(extra="forbid")
    profile: str = "custom"
    telephone: Telephone | None = None
    gain: Gain | None = None
    speed: Speed | None = None

    @model_validator(mode="after")
    def _ranges(self) -> Profile:
        if self.gain and self.gain.min_db > self.gain.max_db:
            raise ValueError("gain: min_db is above max_db")
        if self.speed and self.speed.min > self.speed.max:
            raise ValueError("speed: min is above max")
        return self

    @property
    def enabled(self) -> bool:
        return self.profile != "clean" and any(
            s is not None and s.p > 0 for s in (self.telephone, self.gain, self.speed)
        )

    @property
    def min_speed(self) -> float:
        return self.speed.min if self.enabled and self.speed and self.speed.p > 0 else 1.0


def parse_profile(raw: Any) -> Profile:
    if not isinstance(raw, dict):
        raise StepInputError("augmentation must be an object (a profile)")
    if raw.get("profile") == "clean":
        return Profile(profile="clean")
    tel = raw.get("telephone")
    if isinstance(tel, dict):
        bad = [c for c in tel.get("codecs") or [] if c in UNAVAILABLE_CODECS]
        if bad:
            raise StepInputError("augmentation: " + "; ".join(UNAVAILABLE_CODECS[c] for c in bad))
    try:
        return Profile.model_validate(raw)
    except ValidationError as e:
        raise StepInputError(f"augmentation profile: {e}") from e


# ---------------------------------------------------------------- transforms


def resample(x: Audio, src: int, dst: int) -> Audio:
    """Band-limited resampling through the FFT (the spectrum above the lower Nyquist rate is dropped)."""
    if src == dst or x.size == 0:
        return x.astype(np.float32, copy=False)
    n_out = max(1, round(x.size * dst / src))
    spec = np.fft.rfft(x.astype(np.float64))
    keep = min(spec.size, n_out // 2 + 1)
    out_spec = np.zeros(n_out // 2 + 1, dtype=np.complex128)
    out_spec[:keep] = spec[:keep]
    y = np.fft.irfft(out_spec, n=n_out) * (n_out / x.size)
    return y.astype(np.float32)


MU = 255.0


def mulaw(x: Audio) -> Audio:
    """G.711 μ-law: compand, quantise to 8 bits, expand."""
    c = np.sign(x) * np.log1p(MU * np.minimum(np.abs(x), 1.0)) / np.log1p(MU)
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    return np.asarray(np.sign(q) * ((1 + MU) ** np.abs(q) - 1) / MU, dtype=np.float32)


A = 87.6


def alaw(x: Audio) -> Audio:
    """G.711 A-law: compand, quantise to 8 bits, expand."""
    ax = np.minimum(np.abs(x), 1.0)
    small = ax < 1 / A
    c = np.where(small, A * ax / (1 + np.log(A)), (1 + np.log(np.maximum(A * ax, 1e-12))) / (1 + np.log(A)))
    c = np.sign(x) * c
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    aq = np.abs(q)
    y = np.where(aq < 1 / (1 + np.log(A)), aq * (1 + np.log(A)) / A, np.exp(aq * (1 + np.log(A)) - 1) / A)
    return np.asarray(np.sign(q) * y, dtype=np.float32)


def gsm(x: Audio, sample_rate: int) -> Audio:
    """GSM 06.10 full rate through libsndfile (8 kHz only)."""
    import soundfile as sf  # from the NeMo Speech image

    if sample_rate != 8000:
        raise ValueError("GSM 06.10 runs at 8 kHz")
    buf = io.BytesIO()
    sf.write(buf, np.clip(x, -1, 1), 8000, format="RAW", subtype="GSM610")
    buf.seek(0)
    y, _ = sf.read(buf, samplerate=8000, channels=1, format="RAW", subtype="GSM610", dtype="float32")
    y = np.asarray(y, dtype=np.float32).reshape(-1)
    return y[: x.size] if y.size >= x.size else np.pad(y, (0, x.size - y.size))


def codec(x: Audio, name: str, sample_rate: int) -> Audio:
    if name == "ulaw":
        return mulaw(x)
    if name == "alaw":
        return alaw(x)
    if name == "gsm":
        return gsm(x, sample_rate)
    raise ValueError(f"unknown codec {name!r}")


def speed(x: Audio, factor: float) -> Audio:
    """Speed perturbation by linear-interpolation resampling: ``factor`` 1.1 plays 10 % faster (shorter)."""
    if factor == 1.0 or x.size < 2:
        return x
    n = max(2, round(x.size / factor))
    pos = np.linspace(0, x.size - 1, n)
    return np.interp(pos, np.arange(x.size), x).astype(np.float32)


def apply(x: Audio, sample_rate: int, profile: Profile, rng: np.random.Generator) -> tuple[Audio, list[str]]:
    """One clip through the profile; returns the audio and the stages applied (for logs and tests)."""
    applied: list[str] = []
    if not profile.enabled:
        return x, applied
    y = x.astype(np.float32, copy=True)
    t = profile.telephone
    if t and rng.random() < t.p:
        low = resample(y, sample_rate, t.sample_rate)
        if t.codecs and rng.random() < t.codec_p:
            name = str(rng.choice(t.codecs))
            low = codec(low, name, t.sample_rate)
            applied.append(f"codec:{name}")
        y = resample(low, t.sample_rate, sample_rate)
        y = y[: x.size] if y.size >= x.size else np.pad(y, (0, x.size - y.size))
        applied.append(f"bandlimit:{t.sample_rate}")
    g = profile.gain
    if g and rng.random() < g.p:
        db = float(rng.uniform(g.min_db, g.max_db))
        y = np.clip(y * np.float32(10 ** (db / 20)), -1.0, 1.0).astype(np.float32)
        applied.append(f"gain:{db:+.1f}dB")
    s = profile.speed
    if s and rng.random() < s.p:
        f = float(rng.uniform(s.min, s.max))
        y = speed(y, f)
        applied.append(f"speed:{f:.3f}")
    return y, applied
