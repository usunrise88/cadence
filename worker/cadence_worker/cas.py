"""The content store the worker shares with the control plane (docs/review/2026-09-30-phase-2-plan.md "Worker
protocol"; control-plane/internal/cas is the other half and the two must hash and lay out blobs identically).

A blob is addressed by ``b3:<64 hex>``, the BLAKE3-256 of its bytes, and lives at ``<root>/b3/<first 2 hex>/<64 hex>``.
Writers stream into ``<root>/tmp`` and rename, so a blob exists whole or not at all; blobs are mode 0440. A directory
artifact is a manifest ``{"files":[{"path","hash","size"}]}`` (sorted by path, compact JSON exactly like Go's
``json.Marshal``) stored as a blob whose hash is the artifact's hash; each file is its own blob.
"""

from __future__ import annotations

import contextlib
import json
import os
import re
import shutil
import tempfile
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path
from typing import BinaryIO

import blake3

PREFIX = "b3:"
URI_PREFIX = "cas://"
HASH_RE = re.compile(r"^b3:[0-9a-f]{64}$")
CHUNK = 1 << 20
MANIFEST_LIMIT = 64 << 20


class CasError(Exception):
    pass


def valid_hash(h: str) -> bool:
    return bool(HASH_RE.match(h))


def hash_bytes(b: bytes) -> str:
    return PREFIX + blake3.blake3(b).hexdigest()


def hash_file(path: Path) -> str:
    h = blake3.blake3()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(CHUNK), b""):
            h.update(chunk)
    return PREFIX + h.hexdigest()


def parse_uri(uri: str) -> str:
    """``cas://b3:<hash>`` → ``b3:<hash>``."""
    h = uri.removeprefix(URI_PREFIX)
    if not uri.startswith(URI_PREFIX) or not valid_hash(h):
        raise CasError(f"{uri!r} is not a cas://b3:<hash> URI")
    return h


@dataclass(frozen=True)
class ManifestFile:
    path: str
    hash: str
    size: int


def check_rel(p: str) -> None:
    if not p or p.startswith("/") or "\\" in p:
        raise CasError(f"manifest path {p!r} must be relative and slash-separated")
    if any(seg in ("", ".", "..") for seg in p.split("/")):
        raise CasError(f"manifest path {p!r} has an empty, . or .. segment")


def encode_manifest(files: list[ManifestFile]) -> bytes:
    """Canonical manifest bytes: sorted by path (byte order, like Go's string comparison), compact JSON with Go's
    field order and escaping of <, > and & as Go's json.Marshal does."""
    for f in files:
        check_rel(f.path)
        if not valid_hash(f.hash):
            raise CasError(f"manifest file {f.path!r}: {f.hash!r} is not a b3 hash")
    ordered = sorted(files, key=lambda f: f.path.encode())
    doc = {"files": [{"path": f.path, "hash": f.hash, "size": f.size} for f in ordered]}
    text = json.dumps(doc, separators=(",", ":"), ensure_ascii=False)
    # Go's encoder escapes HTML-significant characters and the two JavaScript line terminators.
    for ch, esc in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        text = text.replace(ch, esc)
    return text.encode()


def decode_manifest(b: bytes) -> list[ManifestFile]:
    doc = json.loads(b)
    if not isinstance(doc, dict) or set(doc) != {"files"} or not isinstance(doc["files"], list):
        raise CasError("not a manifest")
    out: list[ManifestFile] = []
    for item in doc["files"]:
        if not isinstance(item, dict) or set(item) != {"path", "hash", "size"}:
            raise CasError("not a manifest")
        path, h, size = item["path"], item["hash"], item["size"]
        if not isinstance(path, str) or not isinstance(h, str) or not isinstance(size, int) or not valid_hash(h):
            raise CasError("not a manifest")
        check_rel(path)
        out.append(ManifestFile(path, h, size))
    return out


@dataclass(frozen=True)
class Stored:
    """What storing a file or directory produced: the artifact's hash, its size in bytes (for a directory the sum of
    its files) and whether it is a directory manifest."""

    hash: str
    size: int
    directory: bool


