"""The contract every step kind implements (docs/spec/03-pipelines-defaults.md, "Pipelines and extension points").

A step kind declares its version, the artifact types it consumes and produces, its resource needs, a pydantic v2
parameter model in which every field carries ``x-cadence`` metadata (default, description, source, safe range),
and ``run()``, which reads input artifacts and writes output artifacts. Steps never touch the database or the API
except to report progress.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Protocol, runtime_checkable

from pydantic import BaseModel, Field

X_CADENCE_KEYS = ("default", "description", "source", "range")


def cadence_field(
    default: Any,
    *,
    description: str,
    source: str,
    range: Mapping[str, Any] | str,
    default_ref: str | None = None,
) -> Any:
    """A pydantic field with the ``x-cadence`` metadata the UI, help and MCP tool descriptions render from.

    ``default_ref`` points into ``defaults/defaults.yaml`` (R11) once that file lands; until then the literal default
    is the value.
    """
    meta: dict[str, Any] = {
        "default": default,
        "description": description,
        "source": source,
        "range": dict(range) if isinstance(range, Mapping) else range,
    }
    if default_ref:
        meta["defaultRef"] = default_ref
    return Field(default=default, description=description, json_schema_extra={"x-cadence": meta})


@runtime_checkable
class StepKind(Protocol):
    version: ClassVar[str]
    consumes: ClassVar[tuple[str, ...]]
    produces: ClassVar[tuple[str, ...]]
    resources: ClassVar[Mapping[str, Any]]
    Params: ClassVar[type[BaseModel]]

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path]) -> None: ...


REQUIRED_ATTRS = ("version", "consumes", "produces", "resources", "Params", "run")


def implements_step_kind(cls: object) -> bool:
    """Structural check (``issubclass`` cannot test protocols with data members)."""
    return isinstance(cls, type) and all(hasattr(cls, a) for a in REQUIRED_ATTRS)


def params_schema(kind: type[StepKind]) -> dict[str, Any]:
    return kind.Params.model_json_schema()


def missing_metadata(kind: type[StepKind]) -> list[str]:
    """Parameters whose schema lacks complete ``x-cadence`` metadata (the registry refuses such a step)."""
    props: dict[str, Any] = params_schema(kind).get("properties", {})
    bad = []
    for name, prop in props.items():
        meta = prop.get("x-cadence")
        if not isinstance(meta, dict) or any(k not in meta for k in X_CADENCE_KEYS):
            bad.append(name)
    return bad


def file_digest(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def input_hash(name: str, kind: type[StepKind], params: BaseModel, inputs: Mapping[str, Path]) -> str:
    """Idempotence key: step kind, version, resolved parameters and input contents. A re-run with the same hash
    is skipped."""
    doc = {
        "kind": name,
        "version": kind.version,
        "params": params.model_dump(mode="json"),
        "inputs": {k: file_digest(p) for k, p in sorted(inputs.items())},
    }
    return hashlib.sha256(json.dumps(doc, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
