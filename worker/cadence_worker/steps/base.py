"""The contract every step kind implements (docs/spec/03-pipelines-defaults.md, "Pipelines and extension points";
docs/review/2026-09-30-phase-2-plan.md "Worker protocol").

A step kind is a class registered under the ``cadence.steps`` entry-point group. It declares:

- ``version``; ``consumes`` and ``produces``: input and output name → artifact type (``StepKindDescriptor``);
- ``resources``: ``gpu``, ``gpus`` (1 in v1, R44), ``memoryGb``, ``diskGb``, ``jobKind``;
- ``Params``: a pydantic v2 model in which every field carries ``x-cadence`` metadata (default, description, source,
  safe range, optionally ``defaultRef`` into defaults.yaml) — build fields with :func:`cadence_field`;
- optionally ``role`` (the model-family role it fills: calibrate, train, average, transcribe, export, parity,
  materialize), ``neutral`` (a runtime-neutral core kind shipped in every image), ``runtime`` (the runtime a framework
  kind belongs to; None for neutral kinds), ``secrets`` (secret names it needs as environment variables),
  ``optional_inputs`` (inputs of ``consumes`` a pipeline may leave unwired: a transcribe step's boost list; published
  as ``optionalInputs``) and ``optional_outputs`` (outputs a successful step may leave unwritten);
- ``run(params, inputs, outputs, ctx)``: read the input paths, write each output path (a file, or a directory for a
  directory artifact) and report through the :class:`~cadence_worker.steps.context.StepContext`. Steps never touch
  the database or the API.

An input name may receive several artifacts: the pipeline passes ``<name>.0``, ``<name>.1``, … (checkpoint averaging).
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from pathlib import Path
from typing import TYPE_CHECKING, Any, ClassVar, Protocol, runtime_checkable

from pydantic import BaseModel, Field
from pydantic_core import PydanticUndefined

from cadence_worker.cas import ManifestFile, encode_manifest, hash_bytes, hash_file, valid_hash
from cadence_worker.defaults import lookup
from cadence_worker.protocol_gen import StepKindDescriptor, StepResources

if TYPE_CHECKING:
    from cadence_worker.steps.context import StepContext

X_CADENCE_KEYS = ("default", "description", "source", "range")
ROLES = ("calibrate", "train", "average", "transcribe", "export", "parity", "materialize", "live")


class StepInputError(Exception):
    """The inputs or parameters are wrong (reported as error type ``input``; retrying does not help)."""


def cadence_field(
    default: Any = PydanticUndefined,
    *,
    description: str | None = None,
    source: str | None = None,
    range: Mapping[str, Any] | str | None = None,
    default_ref: str | None = None,
    shared: bool = False,
) -> Any:
    """A pydantic field with the ``x-cadence`` metadata the UI, help and MCP tool descriptions render from.

    With ``default_ref`` (``<section>.<key>`` in defaults.yaml, R11) the default, source and range come from that file
    and must not be repeated here; ``description`` may reword the entry's description for this step.

    ``shared`` marks a parameter that a run-level override (``runs.new`` params) sets on every step of the stage that
    declares it, not only on the train step — the data's language, say, means the same to a calibration.
    """
    if default_ref:
        if default is not PydanticUndefined or source is not None or range is not None:
            raise TypeError(f"cadence_field({default_ref!r}): the default, source and range come from defaults.yaml")
        entry = lookup(default_ref)
        default = entry.value
        description = description or entry.description
        source = entry.source
        range = dict(entry.range) if entry.range is not None else "any"
    if default is PydanticUndefined or description is None or source is None or range is None:
        raise TypeError("cadence_field needs a default, description, source and range (or a default_ref)")
    meta: dict[str, Any] = {
        "default": default,
        "description": description,
        "source": source,
        "range": dict(range) if isinstance(range, Mapping) else range,
    }
    if default_ref:
        meta["defaultRef"] = default_ref
    if shared:
        meta["shared"] = True
    return Field(default=default, description=description, json_schema_extra={"x-cadence": meta})


@runtime_checkable
class StepKind(Protocol):
    version: ClassVar[str]
    consumes: ClassVar[Mapping[str, str]]
    produces: ClassVar[Mapping[str, str]]
    resources: ClassVar[StepResources]
    Params: ClassVar[type[BaseModel]]

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: StepContext,
    ) -> None: ...


REQUIRED_ATTRS = ("version", "consumes", "produces", "resources", "Params", "run")


def implements_step_kind(cls: object) -> bool:
    """Structural check (``issubclass`` cannot test protocols with data members)."""
    return (
        isinstance(cls, type)
        and all(hasattr(cls, a) for a in REQUIRED_ATTRS)
        and isinstance(getattr(cls, "consumes", None), Mapping)
        and isinstance(getattr(cls, "produces", None), Mapping)
    )


def role_of(kind: type[Any]) -> str:
    return str(getattr(kind, "role", ""))


def runtime_of(kind: type[Any]) -> str | None:
    r = getattr(kind, "runtime", None)
    return str(r) if r else None


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


def check_ranges(params: BaseModel) -> None:
    """Enforce each field's ``x-cadence`` range (min, max, values, minLength, maxLength); StepInputError otherwise."""
    for name, info in type(params).model_fields.items():
        extra = info.json_schema_extra
        meta = extra.get("x-cadence") if isinstance(extra, dict) else None
        rng = meta.get("range") if isinstance(meta, dict) else None
        if not isinstance(rng, dict):
            continue
        v = getattr(params, name)
        if isinstance(v, int | float) and not isinstance(v, bool):
            lo, hi = rng.get("min"), rng.get("max")
            if isinstance(lo, int | float) and v < lo:
                raise StepInputError(f"{name}: {v} is below the minimum {lo}")
            if isinstance(hi, int | float) and v > hi:
                raise StepInputError(f"{name}: {v} is above the maximum {hi}")
        values = rng.get("values")
        if isinstance(values, list) and values and v not in values:
            raise StepInputError(f"{name}: {v!r} is not one of {values}")
        if isinstance(v, str | list):
            lo_len, hi_len = rng.get("minLength"), rng.get("maxLength")
            if isinstance(lo_len, int) and len(v) < lo_len:
                raise StepInputError(f"{name}: shorter than {lo_len}")
            if isinstance(hi_len, int) and len(v) > hi_len:
                raise StepInputError(f"{name}: longer than {hi_len}")


