"""Cadence worker: discovers step kinds through the `cadence.steps` entry-point group,
publishes their schemas to the control plane, and runs pipeline steps as subprocesses."""

from __future__ import annotations

import json
from importlib.metadata import entry_points
from typing import Any

from cadence_worker.steps.base import StepKind, implements_step_kind, missing_metadata, params_schema


class RegistryError(Exception):
    pass


def load_kinds() -> dict[str, type[StepKind]]:
    kinds: dict[str, type[StepKind]] = {}
    for ep in entry_points(group="cadence.steps"):
        cls = ep.load()
        if not implements_step_kind(cls):
            raise RegistryError(f"step kind {ep.name!r} does not implement the StepKind contract")
        kinds[ep.name] = cls
    return kinds


def registry() -> dict[str, dict[str, Any]]:
    """The registry the worker publishes to the control plane (phase 2 posts it; phase 0 prints it)."""
    out: dict[str, dict[str, Any]] = {}
    for name, cls in sorted(load_kinds().items()):
        if bad := missing_metadata(cls):
            raise RegistryError(f"step kind {name!r}: parameters without complete x-cadence metadata: {bad}")
        out[name] = {
            "version": cls.version,
            "params": params_schema(cls),
            "consumes": list(cls.consumes),
            "produces": list(cls.produces),
            "resources": dict(cls.resources),
            "help": f"steps.{name.replace('_', '-')}",  # help slugs use dashes
        }
    return out


if __name__ == "__main__":
    print(json.dumps(registry(), indent=2))
