from __future__ import annotations

import datetime
import json
from pathlib import Path
from typing import Any

import pytest

from cadence_worker.mounts import MOUNTS_ENV, Mount, MountError, Mounts, MountURI, parse_uri, sign_v4
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.mount_check import MountCheckParams, MountCheckStep, MountUnreachableError


@pytest.mark.parametrize(
    ("uri", "want"),
    [
        ("mount://corpora/fleurs-sr/2024/a.wav", MountURI("corpora", "fleurs-sr/2024/a.wav")),
        ("mount://corpora/calls/x.wav#t=1.5,3.25&ch=1", MountURI("corpora", "calls/x.wav", 1.5, 3.25, 1)),
        ("mount://c/x.wav#ch=0", MountURI("c", "x.wav", channel=0)),
        ("mount://c/x.wav#t=0,2", MountURI("c", "x.wav", 0.0, 2.0)),
        ("mount://c/дир/файл 1.wav", MountURI("c", "дир/файл 1.wav")),
    ],
)
def test_parse_uri_and_canonical_form(uri: str, want: MountURI) -> None:
    got = parse_uri(uri)
    assert got == want
    assert str(got) == uri
    assert parse_uri(str(got)) == got


@pytest.mark.parametrize(
    "bad",
    [
        "file:///x.wav",
        "mount://Corpora/x.wav",
        "mount://corpora",
        "mount://corpora/",
        "mount://corpora//x.wav",
        "mount://corpora/../x.wav",
        "mount://corpora/a/./x.wav",
        "mount://corpora/x.wav#",
        "mount://corpora/x.wav#t=2,1",
        "mount://corpora/x.wav#t=-1,1",
        "mount://corpora/x.wav#t=1",
        "mount://corpora/x.wav#t=nan,1",
        "mount://corpora/x.wav#ch=64",
        "mount://corpora/x.wav#ch=-1",
        "mount://corpora/x.wav#t=0,1&t=0,2",
        "mount://corpora/x.wav#q=1",
    ],
)
def test_parse_uri_refuses(bad: str) -> None:
    with pytest.raises(MountError):
        parse_uri(bad)


def test_resolve_a_path_mount(tmp_path: Path) -> None:
    (tmp_path / "src" / "rev").mkdir(parents=True)
    (tmp_path / "src" / "rev" / "a.wav").write_bytes(b"RIFF")
    mounts = Mounts([{"name": "corpora", "kind": "local", "root": str(tmp_path), "readOnly": True}])
    assert mounts.resolve("mount://corpora/src/rev/a.wav#t=0,1") == tmp_path / "src" / "rev" / "a.wav"
    with pytest.raises(MountError, match="no file"):
        mounts.resolve("mount://corpora/src/rev/missing.wav")
    with pytest.raises(MountError, match="no mount named 'other'"):
        mounts.resolve("mount://other/a.wav")
    with pytest.raises(MountError, match="read-only"):
        mounts.writable_path("mount://corpora/out.tar")
    assert b"".join(mounts.stream("mount://corpora/src/rev/a.wav")) == b"RIFF"


def test_from_env_round_trips_the_lease(tmp_path: Path) -> None:
    m = Mount("exports", "nfs", str(tmp_path), read_only=False)
    env = {MOUNTS_ENV: json.dumps([m.to_lease()]), "CADENCE_MOUNT_CACHE": str(tmp_path / "cache")}
    mounts = Mounts.from_env(env)
    assert list(mounts) == [m]
    assert mounts.writable_path("mount://exports/a/b.tar") == tmp_path / "a" / "b.tar"


def test_s3_request_needs_credentials_and_signs() -> None:
    m = Mount("bucket", "s3", "audio/calls", endpoint="http://minio:9000", region="us-east-1", credentials_env="CRED")
    with pytest.raises(MountError, match="no credentials"):
        Mounts([m], env={}).request(m, "a b.wav")
    url, headers = Mounts([m], env={"CRED": "AK:SK"}).request(m, "a b.wav")
    assert url == "http://minio:9000/audio/calls/a%20b.wav"
    assert headers["Authorization"].startswith("AWS4-HMAC-SHA256 Credential=AK/")
    assert "SignedHeaders=host;x-amz-content-sha256;x-amz-date" in headers["Authorization"]


def test_sign_v4_is_deterministic_and_query_sorted() -> None:
    t = datetime.datetime(2026, 10, 3, 12, 0, 0, tzinfo=datetime.UTC)
    url, h = sign_v4("https://s3.example", "/b", {"prefix": "x/", "list-type": "2"}, "AK", "SK", "eu-west-1", t)
    url2, h2 = sign_v4("https://s3.example", "/b", {"list-type": "2", "prefix": "x/"}, "AK", "SK", "eu-west-1", t)
    assert url == url2 == "https://s3.example/b?list-type=2&prefix=x%2F"
    assert h == h2
    assert h["x-amz-date"] == "20261003T120000Z"
    assert "Credential=AK/20261003/eu-west-1/s3/aws4_request" in h["Authorization"]


def test_hf_request_resolves_at_the_revision() -> None:
    rev = "a" * 40
    m = Mount("fleurs", "hf", "datasets/google/fleurs", revision=rev, credentials_env="T")
    url, headers = Mounts([m], env={"T": "hf_x", "HF_ENDPOINT": "https://hub.example"}).request(m, "data/x.tar")
    assert url == f"https://hub.example/datasets/google/fleurs/resolve/{rev}/data/x.tar"
    assert headers == {"Authorization": "Bearer hf_x"}


def run_check(tmp_path: Path, mount: dict[str, Any], **params: Any) -> tuple[dict[str, float], list[dict[str, Any]]]:
    events: list[dict[str, Any]] = []
    ctx = StepContext(events.append, work_dir=tmp_path / "work", mounts=Mounts([mount], env={}))
    p = MountCheckParams(mount=mount["name"], uri=f"mount://{mount['name']}/", **params)
    MountCheckStep().run(p, {}, {}, ctx)
    return ctx.final_metrics, events


def test_mount_check_reports_space_and_throughput(tmp_path: Path) -> None:
    root = tmp_path / "corpora"
    (root / "src").mkdir(parents=True)
    (root / "src" / "a.wav").write_bytes(b"x" * 300_000)
    metrics, _ = run_check(
        tmp_path, {"name": "corpora", "kind": "local", "root": str(root), "readOnly": True}, sample_mb=1
    )
    assert metrics["reachable"] == 1
    assert metrics["total_bytes"] > 0
    assert metrics["free_bytes"] >= 0
    assert metrics["sampled_bytes"] == 300_000
    assert "writable" not in metrics


def test_mount_check_probes_a_writable_mount(tmp_path: Path) -> None:
    root = tmp_path / "exports"
    root.mkdir()
    metrics, _ = run_check(
        tmp_path, {"name": "exports", "kind": "local", "root": str(root), "readOnly": False}, probe_write=True
    )
    assert metrics["writable"] == 1
    assert list(root.iterdir()) == []  # the probe is gone


def test_mount_check_fails_on_an_unreachable_mount(tmp_path: Path) -> None:
    with pytest.raises(MountUnreachableError, match="not a directory"):
        run_check(tmp_path, {"name": "gone", "kind": "nfs", "root": str(tmp_path / "nope"), "readOnly": True})
