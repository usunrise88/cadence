"""mount_check@1 — is a mount healthy from this worker host: reachable, free space, a throughput sample.

The control plane queues it when a mount is registered, after every scan, on ``mounts.verify`` and every
``storage.mount_check_hours`` (control-plane/internal/mounts, job kind ``mounts.verify``). It reads the mount through
the lease (``ctx.mounts``) and reports final metrics: ``reachable`` (1), ``free_bytes`` and ``total_bytes`` (path
mounts), ``throughput_mbps`` and ``sampled_bytes`` (a read of up to ``sample_mb`` of the mount's files) and, for a
writable mount, ``writable`` (a probe file written and removed). An unreachable mount fails the step with the reason;
the control plane records the mount as unhealthy and refuses jobs that name it. Nothing is read beyond the sample and
nothing is written but the probe. Runtime-neutral, CPU. Help: docs/help/steps/mount-check.md.
"""

from __future__ import annotations

import json
import os
import re
import time
import uuid
from collections.abc import Iterator, Mapping
from pathlib import Path
from typing import ClassVar

from pydantic import BaseModel

from cadence_worker.mounts import PATH_KINDS, Mount, Mounts
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import cadence_field
from cadence_worker.steps.context import StepContext

# Files sampled for throughput, at most.
MAX_SAMPLE_FILES = 64


class MountCheckParams(BaseModel):
    mount: str = cadence_field(
        "",
        description="The mount to check (its name)",
        source="Cadence recommendation",
        range={"pattern": "^([a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?)?$"},
    )
    uri: str = cadence_field(
        "",
        description="mount://<name>/ — names the mount so the lease carries its credentials",
        source="Cadence recommendation",
        range={"maxLength": 100},
    )
    sample_mb: int = cadence_field(default_ref="storage.mount_check_sample_mb")
    probe_write: bool = cadence_field(
        False,
        description="Write and remove a probe file (writable mounts)",
        source="Cadence recommendation",
        range="any",
    )


class MountUnreachableError(RuntimeError):
    """The mount cannot be read from this worker (the step fails; the mount becomes unhealthy)."""


def _walk(root: Path) -> Iterator[Path]:
    """Regular files under root, breadth-first and in name order, so the sample is stable."""
    dirs = [root]
    while dirs:
        d = dirs.pop(0)
        try:
            entries = sorted(os.scandir(d), key=lambda e: e.name)
        except OSError:
            continue
        for e in entries:
            if e.is_dir(follow_symlinks=False):
                dirs.append(Path(e.path))
            elif e.is_file(follow_symlinks=False):
                yield Path(e.path)


def check_path_mount(m: Mount, sample_bytes: int, probe: bool) -> dict[str, float]:
    root = Path(m.root)
    if not root.is_dir():
        raise MountUnreachableError(
            f"mount {m.name}: {root} is not a directory on this worker (is it bound into the worker?)"
        )
    try:
        st = os.statvfs(root)
    except OSError as e:
        raise MountUnreachableError(f"mount {m.name}: {root}: {e}") from None
    out: dict[str, float] = {
        "reachable": 1.0,
        "free_bytes": float(st.f_bavail * st.f_frsize),
        "total_bytes": float(st.f_blocks * st.f_frsize),
    }
    read, files = 0, 0
    t0 = time.perf_counter()
    for f in _walk(root):
        if read >= sample_bytes or files >= MAX_SAMPLE_FILES:
            break
        try:
            with f.open("rb") as fh:
                while read < sample_bytes and (b := fh.read(1 << 20)):
                    read += len(b)
        except OSError:
            continue
        files += 1
    _throughput(out, read, time.perf_counter() - t0)
    if probe:
        p = root / f".cadence-probe-{uuid.uuid4().hex}"
        try:
            p.write_bytes(b"cadence mount_check probe\n")
            p.unlink()
            out["writable"] = 1.0
        except OSError as e:
            raise MountUnreachableError(
                f"mount {m.name} is registered writable but a probe write failed: {e}"
            ) from None
    return out


def _throughput(out: dict[str, float], read: int, seconds: float) -> None:
    out["sampled_bytes"] = float(read)
    if read > 0 and seconds > 0:
        out["throughput_mbps"] = round(read / seconds / 1e6, 2)


def check_remote_mount(mounts: Mounts, m: Mount, sample_bytes: int) -> dict[str, float]:
    """s3: list a page of objects and read a sample; hf: read the repository's file list at its revision and a
    sample. Free space is not known for either."""
    import httpx

    out: dict[str, float] = {"reachable": 1.0}
    if m.kind == "s3":
        _, _, prefix = m.root.partition("/")
        query = {"list-type": "2", "max-keys": "50"}
        if prefix:
            query["prefix"] = prefix + "/"
        url, headers = mounts.request(m, "", query)
        r = httpx.get(url, headers=headers, timeout=30.0)
        if r.status_code // 100 != 2:
            raise MountUnreachableError(f"mount {m.name}: listing the bucket answered {r.status_code}: {r.text[:300]}")
        keys = [k for k in re.findall(r"<Key>([^<]*)</Key>", r.text) if not k.endswith("/")]
        paths = [k[len(prefix) + 1 :] if prefix else k for k in keys]
    else:
        base = (os.environ.get("HF_ENDPOINT") or "https://huggingface.co").rstrip("/")
        kind = m.root.split("/", 1)[0]
        api = f"{base}/api/{m.root}" if kind in ("datasets", "spaces") else f"{base}/api/models/{m.root}"
        token = mounts.credentials(m)
        auth = {"Authorization": f"Bearer {token}"} if token else {}
        r = httpx.get(f"{api}/tree/{m.revision}?recursive=true", headers=auth, timeout=30.0, follow_redirects=True)
        if r.status_code // 100 != 2:
            raise MountUnreachableError(
                f"mount {m.name}: the Hub answered {r.status_code} at revision {m.revision}: {r.text[:300]}"
            )
        entries = json.loads(r.text)
        paths = [e["path"] for e in entries if isinstance(e, Mapping) and e.get("type") == "file"]
    read = 0
    t0 = time.perf_counter()
    for p in paths[:MAX_SAMPLE_FILES]:
        if read >= sample_bytes:
            break
        url, headers = mounts.request(m, p)
        with httpx.stream("GET", url, headers=headers, timeout=60.0, follow_redirects=True) as resp:
            if resp.status_code // 100 != 2:
                raise MountUnreachableError(f"mount {m.name}: reading {p} answered {resp.status_code}")
            for b in resp.iter_bytes(1 << 20):
                read += len(b)
                if read >= sample_bytes:
                    break
    _throughput(out, read, time.perf_counter() - t0)
    return out


class MountCheckStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = MountCheckParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: StepContext,
    ) -> None:
        p = MountCheckParams.model_validate(params.model_dump())
        m = ctx.mounts.get(p.mount)
        sample = max(1, p.sample_mb) * 1_000_000
        ctx.progress(0.1, f"checking mount {m.name} ({m.kind})")
        if m.kind in PATH_KINDS:
            out = check_path_mount(m, sample, p.probe_write and not m.read_only)
        else:
            out = check_remote_mount(ctx.mounts, m, sample)
        for k, v in out.items():
            ctx.final_metric(k, v)
        ctx.log(f"mount {m.name} is reachable", metrics=out)
        ctx.progress(1.0, "done")
