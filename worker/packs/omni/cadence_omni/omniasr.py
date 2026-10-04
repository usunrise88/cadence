"""omniASR CTC models (facebookresearch/omnilingual-asr, Apache-2.0) loaded with fairseq2 0.6 from a pinned Hugging
Face snapshot (the auxiliary's ``hfRepo`` at ``revision``: ``omniASR-CTC-<size>.pt`` and ``omniASR_tokenizer.model``).

The omnilingual-asr package is not installed: it is a fairseq2 extension whose import pulls its training stack
(pandas, polars, kenlm, …) for nothing an aligner needs. What it contributes to these models is configuration — the
wav2vec2 encoder sizes and the CTC vocabulary — repeated in :data:`ARCHS` from its ``wav2vec2_ssl`` and
``wav2vec2_asr`` configs (version 0.2.0, commit 81f51e2 of 2026-09): fairseq2's ``base_10h`` CTC head over its
``large_lv60k`` encoder, resized. The tokenizer is a character SentencePiece model (fairseq2's ``char_tokenizer``);
the blank is index 0 (wav2vec2 CTC convention) and words are separated by a boundary token, found by encoding two
words. The waveform is layer-normalised before the forward pass, as omniASR's own pipeline does. Everything here
imports torch and fairseq2 lazily: only the omni runtime image has them.
"""

from __future__ import annotations

import contextlib
import itertools
import warnings
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from cadence_worker import ctc_align
from cadence_worker.steps.base import StepInputError

BLANK = 0
TOKENIZER_FILE = "omniASR_tokenizer.model"


@dataclass(frozen=True)
class Arch:
    model_dim: int
    layers: int
    ffn_inner_dim: int
    vocab: int


# Checkpoint file → the architecture omnilingual-asr registers for it (its v1 CTC cards; the Hub repos carry v1).
ARCHS: dict[str, Arch] = {
    "omniASR-CTC-300M.pt": Arch(model_dim=1024, layers=24, ffn_inner_dim=4096, vocab=9812),
    "omniASR-CTC-1B.pt": Arch(model_dim=1280, layers=48, ffn_inner_dim=5120, vocab=9812),
    "omniASR-CTC-3B.pt": Arch(model_dim=2048, layers=60, ffn_inner_dim=8192, vocab=9812),
}


def device() -> str:
    import torch

    return "cuda" if torch.cuda.is_available() else "cpu"


def snapshot(repo: str, revision: str) -> Path:
    """The auxiliary's weights: the Hub snapshot at its pinned revision (downloaded once into HF_HOME)."""
    from huggingface_hub import snapshot_download

    try:
        return Path(snapshot_download(repo, revision=revision, allow_patterns=["*.pt", "*.model", "*.md"]))
    except Exception as e:  # network, auth or a missing revision: the step cannot run
        raise StepInputError(f"cannot fetch {repo}@{revision}: {e}") from e


def checkpoint_in(snap: Path) -> tuple[Path, Arch]:
    found = [(snap / name, arch) for name, arch in ARCHS.items() if (snap / name).is_file()]
    if len(found) != 1 or not (snap / TOKENIZER_FILE).is_file():
        raise StepInputError(
            f"{snap.name} is not an omniASR CTC snapshot: it needs one of {', '.join(ARCHS)} and {TOKENIZER_FILE}"
        )
    return found[0]


def _init_fairseq2() -> None:
    """fairseq2's one-time initialisation (it refuses a second call in one process)."""
    from fairseq2 import init_fairseq2
    from fairseq2.error import InvalidOperationError

    with contextlib.suppress(InvalidOperationError):
        init_fairseq2()


