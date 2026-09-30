"""Reading a ``dataset`` artifact: JSON lines whose utterance lines carry ``audio`` (a b3 hash of a WAV in the content
store), ``text`` and optionally ``split``; other lines (the header with source and splits) are skipped."""

from __future__ import annotations

import json
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

import torch

from cadence_toy.model import CharTokenizer, read_wav
from cadence_worker.steps.base import StepInputError


@dataclass
class Utterance:
    audio: str
    text: str
    split: str
    samples: torch.Tensor


def read_dataset(path: Path, blob: Callable[[str], Path]) -> list[Utterance]:
    out: list[Utterance] = []
    for n, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"dataset line {n} is not JSON") from e
        if not isinstance(row, dict) or "audio" not in row:
            continue
        if not isinstance(row.get("text"), str):
            raise StepInputError(f"dataset line {n} has no text")
        try:
            samples = read_wav(blob(str(row["audio"])))
        except (OSError, ValueError) as e:
            raise StepInputError(f"dataset line {n}: {e}") from e
        out.append(Utterance(str(row["audio"]), row["text"], str(row.get("split") or "train"), samples))
    if not out:
        raise StepInputError("the dataset has no utterances")
    return out


def splits(utts: list[Utterance]) -> tuple[list[Utterance], list[Utterance]]:
    """Train and validation utterances; the train set doubles as validation when none is marked (fixtures)."""
    train = [u for u in utts if u.split == "train"] or utts
    val = [u for u in utts if u.split in ("validation", "dev", "val")] or train
    return train, val


def batch(
    utts: list[Utterance], tok: CharTokenizer, feats: Callable[[torch.Tensor], torch.Tensor]
) -> tuple[torch.Tensor, torch.Tensor, torch.Tensor, torch.Tensor]:
    """Padded features, their lengths, concatenated targets and target lengths for CTC."""
    xs = [feats(u.samples) for u in utts]
    ys = [torch.tensor(tok.encode(u.text), dtype=torch.long) for u in utts]
    x = torch.nn.utils.rnn.pad_sequence(xs, batch_first=True)
    x_len = torch.tensor([t.shape[0] for t in xs], dtype=torch.long)
    return x, x_len, torch.cat(ys), torch.tensor([t.shape[0] for t in ys], dtype=torch.long)