class Store:
    def __init__(self, root: Path) -> None:
        self.root = root
        for d in (root / "b3", root / "tmp"):
            d.mkdir(parents=True, exist_ok=True)

    def path(self, h: str) -> Path:
        if not valid_hash(h):
            raise CasError(f"{h!r} is not a b3 hash")
        hx = h.removeprefix(PREFIX)
        return self.root / "b3" / hx[:2] / hx

    def has(self, h: str) -> bool:
        return self.path(h).is_file()

    @contextlib.contextmanager
    def _tmp(self) -> Iterator[tuple[BinaryIO, Path]]:
        fd, name = tempfile.mkstemp(prefix="put-", dir=self.root / "tmp")
        tmp = Path(name)
        try:
            with os.fdopen(fd, "wb") as f:
                yield f, tmp
        finally:
            tmp.unlink(missing_ok=True)

    def _commit(self, tmp: Path, h: str) -> None:
        dst = self.path(h)
        if dst.exists():
            return
        dst.parent.mkdir(parents=True, exist_ok=True)
        os.chmod(tmp, 0o440)
        os.replace(tmp, dst)

    def put_stream(self, src: BinaryIO, want: str = "") -> tuple[str, int]:
        hasher = blake3.blake3()
        n = 0
        with self._tmp() as (f, tmp):
            for chunk in iter(lambda: src.read(CHUNK), b""):
                hasher.update(chunk)
                f.write(chunk)
                n += len(chunk)
            f.close()
            got = PREFIX + hasher.hexdigest()
            if want and got != want:
                raise CasError(f"content does not match its hash: got {got}, want {want}")
            self._commit(tmp, got)
        return got, n

    def put_bytes(self, b: bytes) -> str:
        with self._tmp() as (f, tmp):
            f.write(b)
            f.close()
            h = hash_bytes(b)
            self._commit(tmp, h)
        return h

    def put_file(self, path: Path) -> tuple[str, int]:
        with path.open("rb") as src:
            return self.put_stream(src)

    def put_dir(self, root: Path) -> Stored:
        """Store every regular file under root and the manifest; symlinks are refused (a step must write real files)."""
        files: list[ManifestFile] = []
        total = 0
        for p in sorted(root.rglob("*")):
            if p.is_symlink():
                raise CasError(f"{p}: symbolic links cannot be stored")
            if not p.is_file():
                continue
            h, n = self.put_file(p)
            files.append(ManifestFile(p.relative_to(root).as_posix(), h, n))
            total += n
        return Stored(self.put_bytes(encode_manifest(files)), total, True)

    def put_path(self, p: Path) -> Stored:
        if p.is_dir():
            return self.put_dir(p)
        h, n = self.put_file(p)
        return Stored(h, n, False)

    def read_manifest(self, h: str) -> list[ManifestFile]:
        p = self.path(h)
        if p.stat().st_size > MANIFEST_LIMIT:
            raise CasError(f"{h} is not a manifest")
        try:
            return decode_manifest(p.read_bytes())
        except (ValueError, UnicodeDecodeError) as e:
            raise CasError(f"{h} is not a manifest") from e

    def is_manifest(self, h: str) -> bool:
        """Whether blob h is a directory manifest (a blob that parses as exactly the manifest shape and whose files are
        all present). Used when an input's metadata does not say (``meta.layout``)."""
        try:
            files = self.read_manifest(h)
        except (CasError, OSError):
            return False
        return all(self.has(f.hash) for f in files)

    def materialise(self, h: str, dst: Path, *, directory: bool | None = None) -> Path:
        """Place artifact h at dst: a file, or for a manifest a directory tree. Hard links when the scratch directory is
        on the store's file system, copies otherwise. ``directory`` None means detect."""
        if not self.has(h):
            raise CasError(f"blob not found: {h}")
        if directory is None:
            directory = self.is_manifest(h)
        dst.parent.mkdir(parents=True, exist_ok=True)
        if not directory:
            link_or_copy(self.path(h), dst)
            return dst
        dst.mkdir(parents=True, exist_ok=True)
        for f in self.read_manifest(h):
            target = dst.joinpath(*f.path.split("/"))
            target.parent.mkdir(parents=True, exist_ok=True)
            link_or_copy(self.path(f.hash), target)
        return dst


def link_or_copy(src: Path, dst: Path) -> None:
    try:
        os.link(src, dst)
    except OSError:
        shutil.copyfile(src, dst)
