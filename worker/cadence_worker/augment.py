"""Deterministic, runtime-neutral audio augmentation for evaluation (docs/spec/03-pipelines-defaults.md
"Augmentation"; phase 3 stream R): the transforms of an augmentation profile applied to one utterance with NumPy only,
so the same profile, seed and audio give the same samples on every runtime.

The profile is the ``augment_profile`` artifact the control plane renders from a project's ``augment/<name>.yaml`` at a
commit (every value the file leaves out comes from defaults.yaml ``augment.*``)::

    {"format": "cadence.augment_profile/1", "name": "telephony", "seed": 1234, "hash": "sha256:…",
     "transforms": {"codec": {"probability": 0.5, "codecs": ["g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus"]},
                    "band_limit": {"probability": 0.5, "cutoff_hz": 3400},
                    "level": {"probability": 0.3, "gain_db": [-10, 6]},
                    "speed": {"probability": 0.3, "factor": [0.9, 1.1]},
                    "noise": {"probability": 0.5, "snr_db": [0, 20], "bank": "ver_…"}}}

Per utterance the draw comes from its own generator, seeded by the profile's seed and the utterance's audio hash, so
it does not depend on the order or the subset of utterances. The chain, each stage with its probability:

1. ``speed``: speed perturbation by resampling (tempo and pitch together), factor uniform in the range;
2. ``noise``: a clip of the noise bank, looped and offset at random, mixed at an SNR uniform in ``snr_db`` (speech power
   over the whole utterance);
3. ``level``: a gain uniform in ``gain_db``, clipped at full scale;
4. ``band_limit`` / ``codec``: narrowband telephony — resampled to 8 kHz (which keeps 0-4 kHz), low-passed at
   ``cutoff_hz`` when ``band_limit`` was drawn, through one codec drawn from ``codecs`` when ``codec`` was drawn, and
   back to the model's rate. A codec is always narrowband, so drawing ``codec`` alone also band-limits to 4 kHz.

Codecs implemented here: G.711 μ-law and A-law (computed, no library). GSM-FR, AMR-NB and Opus need native codecs
whose output differs between library versions; they are left out of the draw and reported (``unavailable``), never
silently replaced.
"""

from __future__ import annotations

from collections.abc import Sequence
from typing import Annotated, Any, Literal

import numpy as np
from numpy.typing import NDArray
from pydantic import BaseModel, ConfigDict, Field, ValidationError, model_validator

FORMAT: Literal["cadence.augment_profile/1"] = "cadence.augment_profile/1"
TELEPHONE_RATE = 8000
CODECS_IMPLEMENTED = ("g711-ulaw", "g711-alaw")
CODECS_KNOWN = ("g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus")

Audio = NDArray[np.float64]
Prob = Annotated[float, Field(ge=0, le=1)]


class ProfileError(ValueError):
    pass


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


class Codec(_Strict):
    probability: Prob
    codecs: list[Literal["g711-ulaw", "g711-alaw", "gsm-fr", "amr-nb", "opus"]]


class BandLimit(_Strict):
    probability: Prob
    cutoff_hz: Annotated[float, Field(ge=1000, le=8000)]


class Level(_Strict):
    probability: Prob
    gain_db: Annotated[list[float], Field(min_length=2, max_length=2)]


class Speed(_Strict):
    probability: Prob
    factor: Annotated[list[float], Field(min_length=2, max_length=2)]


class Noise(_Strict):
    probability: Prob
    snr_db: Annotated[list[float], Field(min_length=2, max_length=2)]
    bank: str = ""


class Transforms(_Strict):
    codec: Codec | None = None
    band_limit: BandLimit | None = None
    level: Level | None = None
    speed: Speed | None = None
    noise: Noise | None = None

    @model_validator(mode="after")
    def _ranges(self) -> Transforms:
        for name, pair in (
            ("level.gain_db", self.level.gain_db if self.level else None),
            ("speed.factor", self.speed.factor if self.speed else None),
            ("noise.snr_db", self.noise.snr_db if self.noise else None),
        ):
            if pair is not None and pair[0] > pair[1]:
                raise ValueError(f"{name}: the lower bound {pair[0]} is above the upper {pair[1]}")
        if self.speed and not (self.speed.factor[0] >= 0.5 and self.speed.factor[1] <= 2.0):
            raise ValueError("speed.factor must lie within 0.5 to 2")
        return self


class Profile(_Strict):
    format: Literal["cadence.augment_profile/1"] = FORMAT
    name: str = "custom"
    # The control plane's content hash of the profile (transforms and seed); the eval-record key names it.
    hash: str = ""
    seed: Annotated[int, Field(ge=0, le=2**31 - 1)]
    transforms: Transforms

    @property
    def codecs(self) -> list[str]:
        """The codecs the draw picks from (implemented ones, in the profile's order)."""
        return [c for c in (self.transforms.codec.codecs if self.transforms.codec else []) if c in CODECS_IMPLEMENTED]

    @property
    def unavailable(self) -> list[str]:
        """Codecs the profile names that this module does not implement (left out of the draw)."""
        return [
            c for c in (self.transforms.codec.codecs if self.transforms.codec else []) if c not in CODECS_IMPLEMENTED
        ]


def parse_profile(raw: Any) -> Profile:
    try:
        return Profile.model_validate(raw)
    except ValidationError as e:
        raise ProfileError(f"augmentation profile: {e}") from e


# ---------------------------------------------------------------- transforms


