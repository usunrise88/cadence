"""Reading a ``dataset`` artifact: the directory ``dataset_import`` writes (docs/spec/02-domain-projects-registry.md
"The dataset artifact") — ``dataset.json`` (the header), ``manifest.jsonl`` with one line per utterance (``audio``, a
path inside the artifact; ``text``; ``split``) and the audio files. An utterance is named by the BLAKE3 hash of its
audio file, the content hash the registry gives it."""

from __future__ import annotations

import json
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

import torch

from cadence_toy.model import CharTokenizer, read_wav
from cadence_worker.cas import hash_file
from cadence_worker.steps.base import StepInputError

FORMAT = "cadence.dataset/1"


@dataclass
class Utterance:
    audio: str  # b3 hash of the audio file
    text: str
    split: str
    samples: torch.Tensor


def _inside(root: Path, rel: str, n: int) -> Path:
    p = PurePosixPath(rel)
    if not p.parts or p.is_absolute() or ".." in p.parts:
        raise StepInputError(f"dataset manifest line {n}: audio {rel!r} is not a path inside the artifact")
    return root.joinpath(*p.parts)


def read_dataset(root: Path) -> list[Utterance]:
    if not root.is_dir():
        raise StepInputError("the dataset input is not a directory artifact (dataset.json, manifest.jsonl, audio/)")
    try:
        header = json.loads((root / "dataset.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the dataset has no readable dataset.json: {e}") from e
    if not isinstance(header, dict) or header.get("format") != FORMAT:
        raise StepInputError(f"dataset.json is not {FORMAT}")
    manifest = root / "manifest.jsonl"
    if not manifest.is_file():
        raise StepInputError("the dataset has no manifest.jsonl")
    out: list[Utterance] = []
    for n, line in enumerate(manifest.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"dataset manifest line {n} is not JSON") from e
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str):
            raise StepInputError(f"dataset manifest line {n} has no audio path")
        if not isinstance(row.get("text"), str):
            raise StepInputError(f"dataset manifest line {n} has no text")
        path = _inside(root, row["audio"], n)
        try:
            samples = read_wav(path)
            h = hash_file(path)
        except (OSError, ValueError) as e:
            raise StepInputError(f"dataset manifest line {n}: {e}") from e
        out.append(Utterance(h, row["text"], str(row.get("split") or "train"), samples))
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
