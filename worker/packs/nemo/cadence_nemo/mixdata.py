"""Training and evaluation data as the NeMo steps read it.

- A ``mix`` artifact (``cadence.mix/1``, rendered by the control plane from a mix revision: control-plane/internal/runs
  README "Prepare / Create") is one JSON file: ``input_cfg`` groups with a sampling ``probability`` (temperature
  applied), each listing dataset versions with the BLAKE3 hash of their ``dataset`` artifact and hours.
- A ``dataset`` artifact (docs/help/steps/dataset-import.md) is a directory manifest: ``dataset.json``,
  ``manifest.jsonl`` (audio path inside the artifact, duration, language, text, split) and the audio files.

The harness materialises the mix file only; the datasets it names are read from the content store through
``ctx.blob`` (read-only, never copied): their manifests are decoded and every clip's audio is the blob path itself.
A ``data`` input may also be a dataset directory (one dataset, weight 1).
"""

from __future__ import annotations

import json
from collections.abc import Callable, Iterable
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath
from typing import Any

from cadence_worker.cas import CasError, decode_manifest, valid_hash
from cadence_worker.steps.base import StepInputError

MIX_FORMAT = "cadence.mix/1"
DATASET_FORMAT = "cadence.dataset/1"
TRAIN = "train"
VALIDATION = ("validation", "dev", "val")

BlobPath = Callable[[str], Path]


@dataclass(frozen=True)
class Clip:
    audio: Path  # readable audio file (a blob in the store, or a file inside a materialised dataset)
    duration: float
    text: str
    language: str
    split: str
    dataset: str  # dataset version id (or name) the clip came from


@dataclass
class DatasetPart:
    ref: str  # dataset version id, or the dataset's name for a bare dataset input
    name: str
    hours: float
    clips: list[Clip] = field(default_factory=list)


@dataclass
class Group:
    name: str
    probability: float
    replay: bool
    datasets: list[DatasetPart] = field(default_factory=list)


@dataclass
class TrainingData:
    mix: dict[str, Any]  # the mix reference (id, name, revision) or {} for a bare dataset
    groups: list[Group]

    def clips(self) -> list[Clip]:
        return [c for g in self.groups for d in g.datasets for c in d.clips]

    def train_clips(self) -> list[Clip]:
        return [c for c in self.clips() if c.split == TRAIN]

    def validation_clips(self) -> list[Clip]:
        return [c for c in self.clips() if c.split in VALIDATION]

    def languages(self) -> set[str]:
        return {c.language for c in self.clips()}

    def dataset_ids(self) -> list[str]:
        return [d.ref for g in self.groups for d in g.datasets]


def _inside(rel: str, n: int) -> PurePosixPath:
    p = PurePosixPath(rel)
    if not p.parts or p.is_absolute() or ".." in p.parts:
        raise StepInputError(f"dataset manifest line {n}: audio {rel!r} is not a path inside the artifact")
    return p


def _parse_manifest(text: str, resolve: Callable[[str], Path], dataset: str) -> list[Clip]:
    clips: list[Clip] = []
    for n, line in enumerate(text.splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"dataset {dataset}: manifest line {n} is not JSON") from e
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str) or not isinstance(row.get("text"), str):
            raise StepInputError(f"dataset {dataset}: manifest line {n} lacks audio or text")
        rel = _inside(row["audio"], n).as_posix()
        clips.append(
            Clip(
                audio=resolve(rel),
                duration=float(row.get("duration") or 0.0),
                text=row["text"],
                language=str(row.get("language") or ""),
                split=str(row.get("split") or TRAIN),
                dataset=dataset,
            )
        )
    return clips


def _check_header(raw: bytes | str, dataset: str) -> dict[str, Any]:
    try:
        header = json.loads(raw)
    except ValueError as e:
        raise StepInputError(f"dataset {dataset}: dataset.json is not JSON") from e
    if not isinstance(header, dict) or header.get("format") != DATASET_FORMAT:
        raise StepInputError(f"dataset {dataset}: dataset.json is not {DATASET_FORMAT}")
    return header


def read_dataset_dir(root: Path, ref: str = "") -> DatasetPart:
    """A materialised ``dataset`` directory artifact."""
    if not (root / "dataset.json").is_file() or not (root / "manifest.jsonl").is_file():
        raise StepInputError(f"{root.name}: not a dataset artifact (dataset.json and manifest.jsonl)")
    header = _check_header((root / "dataset.json").read_bytes(), ref or root.name)
    name = str(header.get("name") or ref or root.name)
    clips = _parse_manifest(
        (root / "manifest.jsonl").read_text(encoding="utf-8"), lambda rel: root.joinpath(*rel.split("/")), ref or name
    )
    return DatasetPart(ref=ref or name, name=name, hours=float(header.get("hours") or 0.0), clips=clips)