def resample(x: Audio, src: int, dst: int) -> Audio:
    """Band-limited resampling through the FFT (the spectrum above the lower Nyquist rate is dropped)."""
    if src == dst or x.size == 0:
        return x
    n_out = max(1, round(x.size * dst / src))
    spec = np.fft.rfft(x)
    keep = min(spec.size, n_out // 2 + 1)
    out = np.zeros(n_out // 2 + 1, dtype=np.complex128)
    out[:keep] = spec[:keep]
    return np.fft.irfft(out, n=n_out) * (n_out / x.size)


def lowpass(x: Audio, rate: int, cutoff_hz: float) -> Audio:
    """Zero the spectrum above ``cutoff_hz``."""
    if x.size == 0 or cutoff_hz >= rate / 2:
        return x
    spec = np.fft.rfft(x)
    freqs = np.fft.rfftfreq(x.size, d=1.0 / rate)
    spec[freqs > cutoff_hz] = 0
    return np.fft.irfft(spec, n=x.size)


MU = 255.0
A = 87.6


def mulaw(x: Audio) -> Audio:
    """G.711 μ-law: compand, quantise to 8 bits, expand."""
    c = np.sign(x) * np.log1p(MU * np.minimum(np.abs(x), 1.0)) / np.log1p(MU)
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    return np.asarray(np.sign(q) * ((1 + MU) ** np.abs(q) - 1) / MU, dtype=np.float64)


def alaw(x: Audio) -> Audio:
    """G.711 A-law: compand, quantise to 8 bits, expand."""
    ax = np.minimum(np.abs(x), 1.0)
    big = 1 + np.log(A)
    c = np.where(ax < 1 / A, A * ax / big, (1 + np.log(np.maximum(A * ax, 1e-12))) / big)
    q = np.round((np.sign(x) * c + 1) / 2 * 255) / 255 * 2 - 1
    aq = np.abs(q)
    y = np.where(aq < 1 / big, aq * big / A, np.exp(aq * big - 1) / A)
    return np.asarray(np.sign(q) * y, dtype=np.float64)


def codec(x: Audio, name: str) -> Audio:
    if name == "g711-ulaw":
        return mulaw(x)
    if name == "g711-alaw":
        return alaw(x)
    raise ProfileError(f"codec {name} is not implemented")


def speed(x: Audio, factor: float) -> Audio:
    """Speed perturbation by linear-interpolation resampling: ``factor`` 1.1 plays 10 % faster (shorter)."""
    if factor == 1.0 or x.size < 2:
        return x
    n = max(2, round(x.size / factor))
    return np.interp(np.linspace(0, x.size - 1, n), np.arange(x.size), x)


def mix_noise(x: Audio, noise: Audio, snr_db: float, offset: float) -> Audio:
    """``noise`` looped to the length of ``x`` from ``offset`` (a fraction of the clip), scaled to ``snr_db`` below
    the speech power."""
    if x.size == 0 or noise.size == 0:
        return x
    ps = float(np.mean(x**2))
    if ps <= 0:
        return x
    start = min(noise.size - 1, int(offset * noise.size))
    reps = -(-(start + x.size) // noise.size)
    n = np.tile(noise, reps)[start : start + x.size]
    pn = float(np.mean(n**2))
    if pn <= 0:
        return x
    gain = np.sqrt(ps / (pn * 10 ** (snr_db / 10)))
    return np.asarray(x + gain * n, dtype=np.float64)


def utterance_rng(seed: int, audio_hash: str) -> np.random.Generator:
    """The generator of one utterance: the profile's seed and the first 64 bits of its audio hash."""
    digest = audio_hash.split(":", 1)[-1]
    return np.random.default_rng([seed, int(digest[:16], 16)])


def apply(
    x: Audio, rate: int, profile: Profile, rng: np.random.Generator, noise_bank: Sequence[Audio] = ()
) -> tuple[Audio, list[str]]:
    """One utterance through the profile; returns the audio and the stages applied (``speed:1.043``, …).

    Every coin and value is drawn up front in a fixed order whether or not its stage applies, so a stage's
    probability or presence never shifts the draws of another."""
    t = profile.transforms
    coins = rng.random(5)
    u = rng.random(7)  # speed, noise clip, noise SNR, noise offset, level, codec, spare

    def between(pair: Sequence[float], v: float) -> float:
        return float(pair[0] + (pair[1] - pair[0]) * v)

    applied: list[str] = []
    y = np.asarray(x, dtype=np.float64)
    if t.speed and coins[0] < t.speed.probability:
        f = between(t.speed.factor, u[0])
        y = speed(y, f)
        applied.append(f"speed:{f:.3f}")
    if t.noise and coins[1] < t.noise.probability and noise_bank:
        clip = noise_bank[min(len(noise_bank) - 1, int(u[1] * len(noise_bank)))]
        snr = between(t.noise.snr_db, u[2])
        y = mix_noise(y, clip, snr, float(u[3]))
        applied.append(f"noise:{snr:.1f}dB")
    if t.level and coins[2] < t.level.probability:
        db = between(t.level.gain_db, u[4])
        y = y * 10 ** (db / 20)
        applied.append(f"level:{db:+.1f}dB")
    band = bool(t.band_limit and coins[3] < t.band_limit.probability)
    codecs = profile.codecs
    use_codec = bool(t.codec and codecs and coins[4] < t.codec.probability)
    if band or use_codec:
        n = y.size
        low = resample(y, rate, TELEPHONE_RATE)
        if band and t.band_limit:
            low = lowpass(low, TELEPHONE_RATE, t.band_limit.cutoff_hz)
            applied.append(f"band_limit:{t.band_limit.cutoff_hz:g}Hz")
        if use_codec:
            name = codecs[min(len(codecs) - 1, int(u[5] * len(codecs)))]
            low = codec(np.clip(low, -1.0, 1.0), name)
            applied.append(f"codec:{name}")
        y = resample(low, TELEPHONE_RATE, rate)
        y = y[:n] if y.size >= n else np.pad(y, (0, n - y.size))
    return np.clip(y, -1.0, 1.0), applied
