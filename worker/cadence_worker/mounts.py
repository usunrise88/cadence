"""Mount URIs on the worker: ``mount://<mount>/<path>[#t=<start>,<end>][&ch=<n>]`` → a local path.

The one URI form of audio that lives on a mount (docs/review/2026-10-03-phase-4-plan.md "Interfaces between streams",
M → D). The path is relative to the mount's root, UTF-8 and never percent-encoded, without empty, ``.`` or ``..``
segments and without ``#``; ``t`` is a segment in seconds and ``ch`` a 0-based channel. The control plane parses the
same form (control-plane/internal/mounts/uri.go).

A lease carries every registered mount (``lease["mounts"]``: name, kind, root, readOnly and, for s3 and hf, endpoint,
region, revision and the env variable holding the credentials when the step may read them). The harness hands them to
the step: ``ctx.mounts.resolve(uri)`` inside a step, or ``Mounts.from_env()`` in a helper process (``CADENCE_MOUNTS``).

- ``local``, ``nfs``, ``smb``: the OS mounted the share at ``root`` on the worker host (compose binds it): the path is
  ``root/<path>``, read in place.
- ``s3``: an S3-compatible bucket (``root`` = bucket[/prefix]); the object is downloaded once into the mount cache.
- ``hf``: a Hugging Face Hub repository at the pinned revision; the file is downloaded once into the mount cache.

``resolve`` returns the whole file; the fragment (segment and channel) stays on the parsed URI for the caller to cut.
"""

from __future__ import annotations

import datetime
import hashlib
import hmac
import json
import math
import os
import re
import tempfile
import urllib.parse
from collections.abc import Iterable, Iterator, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from cadence_worker.steps.base import StepInputError

