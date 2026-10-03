"""Mount URIs for the ingest steps — a temporary, minimal resolver.

Stream M (phase 4) owns ``mount://`` URIs, the lease's ``mounts`` field and ``cadence_worker.mounts``; this module
codes against the interface the phase-4 plan fixes ("Interfaces between streams", M → D) and is deleted when M's
module lands (the steps then import ``cadence_worker.mounts``):

- the one URI form ``mount://<name>/<path>[#t=<start>,<end>][&ch=<n>]``;
- the worker learns mount roots from its lease: ``mounts: [{name, kind, root, readOnly}]``. Until the harness passes
  them, a step reads them from the context's ``mounts`` attribute when it has one, else from ``CADENCE_MOUNTS`` (the
  same list as JSON).
"""

from __future__ import annotations

import json
import os
import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

from cadence_worker.steps.base import StepInputError

SCHEME = "mount://"
ENV = "CADENCE_MOUNTS"
NAME = re.compile(r"^[a-z0-9][a-z0-9._-]{0,62}$")


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


def seconds(x: float) -> str:
    """A time in a URI fragment: up to six decimals, trailing zeros dropped (deterministic)."""
    s = format(x, ".6f").rstrip("0").rstrip(".")
    return s or "0"


def format_uri(
    name: str, path: str, start: float | None = None, end: float | None = None, channel: int | None = None
) -> str:
    uri = SCHEME + name + "/" + path.strip("/")
    frag: list[str] = []
    if start is not None and end is not None:
        frag.append(f"t={seconds(start)},{seconds(end)}")
    if channel is not None:
        frag.append(f"ch={channel}")
    return uri + ("#" + "&".join(frag) if frag else "")


def parse(uri: str) -> MountRef:
    if not uri.startswith(SCHEME):
        raise StepInputError(f"{uri!r} is not a mount URI (mount://<name>/<path>)")
    rest, _, frag = uri[len(SCHEME) :].partition("#")
    name, _, path = rest.partition("/")
    if not NAME.match(name):
        raise StepInputError(f"{uri!r}: mount name {name!r} is not a mount name")
    parts = PurePosixPath(path).parts if path else ()
    if any(p in ("..", ".") for p in parts) or path.startswith("/"):
        raise StepInputError(f"{uri!r}: the path must stay inside the mount")
    start = end = None
    channel = None
    for item in filter(None, frag.split("&")):
        key, _, val = item.partition("=")
        try:
            if key == "t":
                a, _, b = val.partition(",")
                start, end = float(a), float(b)
            elif key == "ch":
                channel = int(val)
        except ValueError as e:
            raise StepInputError(f"{uri!r}: bad fragment {item!r}") from e
    return MountRef(name=name, path="/".join(parts), start=start, end=end, channel=channel)


def mounts_of(ctx: Any = None) -> list[Mapping[str, Any]]:
    """The lease's mounts: the context's ``mounts`` when it has them, else ``CADENCE_MOUNTS``."""
    found = getattr(ctx, "mounts", None)
    if found:
        return list(found)
    raw = os.environ.get(ENV, "")
    if not raw:
        return []
    try:
        doc = json.loads(raw)
    except ValueError as e:
        raise StepInputError(f"{ENV} is not JSON: {e}") from e
    if not isinstance(doc, list):
        raise StepInputError(f"{ENV} must be a list of {{name, kind, root, readOnly}}")
    return [m for m in doc if isinstance(m, dict)]


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
