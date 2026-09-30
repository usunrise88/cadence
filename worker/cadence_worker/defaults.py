"""defaults.yaml for step parameters (docs/spec/08-resolutions.md R11).

``control-plane/defaults/defaults.yaml`` is the single source: the control plane embeds it and the worker reads the
same file, never a copy of its values. Lookup order: ``CADENCE_DEFAULTS_FILE`` (the images copy the file to
``/opt/cadence/defaults.yaml`` and set it), then the repository checkout this package lives in (development).
"""

from __future__ import annotations

import functools
import os
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

ENV = "CADENCE_DEFAULTS_FILE"
REPO_FILE = Path(__file__).resolve().parents[2] / "control-plane" / "defaults" / "defaults.yaml"


class DefaultsError(Exception):
    pass


@dataclass(frozen=True)
class Default:
    """One entry of defaults.yaml: its value with provenance."""

    ref: str
    value: Any
    description: str
    source: str
    range: Mapping[str, Any] | None
    unit: str = ""


def defaults_path() -> Path:
    if env := os.environ.get(ENV):
        return Path(env)
    if REPO_FILE.is_file():
        return REPO_FILE
    raise DefaultsError(f"defaults.yaml not found: set {ENV} (the worker image sets it to /opt/cadence/defaults.yaml)")


@functools.cache
def _load(path: Path) -> dict[str, Any]:
    doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(doc, dict):
        raise DefaultsError(f"{path}: not a mapping")
    return doc


def document() -> dict[str, Any]:
    return _load(defaults_path())


def lookup(ref: str) -> Default:
    """Resolve ``<section>.<key>[.<key>…]`` to its entry; the entry must carry value, description and source."""
    node: Any = document()
    for part in ref.split("."):
        if not isinstance(node, dict) or part not in node:
            raise DefaultsError(f"defaults.yaml has no entry {ref!r}")
        node = node[part]
    if not isinstance(node, dict) or "value" not in node:
        raise DefaultsError(f"defaults.yaml entry {ref!r} is a section, not a value")
    for key in ("description", "source"):
        if not str(node.get(key, "")).strip():
            raise DefaultsError(f"defaults.yaml entry {ref!r} has no {key}")
    rng = node.get("range")
    return Default(
        ref=ref,
        value=node["value"],
        description=str(node["description"]),
        source=str(node["source"]),
        range=dict(rng) if isinstance(rng, dict) else None,
        unit=str(node.get("unit", "")),
    )