SCHEME = "mount://"
MOUNTS_ENV = "CADENCE_MOUNTS"
CACHE_ENV = "CADENCE_MOUNT_CACHE"
NAME = re.compile(r"^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
PATH_KINDS = ("local", "nfs", "smb")


class MountError(StepInputError):
    """A URI that does not parse, names no mount, or cannot be read (error type ``input``)."""


@dataclass(frozen=True)
class MountURI:
    mount: str
    path: str
    start: float | None = None
    end: float | None = None
    channel: int | None = None

    def __str__(self) -> str:
        s = f"{SCHEME}{self.mount}/{self.path}"
        sep = "#"
        if self.start is not None and self.end is not None:
            s += f"{sep}t={_num(self.start)},{_num(self.end)}"
            sep = "&"
        if self.channel is not None:
            s += f"{sep}ch={self.channel}"
        return s

    @property
    def segment(self) -> tuple[float, float] | None:
        return (self.start, self.end) if self.start is not None and self.end is not None else None


def _num(x: float) -> str:
    """Shortest form, like Go's strconv.FormatFloat(x, 'f', -1, 64): 1.5 → "1.5", 2.0 → "2"."""
    r = repr(float(x))
    if "e" in r or "E" in r:
        r = format(x, "f").rstrip("0").rstrip(".")
    return r[:-2] if r.endswith(".0") else r


def check_path(p: str) -> None:
    if not p:
        raise MountError("the path is empty")
    if p.startswith("/"):
        raise MountError("the path is relative to the mount's root (no leading /)")
    if any(c in p for c in "\x00#\\"):
        raise MountError("the path may not contain NUL, # or \\")
    if any(seg in ("", ".", "..") for seg in p.split("/")):
        raise MountError("the path may not contain empty, . or .. segments")


def parse_uri(s: str) -> MountURI:
    """Parse a mount URI strictly (MountError when it is malformed)."""
    if not s.startswith(SCHEME):
        raise MountError(f"{s!r} is not a mount URI (mount://<mount>/<path>)")
    rest, _, frag = s[len(SCHEME) :].partition("#")
    name, slash, path = rest.partition("/")
    if not slash or not NAME.match(name):
        raise MountError(f"{s!r}: the mount name must match {NAME.pattern} and be followed by /<path>")
    try:
        check_path(path)
    except MountError as e:
        raise MountError(f"{s!r}: {e}") from None
    if "#" in s and not frag:
        raise MountError(f"{s!r}: empty fragment")
    start: float | None = None
    end: float | None = None
    channel: int | None = None
    for part in frag.split("&") if frag else []:
        k, _, v = part.partition("=")
        if k == "t":
            if start is not None:
                raise MountError(f"{s!r}: t given twice")
            a, comma, b = v.partition(",")
            try:
                start, end = float(a), float(b)
            except ValueError:
                start = end = math.nan
            if not comma or not (math.isfinite(start) and math.isfinite(end)) or start < 0 or end <= start:
                raise MountError(f"{s!r}: t must be <start>,<end> in seconds with 0 <= start < end")
        elif k == "ch":
            if channel is not None:
                raise MountError(f"{s!r}: ch given twice")
            if not v.isdigit() or int(v) > 63:
                raise MountError(f"{s!r}: ch must be a channel index 0-63")
            channel = int(v)
        else:
            raise MountError(f"{s!r}: unknown fragment key {k!r} (t, ch)")
    return MountURI(name, path, start, end, channel)


@dataclass(frozen=True)
class Mount:
    name: str
    kind: str
    root: str
    read_only: bool = True
    endpoint: str = ""
    region: str = ""
    revision: str = ""
    credentials_env: str = ""

    @classmethod
    def from_lease(cls, m: Mapping[str, Any]) -> Mount:
        return cls(
            name=str(m["name"]),
            kind=str(m["kind"]),
            root=str(m["root"]),
            read_only=bool(m.get("readOnly", True)),
            endpoint=str(m.get("endpoint") or ""),
            region=str(m.get("region") or ""),
            revision=str(m.get("revision") or ""),
            credentials_env=str(m.get("credentialsEnv") or ""),
        )

    def to_lease(self) -> dict[str, Any]:
        out: dict[str, Any] = {"name": self.name, "kind": self.kind, "root": self.root, "readOnly": self.read_only}
        for k, v in (
            ("endpoint", self.endpoint),
            ("region", self.region),
            ("revision", self.revision),
            ("credentialsEnv", self.credentials_env),
        ):
            if v:
                out[k] = v
        return out


class Mounts:
    """The mounts of one lease, resolving URIs to local paths."""

    def __init__(
        self,
        mounts: Iterable[Mount | Mapping[str, Any]] = (),
        *,
        env: Mapping[str, str] | None = None,
        cache_dir: Path | None = None,
    ) -> None:
        self._mounts: dict[str, Mount] = {}
        for m in mounts:
            mm = m if isinstance(m, Mount) else Mount.from_lease(m)
            self._mounts[mm.name] = mm
        self._env = dict(os.environ if env is None else env)
        self._cache_dir = cache_dir

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> Mounts:
        """The mounts the harness passed to this process (``CADENCE_MOUNTS``, JSON); downloads go to
        ``CADENCE_MOUNT_CACHE``."""
        e = os.environ if env is None else env
        raw = e.get(MOUNTS_ENV) or "[]"
        cache = e.get(CACHE_ENV)
        return cls(json.loads(raw), env=e, cache_dir=Path(cache) if cache else None)

    def __iter__(self) -> Iterator[Mount]:
        return iter(sorted(self._mounts.values(), key=lambda m: m.name))

    def __len__(self) -> int:
        return len(self._mounts)

    def to_json(self) -> str:
        return json.dumps([m.to_lease() for m in self])

    def get(self, name: str) -> Mount:
        m = self._mounts.get(name)
        if m is None:
            known = ", ".join(sorted(self._mounts)) or "none"
            raise MountError(f"no mount named {name!r} in this lease (registered: {known}; mounts.list)")
        return m

    def local_path(self, uri: str | MountURI) -> Path:
        """Where the file of a path-kind mount (local, nfs, smb) lives, without checking it exists."""
        u = parse_uri(uri) if isinstance(uri, str) else uri
        m = self.get(u.mount)
        if m.kind not in PATH_KINDS:
            raise MountError(f"mount {m.name} is {m.kind}: it has no local path; use resolve()")
        return Path(m.root) / u.path

    def writable_path(self, uri: str | MountURI) -> Path:
        """The local path to write a file to on a writable path-kind mount (exports)."""
        u = parse_uri(uri) if isinstance(uri, str) else uri
        m = self.get(u.mount)
        if m.read_only:
            raise MountError(f"mount {m.name} is read-only")
        return self.local_path(u)

    def resolve(self, uri: str | MountURI) -> Path:
        """A local path holding the URI's whole file (the fragment is ignored): in place on a path-kind mount,
        downloaded once into the mount cache for s3 and hf."""
        u = parse_uri(uri) if isinstance(uri, str) else uri
        m = self.get(u.mount)
        if m.kind in PATH_KINDS:
            p = Path(m.root) / u.path
            if not p.is_file():
                raise MountError(f"{u}: no file at {p} (is the mount bound into this worker?)")
            return p
        dst = self._cache_root() / m.name / (m.revision or "_") / u.path
        if dst.is_file():
            return dst
        dst.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(dir=dst.parent, prefix=".part-")
        try:
            with os.fdopen(fd, "wb") as out:
                for chunk in self.stream(u):
                    out.write(chunk)
            os.replace(tmp, dst)
        except BaseException:
            Path(tmp).unlink(missing_ok=True)
            raise
        return dst

    def stream(self, uri: str | MountURI, chunk: int = 1 << 20) -> Iterator[bytes]:
        """The URI's whole file as chunks, read where it lives."""
        u = parse_uri(uri) if isinstance(uri, str) else uri
        m = self.get(u.mount)
        if m.kind in PATH_KINDS:
            with self.resolve(u).open("rb") as f:
                while b := f.read(chunk):
                    yield b
            return
        import httpx  # the worker's own dependency; imported late so parsing needs nothing

        url, headers = self.request(m, u.path)
        with httpx.stream("GET", url, headers=headers, timeout=60.0, follow_redirects=True) as r:
            if r.status_code // 100 != 2:
                raise MountError(f"{u}: {m.kind} answered {r.status_code}: {r.read()[:300]!r}")
            yield from r.iter_bytes(chunk)

    def credentials(self, m: Mount) -> str:
        """The mount's credentials from the lease environment ("" when the step may not read them)."""
        return self._env.get(m.credentials_env, "") if m.credentials_env else ""

    def request(self, m: Mount, path: str, query: Mapping[str, str] | None = None) -> tuple[str, dict[str, str]]:
        """The URL and headers of a GET of ``path`` on an s3 or hf mount (``path`` "" with a query lists s3)."""
        cred = self.credentials(m)
        if m.kind == "hf":
            base = (self._env.get("HF_ENDPOINT") or "https://huggingface.co").rstrip("/")
            # datasets/<org>/<name> and <org>/<model> both resolve under the root as it is written.
            url = f"{base}/{m.root}/resolve/{m.revision}/{urllib.parse.quote(path)}"
            return url, ({"Authorization": f"Bearer {cred}"} if cred else {})
        if m.kind == "s3":
            if ":" not in cred:
                raise MountError(
                    f"mount {m.name}: no credentials in this lease (a step that names the mount, or a data, eval or "
                    "export step, receives them)"
                )
            key, _, secret = cred.partition(":")
            bucket, _, prefix = m.root.partition("/")
            p = f"/{bucket}/{prefix}/{path}" if prefix and path else (f"/{bucket}/{path}" if path else f"/{bucket}")
            return sign_v4(m.endpoint.rstrip("/"), p, dict(query or {}), key, secret, m.region or "us-east-1")
        raise MountError(f"mount {m.name}: kind {m.kind} is read in place")

    def _cache_root(self) -> Path:
        if self._cache_dir is not None:
            return self._cache_dir
        return Path(tempfile.gettempdir()) / "cadence-mounts"


# ---------------------------------------------------------------- S3 Signature Version 4 (body-less GET)

_EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()


def _aws_quote(s: str, keep_slash: bool) -> str:
    return urllib.parse.quote(s, safe="/-_.~" if keep_slash else "-_.~")


def sign_v4(
    endpoint: str,
    path: str,
    query: Mapping[str, str],
    key: str,
    secret: str,
    region: str,
    now: datetime.datetime | None = None,
) -> tuple[str, dict[str, str]]:
    """URL and headers of a signed GET (AWS Signature Version 4, path-style; the control plane's mounts.SignV4)."""
    t = (now or datetime.datetime.now(datetime.UTC)).astimezone(datetime.UTC)
    amz_date, day = t.strftime("%Y%m%dT%H%M%SZ"), t.strftime("%Y%m%d")
    host = urllib.parse.urlsplit(endpoint).netloc
    cpath = _aws_quote(path, True)
    cquery = "&".join(f"{_aws_quote(k, False)}={_aws_quote(v, False)}" for k, v in sorted(query.items()))
    signed = "host;x-amz-content-sha256;x-amz-date"
    canonical = "\n".join(
        [
            "GET",
            cpath,
            cquery,
            f"host:{host}\nx-amz-content-sha256:{_EMPTY_SHA256}\nx-amz-date:{amz_date}\n",
            signed,
            _EMPTY_SHA256,
        ]
    )
    scope = f"{day}/{region}/s3/aws4_request"
    to_sign = f"AWS4-HMAC-SHA256\n{amz_date}\n{scope}\n{hashlib.sha256(canonical.encode()).hexdigest()}"
    k = hmac.new(f"AWS4{secret}".encode(), day.encode(), hashlib.sha256).digest()
    for part in (region, "s3", "aws4_request"):
        k = hmac.new(k, part.encode(), hashlib.sha256).digest()
    sig = hmac.new(k, to_sign.encode(), hashlib.sha256).hexdigest()
    headers = {
        "x-amz-date": amz_date,
        "x-amz-content-sha256": _EMPTY_SHA256,
        "Authorization": f"AWS4-HMAC-SHA256 Credential={key}/{scope}, SignedHeaders={signed}, Signature={sig}",
    }
    url = endpoint + cpath + (f"?{cquery}" if cquery else "")
    return url, headers


_DEFAULT: Mounts | None = None


def resolve(uri: str | MountURI) -> Path:
    """Resolve with the mounts of this process (``CADENCE_MOUNTS``)."""
    global _DEFAULT
    if _DEFAULT is None:
        _DEFAULT = Mounts.from_env()
    return _DEFAULT.resolve(uri)
