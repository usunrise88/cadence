"""Smallest possible step kind — the template for every real one.

Copies a text artifact to its output, optionally prefixed. It demonstrates the whole contract: version, artifact
types, resources, parameters with ``x-cadence`` metadata, and a ``run()`` that only reads inputs and writes
outputs. Help: docs/help/steps/echo.md.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker.steps.base import cadence_field


class EchoParams(BaseModel):
    prefix: str = cadence_field(
        "",
        description="Text written before the copied input",
        source="Cadence recommendation",
        range={"maxLength": 64},
    )


class EchoStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[tuple[str, ...]] = ("text",)
    produces: ClassVar[tuple[str, ...]] = ("text",)
    resources: ClassVar[Mapping[str, Any]] = {"gpu": False}
    Params: ClassVar[type[BaseModel]] = EchoParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path]) -> None:
        p = EchoParams.model_validate(params.model_dump())
        if len(p.prefix) > 64:
            raise ValueError("prefix is longer than 64 characters")
        outputs["text"].write_text(p.prefix + inputs["text"].read_text(encoding="utf-8"), encoding="utf-8")
