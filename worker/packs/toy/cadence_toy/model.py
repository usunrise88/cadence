"""The toy-ctc model: log-mel features computed on the fly (never stored, R42), a character tokenizer, a linear layer,
a layer norm and a unidirectional GRU with a CTC head (≈ 60 k parameters). Unidirectional, so chunked decoding with the
carried hidden state is genuinely streaming and gives the same result as the offline decode.
"""

from __future__ import annotations

import math
import time
import wave
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import torch
from torch import nn

SAMPLE_RATE = 16000
N_FFT = 512
WIN = 400  # 25 ms
HOP = 160  # 10 ms
N_MELS = 40
STACK = 2  # model frames are 20 ms
FRAME_MS = 1000 * HOP * STACK // SAMPLE_RATE
VOCAB = ["<blank>", " ", *"abcdefghijklmnopqrstuvwxyz", "'"]


def use_one_thread() -> None:
    """The model is tiny: more CPU threads only contend (60 steps take 1.4 s on one thread, 13 s on many)."""
    torch.set_num_threads(1)


# ---------------------------------------------------------------- tokenizer


class CharTokenizer:
    kind = "chars"

    def __init__(self, vocab: list[str] | None = None) -> None:
        self.vocab = list(vocab or VOCAB)
        self.index = {c: i for i, c in enumerate(self.vocab)}

    def normalise(self, text: str) -> str:
        kept = "".join(c if c in self.index and c != "<blank>" else " " for c in text.lower())
        return " ".join(kept.split())

    def encode(self, text: str) -> list[int]:
        return [self.index[c] for c in self.normalise(text)]

    def decode(self, ids: list[int]) -> str:
        return "".join(self.vocab[i] for i in ids if i > 0)

    def to_json(self) -> dict[str, Any]:
        return {"kind": self.kind, "vocab": self.vocab, "blank": 0}


# ---------------------------------------------------------------- audio and features


def read_wav(path: Path) -> torch.Tensor:
    """16 kHz mono 16-bit PCM WAV → float32 samples in [-1, 1]."""
    with wave.open(str(path), "rb") as w:
        if w.getframerate() != SAMPLE_RATE or w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise ValueError(f"{path}: toy-ctc reads 16 kHz mono 16-bit WAV only")
        raw = w.readframes(w.getnframes())
    return torch.frombuffer(bytearray(raw), dtype=torch.int16).to(torch.float32) / 32768.0


def _mel_filters() -> torch.Tensor:
    def hz_to_mel(f: float) -> float:
        return 2595.0 * math.log10(1.0 + f / 700.0)

    def mel_to_hz(m: torch.Tensor) -> torch.Tensor:
        return 700.0 * (10 ** (m / 2595.0) - 1.0)

    n_bins = N_FFT // 2 + 1
    mels = torch.linspace(hz_to_mel(0.0), hz_to_mel(SAMPLE_RATE / 2), N_MELS + 2)
    hz = mel_to_hz(mels)
    freqs = torch.linspace(0, SAMPLE_RATE / 2, n_bins)
    fb = torch.zeros(N_MELS, n_bins)
    for i in range(N_MELS):
        lo, mid, hi = hz[i].item(), hz[i + 1].item(), hz[i + 2].item()
        up = (freqs - lo) / max(mid - lo, 1e-6)
        down = (hi - freqs) / max(hi - mid, 1e-6)
        fb[i] = torch.clamp(torch.minimum(up, down), min=0.0)
    return fb


_FB = _mel_filters()
_WINDOW = torch.hann_window(WIN)


