"""Step kinds for harness tests only (loaded by the step subprocess through PYTHONPATH=tests)."""

from __future__ import annotations

import json
import os
import signal
import time
from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

from pydantic import BaseModel

from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import cadence_field
from cadence_worker.steps.context import StepContext


class NoParams(BaseModel):
    pass


class LoopParams(BaseModel):
    seconds: float = cadence_field(10.0, description="How long to loop", source="test", range={"min": 0, "max": 60})


class SlowTrain:
    """Posts a metric, then loops until asked to stop; writes its training-state (and never its checkpoint)."""

    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint", "state": "training-state"}
    resources: ClassVar[StepResources] = {"gpu": False, "jobKind": "training"}
    role: ClassVar[str] = "train"
    Params: ClassVar[type[BaseModel]] = LoopParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = LoopParams.model_validate(params.model_dump())
        ctx.metric("loss", 1.0, step=1)
        ctx.progress(0.1, "looping")
        deadline = time.monotonic() + p.seconds
        while time.monotonic() < deadline:
            if ctx.should_stop():
                outputs["state"].mkdir()
                (outputs["state"] / "state.json").write_text('{"step": 1}', encoding="utf-8")
                ctx.set_meta("state", {"step": 1})
                return
            time.sleep(0.05)
        outputs["checkpoint"].write_text("weights", encoding="utf-8")
        outputs["state"].write_text("state", encoding="utf-8")


class Stubborn:
    """Ignores SIGTERM (the harness must kill it after the grace period)."""

    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"state": "training-state"}
    resources: ClassVar[StepResources] = {"gpu": False}
    Params: ClassVar[type[BaseModel]] = NoParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        ctx.metric("loss", 1.0)
        time.sleep(60)


class CardOom:
    """Raises torch's CUDA out-of-memory error (as a GPU step would)."""

    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"out": "text"}
    resources: ClassVar[StepResources] = {"gpu": False}
    Params: ClassVar[type[BaseModel]] = NoParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        import torch

        raise torch.cuda.OutOfMemoryError("CUDA out of memory. Tried to allocate 2.00 GiB")


class Forgetful:
    """Returns without writing its output."""

    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"out": "text"}
    resources: ClassVar[StepResources] = {"gpu": False}
    Params: ClassVar[type[BaseModel]] = NoParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        ctx.log("forgot")


class EnvProbe:
    """Writes the environment it sees and prints its secret (the harness must redact it)."""

    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"env": "text"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "jobKind": "eval"}
    secrets: ClassVar[tuple[str, ...]] = ("HF_TOKEN",)
    Params: ClassVar[type[BaseModel]] = NoParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        keys = ("HF_TOKEN", "CUDA_VISIBLE_DEVICES", "CADENCE_MEMORY_CAP_MB", "CADENCE_WORKER_TOKEN_FILE", "TRACEPARENT")
        seen = {k: os.environ.get(k) for k in keys}
        seen["card"] = json.dumps({"index": ctx.card.index, "cap": ctx.memory_cap_mb} if ctx.card else None)
        outputs["env"].write_text(json.dumps(seen), encoding="utf-8")
        print(f"token is {os.environ.get('HF_TOKEN')}", flush=True)
        ctx.log("secret in a field", token=os.environ.get("HF_TOKEN"))
