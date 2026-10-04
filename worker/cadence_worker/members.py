"""What the pseudo-label members and the LID step share (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for
phase 4" 6-7): the utterances of a ``dataset`` input whether or not they have text, their audio as 16 kHz mono, the
language tags they compare, the ``hypotheses`` rows a member writes and the ``lid`` artifact.

A member reads the segments as a dataset (materialised segments, or the dataset a dry-run freeze cuts from them) and
writes one hypotheses row per utterance keyed by the BLAKE3 hash of its audio file, which is the segment's hash:

    {audio, text, member, language, detectedLanguage?, languageConfidence?, confidence?, vote?, model, decodingHash}

``member`` labels the member in the ensemble (the auxiliary's collection name); ``vote`` marks a member that is itself
an ensemble, whose text the ensemble prefers when it agrees (plan decision 6). The ``lid`` artifact is JSON lines
``{audio, language, confidence, top: [[language, p], …], model}``.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Iterable, Mapping
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

import numpy as np

from cadence_worker import audio as audio_io
from cadence_worker.cas import hash_file
from cadence_worker.steps.base import StepInputError

SR = 16000
DATASET_FORMAT = "cadence.dataset/1"


@dataclass(frozen=True)
class Utterance:
    path: Path
    hash: str  # b3 of the audio file: the segment's identity
    language: str
    duration: float
    text: str = ""


def read_utterances(root: Path) -> list[Utterance]:
    """The utterances of a materialised ``dataset`` directory in manifest order; text may be empty or absent (the
    segments a member labels have none)."""
    if not root.is_dir() or not (root / "manifest.jsonl").is_file():
        raise StepInputError("the data input is not a dataset artifact (dataset.json, manifest.jsonl, audio/)")
    try:
        header = json.loads((root / "dataset.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the dataset has no readable dataset.json: {e}") from e
    if not isinstance(header, dict) or header.get("format") != DATASET_FORMAT:
        raise StepInputError(f"dataset.json is not {DATASET_FORMAT}")
    out: list[Utterance] = []
    for n, line in enumerate((root / "manifest.jsonl").read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"dataset manifest line {n} is not JSON") from e
        rel = row.get("audio") if isinstance(row, dict) else None
        if not isinstance(rel, str):
            raise StepInputError(f"dataset manifest line {n} lacks audio")
        p = PurePosixPath(rel)
        if not p.parts or p.is_absolute() or ".." in p.parts:
            raise StepInputError(f"dataset manifest line {n}: audio {rel!r} is not a path inside the artifact")
        f = root.joinpath(*p.parts)
        if not f.is_file():
            raise StepInputError(f"dataset manifest line {n}: audio {rel} is not in the artifact")
        text = row.get("text")
        out.append(
            Utterance(
                path=f,
                hash=hash_file(f),
                language=str(row.get("language") or ""),
                duration=float(row.get("duration") or 0.0),
                text=text if isinstance(text, str) else "",
            )
        )
    if not out:
        raise StepInputError("the dataset has no utterances")
    return out


def samples_16k(path: Path) -> np.ndarray[Any, np.dtype[np.float32]]:
    """An utterance as 16 kHz mono float32 in [-1, 1]."""
    try:
        a = audio_io.canonical(audio_io.read(path), SR)
    except (audio_io.AudioError, OSError) as e:
        raise StepInputError(f"cannot read {path.name}: {e}") from e
    return np.asarray(a.samples, dtype=np.float32)


def pcm16_16k(path: Path) -> bytes:
    """An utterance as 16 kHz mono PCM16 little-endian (the canonical segment the hash covers)."""
    x = samples_16k(path).astype(np.float64)
    return bytes(np.clip(np.round(x * 32767), -32768, 32767).astype("<i2").tobytes())


def primary(tag: str) -> str:
    """The primary subtag of a BCP-47 tag, lower case ("sr-Latn-RS" → "sr"); "" stays ""."""
    return tag.split("-", 1)[0].split("_", 1)[0].strip().lower()


# Individual languages folded to their macrolanguage before comparing with a model's or an auxiliary's languages: the
# same table as the control plane's internal/langtag (BCP 47 / ISO 639-3 macrolanguage mappings), kept equal by hand.
MACROLANGUAGES = {"nb": "no", "nn": "no", "zsm": "ms", "zlm": "ms", "arb": "ar", "cmn": "zh", "pes": "fa", "swh": "sw",
                  "ekk": "et", "lvs": "lv"}


def macro(tag: str) -> str:
    """The primary subtag folded to its macrolanguage ("nb-NO" → "no"); a language that is no member is its own."""
    p = primary(tag)
    return MACROLANGUAGES.get(p, p)


def same_language(a: str, b: str, equivalents: Iterable[Iterable[str]] = ()) -> bool:
    """Whether two tags name the same language: equal primary subtags, or both in one equivalence group."""
    pa, pb = primary(a), primary(b)
    if not pa or not pb:
        return False
    if pa == pb:
        return True
    return any(pa in g and pb in g for g in ({primary(x) for x in grp} for grp in equivalents))


def decoding_hash(decoding: Mapping[str, Any]) -> str:
    return "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()


def write_jsonl(path: Path, rows: Iterable[Mapping[str, Any]]) -> int:
    n = 0
    with path.open("w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
            n += 1
    return n


def read_jsonl_by_audio(path: Path, what: str) -> dict[str, dict[str, Any]]:
    """Rows of a JSON-lines artifact keyed by ``audio`` (a hypotheses or lid artifact)."""
    out: dict[str, dict[str, Any]] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"cannot read the {what}: {e}") from e
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"{what} line {n} is not JSON") from e
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str):
            raise StepInputError(f"{what} line {n} lacks audio")
        if row["audio"] in out:
            raise StepInputError(f"{what} line {n}: audio {row['audio']} appears twice")
        out[row["audio"]] = row
    return out


def member_label(ref: Mapping[str, Any]) -> str:
    """A member's label: the auxiliary collection's short name (auxiliary/whisper-large-v3 → whisper-large-v3)."""
    name = str(ref.get("name") or "")
    return name.split("/", 1)[1] if "/" in name else name
