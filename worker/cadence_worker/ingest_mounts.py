"""Mount URIs as the ingest steps use them: walking a directory of a path-kind mount (local, nfs, smb).

``cadence_worker.mounts`` (the lease's mounts, ``ctx.mounts``) owns the URI form and file resolution; this module
adds what ingest needs on top: a URI may name a directory (or the mount's root) to walk, a local path maps back to its
URI, and URIs are written in the canonical form ``mounts.MountURI`` prints (the same string Go's ``URI.String()``
gives), with times rounded to microseconds so the same cut always prints the same URI.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from cadence_worker.mounts import NAME, Mounts, MountURI, parse_uri
from cadence_worker.mounts import SCHEME as SCHEME
from cadence_worker.steps.base import StepInputError


@dataclass(frozen=True)
class MountRef:
    name: str
    path: str  # relative to the mount's root, "/"-separated, "" for the root itself
    start: float | None = None
    end: float | None = None
    channel: int | None = None

    @property
    def file_uri(self) -> str:
        return format_uri(self.name, self.path)


def format_uri(
    name: str, path: str, start: float | None = None, end: float | None = None, channel: int | None = None
) -> str:
    if start is not None and end is not None:
        start, end = round(start, 6), round(end, 6)
    return str(MountURI(name, path.strip("/"), start, end, channel))


def parse(uri: str) -> MountRef:
    """A mount URI as ingest reads it: ``mounts.parse_uri`` (the one parser of the URI form), and also the mount's root
    (``mount://<name>`` or ``mount://<name>/``) or a directory with a trailing ``/``."""
    if not uri.startswith(SCHEME):
        raise StepInputError(f"{uri!r} is not a mount URI (mount://<name>/<path>)")
    rest, sep, frag = uri[len(SCHEME) :].partition("#")
    name, _, path = rest.partition("/")
    path = path.rstrip("/")
    if not path:
        if not NAME.match(name):
            raise StepInputError(f"{uri!r}: mount name {name!r} is not a mount name")
        if sep:
            raise StepInputError(f"{uri!r}: the mount's root takes no fragment")
        return MountRef(name=name, path="")
    u = parse_uri(f"{SCHEME}{name}/{path}{sep}{frag}")
    return MountRef(name=u.mount, path=u.path, start=u.start, end=u.end, channel=u.channel)


def mounts_of(ctx: Any = None) -> list[Mapping[str, Any]]:
    """The lease's mounts: the context's ``mounts`` (``cadence_worker.mounts.Mounts``), else ``CADENCE_MOUNTS``."""
    found = getattr(ctx, "mounts", None)
    ms = found if isinstance(found, Mounts) and len(found) else Mounts.from_env()
    return [m.to_lease() for m in ms]


def resolve(uri: str, mounts: Sequence[Mapping[str, Any]]) -> Path:
    """The local path of a mount URI (fragment ignored); it must stay inside the mount's root."""
    ref = parse(uri)
    for m in mounts:
        if m.get("name") == ref.name:
            root = Path(str(m.get("root", ""))).resolve()
            p = (root / ref.path).resolve() if ref.path else root
            if p != root and root not in p.parents:
                raise StepInputError(f"{uri!r} leaves the mount {ref.name} (a link out of its root)")
            return p
    raise StepInputError(f"no mount {ref.name!r} on this worker (mounts: {sorted(str(m.get('name')) for m in mounts)})")


def relative(path: Path, mounts: Sequence[Mapping[str, Any]], name: str) -> str:
    """The URI path of a local path inside mount name."""
    for m in mounts:
        if m.get("name") == name:
            root = Path(str(m.get("root", ""))).resolve()
            return path.resolve().relative_to(root).as_posix()
    raise StepInputError(f"no mount {name!r}")