class OmniCtc:
    """An omniASR CTC model on one device: :class:`cadence_omni.aligner.Emissions`."""

    blank = BLANK

    def __init__(self, snap: Path, device: str, dtype: str) -> None:
        import torch

        _init_fairseq2()
        from fairseq2.data.tokenizers.char import load_char_tokenizer
        from fairseq2.models.wav2vec2 import get_wav2vec2_model_hub
        from fairseq2.models.wav2vec2.asr import get_wav2vec2_asr_model_hub

        path, arch = checkpoint_in(snap)
        hub = get_wav2vec2_asr_model_hub()
        cfg = hub.get_arch_config("base_10h")
        enc = get_wav2vec2_model_hub().get_arch_config("large_lv60k").encoder_config
        enc.model_dim, enc.num_encoder_layers, enc.ffn_inner_dim = arch.model_dim, arch.layers, arch.ffn_inner_dim
        enc.dropout_p = enc.attn_dropout_p = enc.layer_drop_p = 0.0
        cfg.encoder_config = enc
        cfg.use_masking = False
        cfg.max_temporal_mask_prob = cfg.max_spatial_mask_prob = 0.0
        cfg.target_vocab_size = arch.vocab
        self._torch = torch
        self._device = torch.device(device)
        self._dtype = getattr(torch, dtype)
        self.model = hub.load_custom_model(path, cfg, device=self._device, dtype=self._dtype, progress=False)
        self.model.eval()
        tokenizer = load_char_tokenizer(snap / TOKENIZER_FILE, None)
        self._encoder = tokenizer.create_encoder()
        info = tokenizer.vocab_info
        self._special = {i for i in (info.pad_idx, info.bos_idx, info.eos_idx) if i is not None}
        self.separator = self._separator()

    def _ids(self, text: str) -> list[int]:
        return [int(i) for i in self._encoder(text).tolist()]

    def _separator(self) -> int | None:
        one, two = self._ids("a"), self._ids("a a")
        if len(two) == 2 * len(one) + 1 and two[: len(one)] == one:
            return two[len(one)]
        return None

    def encode(self, text: str) -> list[int]:
        return [i for i in self._ids(text) if i not in self._special and i != self.separator]

    def log_probs(self, samples: np.ndarray[Any, np.dtype[np.float32]]) -> np.ndarray[Any, np.dtype[np.float32]]:
        torch = self._torch
        from fairseq2.nn.batch_layout import BatchLayout

        x = np.asarray(samples, dtype=np.float32)
        # The waveform is layer-normalised first, as omniASR's inference pipeline does (normalize_audio): without it
        # a quiet recording decodes to noise.
        x = (x - x.mean()) / np.sqrt(x.var() + 1e-5)
        with torch.inference_mode():
            seqs = torch.from_numpy(np.ascontiguousarray(x, dtype=np.float32))[None].to(self._device, self._dtype)
            logits, _ = self.model(seqs, BatchLayout(seqs.shape, seq_lens=[seqs.shape[1]], device=self._device))
            lp = torch.log_softmax(logits[0].float(), dim=-1)
        out: np.ndarray[Any, np.dtype[np.float32]] = lp.cpu().numpy()
        return out


def torchaudio_forced(
    log_probs: np.ndarray[Any, Any], targets: Sequence[int], blank: int
) -> np.ndarray[Any, Any] | None:
    """torchaudio.functional.forced_align (on the card when there is one), as token positions per frame."""
    import torch
    import torchaudio.functional as taf

    frames = int(log_probs.shape[0])
    needed = len(targets) + sum(1 for a, b in itertools.pairwise(targets) if a == b)
    if frames < needed:
        return None
    device = "cuda" if torch.cuda.is_available() else "cpu"
    lp = torch.from_numpy(np.ascontiguousarray(log_probs, dtype=np.float32))[None].to(device)
    tg = torch.tensor([list(targets)], dtype=torch.int32, device=device)
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", UserWarning)  # 2.8 announces the function's removal in 2.9 on every call
        labels, _ = taf.forced_align(lp, tg, blank=blank)
    return ctc_align.positions_from_labels(labels[0].cpu().tolist(), blank)


def forced_aligner() -> tuple[str, Any]:
    """torchaudio's forced_align when the runtime has it (2.8 does; 2.9 removed it), else the NumPy Viterbi."""
    try:
        import torchaudio.functional as taf

        if hasattr(taf, "forced_align"):
            return "torchaudio.forced_align", torchaudio_forced
    except ImportError:
        pass
    return "ctc-viterbi", ctc_align.viterbi
