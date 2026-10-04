"""What the export step kinds share (``shar_export``, ``dataset_export``, ``hf_push``; phase 4 · stream I).

An export reads a ``dataset`` artifact (``cadence.dataset/1``: ``dataset.json``, ``manifest.jsonl`` and the audio
files) and writes files to a **target**: ``cas`` (the files go into the step's ``export`` output under ``files/``) or
a directory on a writable path mount, ``mount://<mount>/<dir>`` (the files go there, written atomically, and the
output holds only the manifest). Either way the output is an ``export`` directory artifact with ``export.json``:

    {format: cadence.export/1, exportFormat, target, version?, utterances,
     files: [{path, hash, bytes}, …] (relative to the target), hub?: {repo, commit, url, private}}

The control plane's ``export`` output hook records the export, and every listed file whose hash is a blob of the
exported dataset artifact (audio copied unchanged) as a copy of that blob on the mount — so the cache may evict it.
"""

from __future__ import annotations

import json
import os
import tempfile
from collections.abc import Iterator, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from cadence_worker.cas import hash_bytes, hash_file
from cadence_worker.mounts import Mounts, MountURI, parse_uri
from cadence_worker.steps.base import StepInputError

MANIFEST = "export.json"
FORMAT = "cadence.export/1"
DATASET_FORMAT = "cadence.dataset/1"
CAS = "cas"
FILES_DIR = "files"
SPLITS = ("train", "validation", "test")


@dataclass
class Dataset:
    """A dataset artifact as an export reads it: the header, its lines and where the artifact lives."""

    root: Path
    header: dict[str, Any]
    lines: list[dict[str, Any]]

    def audio(self, line: Mapping[str, Any]) -> Path:
        return self.root / str(line["audio"])

    def by_split(self) -> Iterator[tuple[str, list[dict[str, Any]]]]:
        for s in SPLITS:
            part = [x for x in self.lines if x.get("split") == s]
            if part:
                yield s, part


def read_dataset(root: Path) -> Dataset:
    """Read a frozen dataset artifact (cadence.dataset/1) from its materialised directory."""
    if not (root / "dataset.json").is_file() or not (root / "manifest.jsonl").is_file():
        raise StepInputError("the input is not a dataset artifact (dataset.json, manifest.jsonl and the audio)")
    try:
        header = json.loads((root / "dataset.json").read_text(encoding="utf-8"))
    except ValueError as e:
        raise StepInputError(f"dataset.json is not JSON: {e}") from e
    if not isinstance(header, dict) or header.get("format") != DATASET_FORMAT:
        raise StepInputError(f"the dataset is not {DATASET_FORMAT} (a draft is frozen with datasets.freeze first)")
    lines: list[dict[str, Any]] = []
    for n, raw in enumerate((root / "manifest.jsonl").read_text(encoding="utf-8").splitlines(), 1):
        if not raw.strip():
            continue
        x = json.loads(raw)
        if not isinstance(x, dict) or not x.get("audio"):
            raise StepInputError(f"manifest.jsonl line {n} has no audio")
        if not (root / str(x["audio"])).is_file():
            raise StepInputError(f"manifest.jsonl line {n}: {x['audio']} is not a file of the dataset")
        lines.append(x)
    if not lines:
        raise StepInputError("the dataset has no utterances")
    return Dataset(root=root, header=header, lines=lines)


def mounts_of(ctx: Any) -> Mounts:
    found = getattr(ctx, "mounts", None)
    return found if isinstance(found, Mounts) and len(found) else Mounts.from_env()


@dataclass
class Writer:
    """Writes an export's files to its target and lists them for export.json."""

    target: str
    base: Path
    files: list[dict[str, Any]] = field(default_factory=list)

    def write_bytes(self, rel: str, body: bytes) -> None:
        dst = self._dst(rel)
        _atomic(dst, lambda f: f.write(body))
        self.files.append({"path": rel, "hash": hash_bytes(body), "bytes": len(body)})

    def copy(self, rel: str, src: Path) -> None:
        dst = self._dst(rel)

        def write(f: Any) -> None:
            with src.open("rb") as r:
                while chunk := r.read(1 << 20):
                    f.write(chunk)

        _atomic(dst, write)
        self.files.append({"path": rel, "hash": hash_file(dst), "bytes": dst.stat().st_size})

    def _dst(self, rel: str) -> Path:
        parts = rel.split("/")
        if not rel or rel.startswith("/") or any(p in ("", ".", "..") for p in parts):
            raise StepInputError(f"export path {rel!r} must stay inside the target")
        return self.base.joinpath(*parts)


def _atomic(dst: Path, write: Any) -> None:
    dst.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=dst.parent, prefix=".part-")
    try:
        with os.fdopen(fd, "wb") as f:
            write(f)
        os.replace(tmp, dst)
    except BaseException:
        Path(tmp).unlink(missing_ok=True)
        raise


def writer(target: str, out: Path, ctx: Any) -> Writer:
    """A writer for target: cas writes into the output under files/; a mount URI names a directory on a writable
    path mount of the lease."""
    out.mkdir(parents=True, exist_ok=True)
    if target == CAS:
        return Writer(target=CAS, base=out / FILES_DIR)
    if not target.startswith("mount://"):
        raise StepInputError(f"target {target!r} is neither cas nor a mount URI (mount://<mount>/<directory>)")
    u = parse_uri(target)
    if u.segment is not None or u.channel is not None:
        raise StepInputError(f"target {target!r} is a directory; it takes no #t or ch fragment")
    base = mounts_of(ctx).writable_path(u)
    if base.exists() and not base.is_dir():
        raise StepInputError(f"target {target!r} is a file on the mount, not a directory")
    base.mkdir(parents=True, exist_ok=True)
    return Writer(target=str(MountURI(u.mount, u.path)), base=base)


def finish(
    out: Path,
    export_format: str,
    w: Writer | None,
    *,
    target: str = "",
    version: str = "",
    utterances: int = 0,
    hub: Mapping[str, Any] | None = None,
    files: Sequence[Mapping[str, Any]] | None = None,
) -> dict[str, Any]:
    """Write export.json into the output directory and return it."""
    out.mkdir(parents=True, exist_ok=True)
    doc: dict[str, Any] = {
        "format": FORMAT,
        "exportFormat": export_format,
        "target": w.target if w is not None else target,
        "utterances": utterances,
        "files": list(w.files if w is not None else (files or [])),
    }
    if version:
        doc["version"] = version
    if hub:
        doc["hub"] = dict(hub)
    (out / MANIFEST).write_text(json.dumps(doc, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
    return doc


def report(ctx: Any, fraction: float, message: str) -> None:
    fn = getattr(ctx, "progress", None)
    if callable(fn):
        fn(fraction, message)