def features(samples: torch.Tensor) -> torch.Tensor:
    """Log-mel frames stacked by two: (frames, 80). Frame-local (no utterance statistics), so chunks compose."""
    if samples.numel() < WIN:
        samples = torch.nn.functional.pad(samples, (0, WIN - samples.numel()))
    spec = torch.stft(samples, N_FFT, hop_length=HOP, win_length=WIN, window=_WINDOW, center=False, return_complex=True)
    power = spec.abs().pow(2)  # (bins, frames)
    mel = torch.log(_FB @ power + 1e-6).T  # (frames, mels)
    mel = (mel + 6.0) / 4.0
    usable = (mel.shape[0] // STACK) * STACK
    return mel[:usable].reshape(-1, N_MELS * STACK)


# ---------------------------------------------------------------- model


@dataclass(frozen=True)
class ModelConfig:
    input_dim: int = N_MELS * STACK
    hidden: int = 96
    vocab: int = len(VOCAB)
    # Layer norm on the GRU input. Without it the model sat on a loss plateau for ~150 steps and learned the word
    # boundary last, so letters were right but words merged and the validation WER stayed near 1 at 300 steps.
    # Frame-local, so the streaming decode still equals the offline one. Checkpoints written before it load without.
    input_norm: bool = True


class TinyCTC(nn.Module):
    def __init__(self, cfg: ModelConfig | None = None) -> None:
        super().__init__()
        self.cfg = cfg or ModelConfig()
        self.proj = nn.Linear(self.cfg.input_dim, self.cfg.hidden)
        self.norm: nn.Module = nn.LayerNorm(self.cfg.hidden) if self.cfg.input_norm else nn.Identity()
        self.gru = nn.GRU(self.cfg.hidden, self.cfg.hidden, batch_first=True)
        self.head = nn.Linear(self.cfg.hidden, self.cfg.vocab)

    def forward(self, x: torch.Tensor, h: torch.Tensor | None = None) -> tuple[torch.Tensor, torch.Tensor]:
        """x: (batch, frames, input_dim) → log-probabilities (batch, frames, vocab) and the GRU state."""
        y, h_out = self.gru(self.norm(torch.relu(self.proj(x))), h)
        return torch.log_softmax(self.head(y), dim=-1), h_out


def config_json(model: TinyCTC) -> dict[str, Any]:
    return {"family": "toy-ctc", "model": asdict(model.cfg), "frameMs": FRAME_MS, "features": feature_config()}


def feature_config() -> dict[str, Any]:
    return {"kind": "log-mel", "bins": N_MELS, "nFft": N_FFT, "windowMs": 25, "hopMs": 10, "stack": STACK}


def load_checkpoint(d: Path) -> tuple[TinyCTC, CharTokenizer]:
    import json

    cfg = json.loads((d / "config.json").read_text(encoding="utf-8"))
    tok = json.loads((d / "tokenizer.json").read_text(encoding="utf-8"))
    model = TinyCTC(ModelConfig(**({"input_norm": False} | cfg["model"])))
    model.load_state_dict(torch.load(d / "model.pt", weights_only=True))
    model.eval()
    return model, CharTokenizer(tok["vocab"])


def save_checkpoint(d: Path, model: TinyCTC, tok: CharTokenizer, step: int | None = None) -> None:
    import json

    d.mkdir(parents=True, exist_ok=True)
    torch.save(model.state_dict(), d / "model.pt")
    cfg = config_json(model) | ({"step": step} if step is not None else {})
    (d / "config.json").write_text(json.dumps(cfg, indent=2, sort_keys=True), encoding="utf-8")
    (d / "tokenizer.json").write_text(json.dumps(tok.to_json(), indent=2), encoding="utf-8")


# ---------------------------------------------------------------- decoding


@dataclass
class Emission:
    token: int
    frame: int
    prob: float


class GreedyDecoder:
    """Greedy CTC over frames fed in any number of chunks; words with start, end and confidence from the alignment."""

    def __init__(self, tok: CharTokenizer) -> None:
        self.tok = tok
        self.prev = 0
        self.frame = 0
        self.emitted: list[Emission] = []

    def feed(self, logp: torch.Tensor) -> None:
        probs, ids = logp.exp().max(dim=-1)
        for p, i in zip(probs.tolist(), ids.tolist(), strict=True):
            if i != 0 and i != self.prev:
                self.emitted.append(Emission(int(i), self.frame, float(p)))
            self.prev = int(i)
            self.frame += 1

    def text(self) -> str:
        return " ".join(self.tok.decode([e.token for e in self.emitted]).split())

    def words(self) -> list[dict[str, Any]]:
        words: list[dict[str, Any]] = []
        cur: list[Emission] = []
        for e in [*self.emitted, Emission(1, self.frame, 1.0)]:
            if e.token == 1:  # space
                if cur:
                    words.append(
                        {
                            "word": self.tok.decode([c.token for c in cur]),
                            "start": round(cur[0].frame * FRAME_MS / 1000, 3),
                            "end": round((cur[-1].frame + 1) * FRAME_MS / 1000, 3),
                            "confidence": round(sum(c.prob for c in cur) / len(cur), 4),
                        }
                    )
                cur = []
            else:
                cur.append(e)
        return words


@torch.no_grad()
def decode_offline(model: TinyCTC, tok: CharTokenizer, samples: torch.Tensor) -> GreedyDecoder:
    dec = GreedyDecoder(tok)
    logp, _ = model(features(samples).unsqueeze(0))
    dec.feed(logp[0])
    return dec


@torch.no_grad()
def decode_streaming(
    model: TinyCTC, tok: CharTokenizer, samples: torch.Tensor, chunk_ms: int
) -> tuple[GreedyDecoder, list[dict[str, Any]]]:
    """Feed the audio chunk by chunk (samples of chunk_ms), carrying the GRU state; a partial event per chunk with the
    audio offset it covers, the time it was emitted (ms since the decode started) and the text so far."""
    dec = GreedyDecoder(tok)
    feats = features(samples)
    per_chunk = max(1, chunk_ms // FRAME_MS)
    h: torch.Tensor | None = None
    partials: list[dict[str, Any]] = []
    t0 = time.monotonic()
    for start in range(0, feats.shape[0], per_chunk):
        chunk = feats[start : start + per_chunk]
        logp, h = model(chunk.unsqueeze(0), h)
        dec.feed(logp[0])
        partials.append(
            {
                "audioOffsetMs": (start + chunk.shape[0]) * FRAME_MS,
                "emitMs": round((time.monotonic() - t0) * 1000, 2),
                "text": dec.text(),
            }
        )
    if partials:
        partials[-1]["final"] = True
    return dec, partials


def weights_hash(model_file: Path) -> str:
    from cadence_worker.cas import hash_file

    return hash_file(model_file)
