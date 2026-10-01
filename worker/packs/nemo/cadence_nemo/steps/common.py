"""Pieces the NeMo step kinds share: the card under the cap, reading the base and data inputs."""

from __future__ import annotations

import os
from collections.abc import Mapping
from pathlib import Path
from typing import Any

from cadence_nemo import gpu
from cadence_nemo.checkpoint import Base, download_base, read_base
from cadence_nemo.mixdata import TrainingData, read_training_data
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

STOP_GRACE_ENV = "CADENCE_STOP_GRACE_SECONDS"
# Development only: CADENCE_NEMO_DEVICE=cpu lets the train and transcribe steps run on the CPU (slowly) to check the
# glue without a card. Workers never set it.
DEVICE_ENV = "CADENCE_NEMO_DEVICE"


def device() -> str:
    import torch

    return "cuda" if torch.cuda.is_available() else "cpu"


def card(ctx: StepContext, reserve_mb: int, allow_cpu: bool = False) -> dict[str, Any]:
    """Cap device 0 under the lease's memory cap (minus the context reserve); refuse to run without a card."""
    import torch

    if not torch.cuda.is_available():
        if allow_cpu and os.environ.get(DEVICE_ENV) == "cpu":
            ctx.log("no card: running on the CPU (development only)", level="warn")
            return {"device": "cpu"}
        raise StepInputError("this step needs a CUDA card (resources.gpu) and none is visible")
    applied = gpu.apply_cap(ctx.memory_cap_mb, reserve_mb)
    ctx.log("card", **applied)
    return applied


def base_input(inputs: Mapping[str, Path]) -> Base:
    if "base" not in inputs:
        raise StepInputError("the step needs a base input (a base_model artifact or a checkpoint)")
    return read_base(inputs["base"], download_base)


def data_input(inputs: Mapping[str, Path], ctx: StepContext) -> TrainingData:
    if "data" not in inputs:
        raise StepInputError("the step needs a data input (a mix artifact)")
    return read_training_data(inputs["data"], ctx.blob)


def stop_grace() -> float:
    try:
        return float(os.environ.get(STOP_GRACE_ENV, "60"))
    except ValueError:
        return 60.0
