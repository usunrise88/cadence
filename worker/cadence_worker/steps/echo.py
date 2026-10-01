"""Smallest possible step kind — the template for every real one.

Copies a text artifact to its output, optionally prefixed. It demonstrates the whole contract: version, named inputs
and outputs with artifact types, resources, parameters with ``x-cadence`` metadata, the step context, and a ``run()``
that only reads inputs and writes outputs. Runtime-neutral: it ships in every runtime image. Help:
docs/help/steps/echo.md.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

from pydantic import BaseModel

from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class EchoParams(BaseModel):
    prefix: str = cadence_field(
        "",
        description="Text written before the copied input",
        source="Cadence recommendation",
        range={"maxLength": 64},
    )


class EchoStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"text": "text"}
    produces: ClassVar[Mapping[str, str]] = {"text": "text"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = EchoParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: StepContext,
    ) -> None:
        p = EchoParams.model_validate(params.model_dump())
        src = inputs.get("text")
        if src is None or not src.is_file():
            raise StepInputError("echo needs a text input")
        text = src.read_text(encoding="utf-8")
        outputs["text"].write_text(p.prefix + text, encoding="utf-8")
        ctx.log("echoed", chars=len(text))
        ctx.progress(1.0, "done")
