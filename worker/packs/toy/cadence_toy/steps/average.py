"""toy_average — the average role of toy-ctc: the element-wise mean of two or more checkpoints' weights (passed as
``checkpoints.0``, ``checkpoints.1``, …), written as a new ``checkpoint``. Help: docs/help/steps/toy-average.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

import torch
from pydantic import BaseModel

from cadence_toy.family import NAME, RUNTIME
from cadence_toy.model import load_checkpoint, save_checkpoint, use_one_thread, weights_hash
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext


class AverageParams(BaseModel):
    """No parameters: the inputs say what to average."""


def checkpoint_inputs(inputs: Mapping[str, Path], name: str = "checkpoints") -> list[Path]:
    keyed = [(k, p) for k, p in inputs.items() if k == name or k.startswith(name + ".")]
    return [p for _, p in sorted(keyed, key=lambda kp: int(kp[0].rpartition(".")[2]) if "." in kp[0] else -1)]


class AverageStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"checkpoints": "checkpoint"}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "training"}
    role: ClassVar[str] = "average"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = AverageParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        paths = checkpoint_inputs(inputs)
        if len(paths) < 2:
            raise StepInputError("averaging needs at least two checkpoints (checkpoints.0, checkpoints.1, …)")
        steps: list[int] = []
        sources: list[str] = []
        acc: dict[str, torch.Tensor] = {}
        model = tok = None
        for i, d in enumerate(paths):
            cfg = json.loads((d / "config.json").read_text(encoding="utf-8"))
            if cfg.get("family") != NAME:
                raise StepInputError(f"{d.name} is a {cfg.get('family')!r} checkpoint, not {NAME}")
            if isinstance(cfg.get("step"), int):
                steps.append(cfg["step"])
            model, tok = load_checkpoint(d)
            for k, v in model.state_dict().items():
                acc[k] = acc[k] + v.float() if k in acc else v.float().clone()
            sources.append(weights_hash(d / "model.pt"))
            ctx.progress((i + 1) / len(paths), f"read {i + 1}/{len(paths)}")
        assert model is not None
        assert tok is not None
        model.load_state_dict({k: v / len(paths) for k, v in acc.items()})
        save_checkpoint(outputs["checkpoint"], model, tok, max(steps) if steps else None)
        ctx.set_meta(
            "checkpoint",
            {
                "family": NAME,
                "step": max(steps) if steps else None,
                "averagedFrom": sources,
                "weightsHash": weights_hash(outputs["checkpoint"] / "model.pt"),
            },
        )
        ctx.log("averaged", checkpoints=len(paths))
