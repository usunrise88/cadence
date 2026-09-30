"""What the worker publishes at start (workerRegistrations.new): its runtime, the step kinds it can run and the model
families it carries (R40, R41, R45).

Step kinds come from the ``cadence.steps`` entry-point group, families from ``cadence.families``. A framework kind or
family names its runtime and is published only by a worker of that runtime; neutral core kinds go everywhere.
"""

from __future__ import annotations

import json
import os
from collections.abc import Mapping
from dataclasses import dataclass, field
from importlib.metadata import PackageNotFoundError, entry_points, version
from pathlib import Path
from typing import Any

from cadence_worker import __version__
from cadence_worker.protocol_gen import ModelFamilyDescriptor, RuntimeDescriptor, StepKindDescriptor
from cadence_worker.steps.base import StepKind, descriptor, implements_step_kind, missing_metadata, runtime_of

STEPS_GROUP = "cadence.steps"
FAMILIES_GROUP = "cadence.families"
RUNTIME_FILE_ENV = "CADENCE_RUNTIME_FILE"
RUNTIME_ENV = "CADENCE_RUNTIME"
DEFAULT_RUNTIME_FILE = Path("/etc/cadence/runtime.json")
# Distributions whose versions make up the environment lock when the runtime file lists none.
DEFAULT_ENVIRONMENT = ("torch", "nemo-toolkit", "lhotse", "numpy", "pydantic", "cadence-toy")


class RegistryError(Exception):
    pass


@dataclass(frozen=True)
class Family:
    """A model family a pack publishes (entry point ``cadence.families``): the runtime it belongs to and its
    descriptor (ModelFamilyDescriptor in api/openapi.yaml). ``fixtures`` and ``conformance`` are for the conformance
    suite only and never published: an import folder in ``dataset_import``'s ``folder-csv`` format (``metadata.csv``
    with columns file, text, split? and the clips), which the suite imports into a ``dataset`` artifact first, and
    per-stage parameters (keys import, calibrate, train, resume, stop, average, transcribe)."""

    runtime: str
    descriptor: ModelFamilyDescriptor
    fixtures: Path | None = None
    conformance: Mapping[str, Mapping[str, Any]] = field(default_factory=dict)


@dataclass(frozen=True)
class KindEntry:
    name: str
    cls: type[StepKind]
    ref: str  # module:attr, what the step subprocess imports


def load_kinds(runtime: str | None = None) -> dict[str, KindEntry]:
    """Installed step kinds; with a runtime, only those it may publish (its own and the neutral ones)."""
    kinds: dict[str, KindEntry] = {}
    for ep in entry_points(group=STEPS_GROUP):
        cls = ep.load()
        if not implements_step_kind(cls):
            raise RegistryError(f"step kind {ep.name!r} does not implement the StepKind contract")
        if runtime is not None and runtime_of(cls) not in (None, runtime):
            continue
        kinds[ep.name] = KindEntry(ep.name, cls, ep.value)
    return kinds


def load_families(runtime: str | None = None) -> list[Family]:
    out: list[Family] = []
    for ep in entry_points(group=FAMILIES_GROUP):
        fam = ep.load()
        if not isinstance(fam, Family):
            raise RegistryError(f"model family {ep.name!r} is not a cadence_worker.registry.Family")
        if fam.descriptor["name"] != ep.name:
            raise RegistryError(f"model family entry point {ep.name!r} names {fam.descriptor['name']!r}")
        if runtime is None or fam.runtime == runtime:
            out.append(fam)
    return sorted(out, key=lambda f: f.descriptor["name"])


def step_kinds(kinds: Mapping[str, KindEntry]) -> dict[str, StepKindDescriptor]:
    out: dict[str, StepKindDescriptor] = {}
    for name, entry in sorted(kinds.items()):
        if bad := missing_metadata(entry.cls):
            raise RegistryError(f"step kind {name!r}: parameters without complete x-cadence metadata: {bad}")
        out[name] = descriptor(name, entry.cls)
    return out


def registry(runtime: str | None = None) -> dict[str, StepKindDescriptor]:
    """The step kinds a worker of this runtime publishes (all installed kinds without one)."""
    return step_kinds(load_kinds(runtime))


def _dist_version(name: str) -> str | None:
    try:
        return version(name)
    except PackageNotFoundError:
        return None


def runtime_descriptor() -> RuntimeDescriptor:
    """The runtime from ``CADENCE_RUNTIME`` (JSON) or ``CADENCE_RUNTIME_FILE`` (default /etc/cadence/runtime.json,
    baked into the image), with the environment lock probed from the installed distributions."""
    raw: dict[str, Any]
    if env := os.environ.get(RUNTIME_ENV):
        raw = json.loads(env)
    else:
        path = Path(os.environ.get(RUNTIME_FILE_ENV, str(DEFAULT_RUNTIME_FILE)))
        if not path.is_file():
            raise RegistryError(f"no runtime descriptor: set {RUNTIME_ENV} or {RUNTIME_FILE_ENV} (tried {path})")
        raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, dict) or not raw.get("name") or not raw.get("version"):
        raise RegistryError("the runtime descriptor needs a name and a version")
    probe = raw.pop("environmentPackages", None)
    packages = [str(p) for p in probe] if isinstance(probe, list) else list(DEFAULT_ENVIRONMENT)
    env_lock: dict[str, str] = {str(k): str(v) for k, v in dict(raw.get("environment") or {}).items()}
    for pkg in packages:
        if (v := _dist_version(pkg)) is not None:
            env_lock.setdefault(pkg, v)
    desc: RuntimeDescriptor = {"name": str(raw["name"]), "version": str(raw["version"])}
    if raw.get("image"):
        desc["image"] = str(raw["image"])
    if digest := os.environ.get("CADENCE_RUNTIME_DIGEST", raw.get("digest") or ""):
        desc["digest"] = str(digest)
    if env_lock:
        desc["environment"] = dict(sorted(env_lock.items()))
    desc["plugin"] = __version__
    return desc