def help_slug(name: str) -> str:
    """The help article of a step kind: ``steps.<name>`` with underscores as dashes (help slugs are lowercase letters,
    digits and dashes), i.e. docs/help/steps/<name-with-dashes>.md."""
    return "steps." + name.replace("_", "-")


def descriptor(name: str, kind: type[StepKind]) -> StepKindDescriptor:
    """What the worker publishes for one kind (StepKindDescriptor in api/openapi.yaml)."""
    d: StepKindDescriptor = {
        "version": kind.version,
        "params": params_schema(kind),
        "consumes": dict(kind.consumes),
        "produces": dict(kind.produces),
        "resources": StepResources(**kind.resources),
        "help": help_slug(name),
    }
    if role := role_of(kind):
        d["role"] = role
    if getattr(kind, "neutral", False):
        d["neutral"] = True
    if secrets := list(getattr(kind, "secrets", ())):
        d["secrets"] = secrets
    if optional := sorted(getattr(kind, "optional_outputs", ())):
        d["optionalOutputs"] = optional
    if optional_in := sorted(getattr(kind, "optional_inputs", ())):
        d["optionalInputs"] = optional_in
    return d


def artifact_digest(p: Path | str) -> str:
    """The b3 hash of an input: given as a hash, a file, or a directory (hashed as its manifest)."""
    if isinstance(p, str):
        if not valid_hash(p):
            raise ValueError(f"{p!r} is not a b3 hash")
        return p
    if p.is_dir():
        files = [
            ManifestFile(f.relative_to(p).as_posix(), hash_file(f), f.stat().st_size)
            for f in p.rglob("*")
            if f.is_file()
        ]
        return hash_bytes(encode_manifest(files))
    return hash_file(p)


def input_hash(name: str, kind: type[StepKind], params: BaseModel, inputs: Mapping[str, Path | str]) -> str:
    """Idempotence key: step kind, version, resolved parameters and input contents. A re-run with the same hash
    is skipped."""
    doc = {
        "kind": name,
        "version": kind.version,
        "params": params.model_dump(mode="json"),
        "inputs": {k: artifact_digest(p) for k, p in sorted(inputs.items())},
    }
    return hashlib.sha256(json.dumps(doc, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