def read_dataset_blob(h: str, blob: BlobPath, ref: str) -> DatasetPart:
    """A ``dataset`` artifact by its manifest hash, read from the content store without copying."""
    if not valid_hash(h):
        raise StepInputError(f"dataset {ref}: artifact {h!r} is not a b3 hash")
    try:
        files = {f.path: f.hash for f in decode_manifest(blob(h).read_bytes())}
    except (OSError, CasError, ValueError) as e:
        raise StepInputError(f"dataset {ref}: cannot read its artifact {h} from the content store: {e}") from e
    if "dataset.json" not in files or "manifest.jsonl" not in files:
        raise StepInputError(f"dataset {ref}: artifact {h} lacks dataset.json or manifest.jsonl")
    header = _check_header(blob(files["dataset.json"]).read_bytes(), ref)

    def resolve(rel: str) -> Path:
        if rel not in files:
            raise StepInputError(f"dataset {ref}: audio {rel} is not in the artifact")
        return blob(files[rel])

    clips = _parse_manifest(blob(files["manifest.jsonl"]).read_text(encoding="utf-8"), resolve, ref)
    return DatasetPart(
        ref=ref, name=str(header.get("name") or ref), hours=float(header.get("hours") or 0.0), clips=clips
    )


def read_training_data(path: Path, blob: BlobPath) -> TrainingData:
    """The ``data`` input of a calibrate or train step: a mix file, or a dataset directory."""
    if path.is_dir():
        part = read_dataset_dir(path)
        return TrainingData(mix={}, groups=[Group(name=part.name, probability=1.0, replay=False, datasets=[part])])
    try:
        doc = json.loads(path.read_bytes())
    except (OSError, ValueError) as e:
        raise StepInputError(f"the data input is neither a dataset directory nor a mix file: {e}") from e
    if not isinstance(doc, dict) or doc.get("format") != MIX_FORMAT:
        raise StepInputError(f"the data input is not a {MIX_FORMAT} mix artifact")
    groups: list[Group] = []
    for g in doc.get("input_cfg") or []:
        group = Group(
            name=str(g.get("name") or f"group{len(groups)}"),
            probability=float(g.get("probability") or g.get("weight") or 0.0),
            replay=bool(g.get("replay")),
        )
        for d in g.get("input_cfg") or []:
            ref = str(d.get("dataset") or d.get("name") or "")
            part = read_dataset_blob(str(d.get("artifact") or ""), blob, ref)
            part.hours = float(d.get("hours") or part.hours)
            group.datasets.append(part)
        if group.datasets:
            groups.append(group)
    if not groups:
        raise StepInputError("the mix names no dataset")
    total = sum(g.probability for g in groups)
    for g in groups:  # a mix without shares samples groups uniformly
        g.probability = g.probability / total if total > 0 else 1.0 / len(groups)
    mix_ref = doc.get("mix")
    return TrainingData(mix=dict(mix_ref) if isinstance(mix_ref, dict) else {}, groups=groups)


def nemo_rows(clips: Iterable[Clip], text: Callable[[Clip], str], lang: Callable[[Clip], str]) -> list[dict[str, Any]]:
    """NeMo manifest lines (``lang_field: target_lang`` is what the prompt model's Lhotse reader maps to the
    supervision language)."""
    return [
        {
            "audio_filepath": str(c.audio),
            "duration": round(c.duration, 3),
            "text": text(c),
            "lang": lang(c),
            "target_lang": lang(c),
        }
        for c in clips
    ]


def write_jsonl(path: Path, rows: Iterable[dict[str, Any]]) -> int:
    path.parent.mkdir(parents=True, exist_ok=True)
    n = 0
    with path.open("w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
            n += 1
    return n


def input_cfg(
    data: TrainingData,
    manifest_dir: Path,
    text: Callable[[Clip], str],
    lang: Callable[[Clip], str],
    keep: Callable[[Clip], bool],
) -> list[dict[str, Any]]:
    """The Lhotse ``input_cfg`` of the training set: one ``group`` per mix group weighted by its sampling probability,
    and inside it one NeMo manifest per dataset weighted by its hours (utterances when hours are unknown). Clips that
    ``keep`` refuses (other splits, out-of-range durations) are left out; empty datasets and groups are dropped."""
    out: list[dict[str, Any]] = []
    for gi, g in enumerate(data.groups):
        items: list[dict[str, Any]] = []
        for di, d in enumerate(g.datasets):
            clips = [c for c in d.clips if keep(c)]
            if not clips:
                continue
            path = manifest_dir / f"g{gi}_d{di}.json"
            write_jsonl(path, nemo_rows(clips, text, lang))
            weight = d.hours if d.hours > 0 else float(len(clips))
            items.append({"type": "nemo", "manifest_filepath": str(path), "weight": weight})
        if items:
            out.append({"type": "group", "weight": g.probability, "input_cfg": items})
    if not out:
        raise StepInputError("no training clips left in the mix (split train, within the duration limits)")
    return out
