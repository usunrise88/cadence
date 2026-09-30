from __future__ import annotations

import os
import stat
from pathlib import Path

import pytest

from cadence_worker import cas
from cadence_worker.cas import CasError, ManifestFile, Store, encode_manifest, hash_bytes, parse_uri

EMPTY = "b3:af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
HELLO = "b3:ea8f163db38682925e4491c5e58d4bb3506ef8c14eb78a86e908c5624a67200f"
X = "b3:3ae7d805f6789a6402acb70ad4096a85a56bf6804eaf25c0493ac697548d30b5"
# control-plane/internal/cas Manifest.Encode of the three files below (computed with the Go package).
GO_MANIFEST = (
    '{"files":[{"path":"A","hash":"' + X + '","size":1},'
    '{"path":"a/é.bin","hash":"' + EMPTY + '","size":0},'
    '{"path":"b/x\\u003cy\\u0026z\\u003e.txt","hash":"' + HELLO + '","size":5}]}'
)
GO_MANIFEST_HASH = "b3:ffdadcfc3d6cbdb7ba1485f18f5e71395f7f388e70048e1d8ef767afd0ca5ed1"


def test_hash_matches_the_go_store() -> None:
    assert hash_bytes(b"") == EMPTY
    assert hash_bytes(b"hello") == HELLO


def test_manifest_encoding_is_byte_identical_to_go() -> None:
    files = [
        ManifestFile("b/x<y&z>.txt", HELLO, 5),
        ManifestFile("a/é.bin", EMPTY, 0),
        ManifestFile("A", X, 1),
    ]
    b = encode_manifest(files)
    assert b.decode() == GO_MANIFEST
    assert hash_bytes(b) == GO_MANIFEST_HASH
    assert cas.decode_manifest(b) == sorted(files, key=lambda f: f.path)


@pytest.mark.parametrize("bad", ["", "/abs", "a/../b", "a//b", "a\\b", "./a"])
def test_manifest_paths_must_be_relative(bad: str) -> None:
    with pytest.raises(CasError):
        encode_manifest([ManifestFile(bad, HELLO, 5)])


def test_put_is_atomic_read_only_and_idempotent(tmp_path: Path) -> None:
    store = Store(tmp_path)
    src = tmp_path / "f"
    src.write_bytes(b"hello")
    h, n = store.put_file(src)
    assert (h, n) == (HELLO, 5)
    p = store.path(h)
    assert p == tmp_path / "b3" / HELLO[3:5] / HELLO[3:]
    assert stat.S_IMODE(p.stat().st_mode) == 0o440
    assert store.put_bytes(b"hello") == HELLO
    assert list((tmp_path / "tmp").iterdir()) == []
    with pytest.raises(CasError, match="does not match"), src.open("rb") as f:
        store.put_stream(f, want=EMPTY)
    assert list((tmp_path / "tmp").iterdir()) == []


def test_directory_round_trip_by_hard_links(tmp_path: Path) -> None:
    store = Store(tmp_path / "cas")
    d = tmp_path / "ckpt"
    (d / "sub").mkdir(parents=True)
    (d / "model.pt").write_bytes(b"weights")
    (d / "sub" / "config.json").write_bytes(b"{}")
    stored = store.put_path(d)
    assert stored.directory
    assert stored.size == 9
    assert store.is_manifest(stored.hash)
    assert not store.is_manifest(hash_bytes(b"weights"))
    out = store.materialise(stored.hash, tmp_path / "scratch" / "in" / "ckpt")
    assert (out / "model.pt").read_bytes() == b"weights"
    assert (out / "sub" / "config.json").read_bytes() == b"{}"
    assert os.stat(out / "model.pt").st_ino == store.path(hash_bytes(b"weights")).stat().st_ino


def test_materialise_copies_when_links_fail(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    store = Store(tmp_path / "cas")
    h = store.put_bytes(b"hello")

    def no_link(src: object, dst: object) -> None:
        raise OSError(18, "Invalid cross-device link")

    monkeypatch.setattr("cadence_worker.cas.os.link", no_link)
    out = store.materialise(h, tmp_path / "x" / "f", directory=False)
    assert out.read_bytes() == b"hello"
    assert out.stat().st_ino != store.path(h).stat().st_ino


def test_symlinks_are_refused(tmp_path: Path) -> None:
    d = tmp_path / "d"
    d.mkdir()
    (d / "real").write_text("x")
    (d / "link").symlink_to(d / "real")
    with pytest.raises(CasError, match="symbolic"):
        Store(tmp_path / "cas").put_dir(d)


def test_uri() -> None:
    assert parse_uri("cas://" + HELLO) == HELLO
    for bad in (HELLO, "cas://b3:xyz", "s3://" + HELLO):
        with pytest.raises(CasError):
            parse_uri(bad)
