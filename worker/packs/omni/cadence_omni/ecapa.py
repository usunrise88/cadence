"""SpeechBrain's VoxLingua107 ECAPA-TDNN language classifier (``auxiliary/lid-voxlingua107``, engine
``speechbrain-ecapa``) for ``lid_classify@2``. speechbrain, torch and torchaudio come from the omni runtime image and
are imported lazily, so the dev environment tests the pure parts without them.

VoxLingua107 labels its classes ``<code>: <Name>`` with the YouTube language codes of 2021: Hebrew is ``iw`` and
Javanese ``jw``. They are written as BCP 47 primary subtags (``he``, ``jv``), the codes Whisper's detection and the
sources' locales use, so the pseudo-label ensemble compares like with like.
"""

from __future__ import annotations

from collections.abc import Sequence
from pathlib import Path
from typing import Any

import numpy as np

from cadence_omni import hub
from cadence_worker.steps.base import StepInputError

# Weights and hyperparameters of the classifier (no audio examples, no training logs).
ALLOW = ["*.ckpt", "*.yaml", "*.txt", "*.md"]

# VoxLingua107's legacy codes → BCP 47 primary subtags.
LEGACY = {"iw": "he", "jw": "jv", "in": "id", "ji": "yi", "mo": "ro"}


def language_of(label: str) -> str:
    """The BCP 47 primary subtag of a VoxLingua107 label (``iw: Hebrew`` → ``he``)."""
    code = label.split(":", 1)[0].strip().lower()
    return LEGACY.get(code, code)


def rank(logp: Sequence[float], codes: Sequence[str], k: int) -> list[tuple[str, float]]:
    """The k most probable languages with their probabilities (softmax of the log-posteriors), most probable first,
    ties by code."""
    x = np.asarray(logp, dtype=np.float64)
    e = np.exp(x - x.max())
    p = e / e.sum()
    order = sorted(range(len(codes)), key=lambda i: (-p[i], codes[i]))
    return [(codes[i], float(p[i])) for i in order[:k]]


def snapshot(repo: str, revision: str) -> Path:
    """The classifier at its pinned revision (a cache, or downloaded once into HF_HOME)."""
    return hub.snapshot(repo, revision, ALLOW)


class Ecapa:
    """The classifier on a device; :meth:`classify` ranks a batch of 16 kHz mono clips."""

    def __init__(self, path: Path, work: Path, device: str) -> None:
        try:
            from speechbrain.inference.classifiers import EncoderClassifier
        except ImportError as e:
            raise StepInputError(
                "this runtime has no speechbrain; lid_classify@2 runs in the omni runtime (worker/Dockerfile.omni)"
            ) from e
        # The hyperparameters name the Hub repository as pretrained_path, which speechbrain would fetch at its newest
        # revision: point it at the pinned snapshot, so the step stays pinned and runs offline.
        clf = EncoderClassifier.from_hparams(
            source=str(path),
            savedir=str(work / "speechbrain"),
            run_opts={"device": device},
            overrides={"pretrained_path": str(path)},
        )
        if clf is None:
            raise StepInputError(f"{path}: speechbrain loaded no classifier from these hyperparameters")
        self.clf: Any = clf
        enc = clf.hparams.label_encoder
        self.codes = [language_of(str(enc.decode_ndim(i))) for i in range(len(enc))]

    def classify(self, clips: Sequence[np.ndarray[Any, np.dtype[np.float32]]], k: int) -> list[list[tuple[str, float]]]:
        import torch

        n = max(len(c) for c in clips)
        wavs = torch.zeros(len(clips), n)
        for i, c in enumerate(clips):
            wavs[i, : len(c)] = torch.from_numpy(np.ascontiguousarray(c, dtype=np.float32))
        lens = torch.tensor([len(c) / n for c in clips])
        with torch.inference_mode():
            logp = self.clf.classify_batch(wavs, lens)[0].float().cpu().numpy()
        return [rank(row.tolist(), self.codes, k) for row in logp]
