"""toy_calibrate — the calibrate role of toy-ctc: time a few optimiser steps at the configured batch size and write a
``calibration`` artifact (the estimate switches to the measured basis from it). Help: docs/help/steps/toy-calibrate.md.
"""

from __future__ import annotations

import json
import time
from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

import torch
from pydantic import BaseModel

from cadence_toy.data import read_dataset, splits
from cadence_toy.family import NAME, RUNTIME
from cadence_toy.model import CharTokenizer, TinyCTC, use_one_thread
from cadence_toy.training import sampler, train_step
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import cadence_field
from cadence_worker.steps.context import StepContext


class CalibrateParams(BaseModel):
    batch_size: int = cadence_field(default_ref="packs.toy.batch_size")
    steps: int = cadence_field(default_ref="packs.toy.calibrate_steps")


class CalibrateStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"calibration": "calibration"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "training"}
    role: ClassVar[str] = "calibrate"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = CalibrateParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        p = CalibrateParams.model_validate(params.model_dump())
        torch.manual_seed(0)
        train, _ = splits(read_dataset(inputs["data"]))
        tok = CharTokenizer()
        model = TinyCTC()
        opt = torch.optim.Adam(model.parameters(), lr=1e-3)
        size = max(1, int(p.batch_size * ctx.batch_scale))
        train_step(model, opt, sampler(train, size, 0, 0), tok)  # warm-up, not timed
        t0 = time.perf_counter()
        for i in range(p.steps):
            train_step(model, opt, sampler(train, size, 0, i + 1), tok)
            ctx.progress((i + 1) / p.steps, f"timed step {i + 1}/{p.steps}")
        seconds = (time.perf_counter() - t0) / p.steps
        doc = {
            "family": NAME,
            "batchSize": size,
            "secondsPerStep": round(seconds, 6),
            "stepsMeasured": p.steps,
            "device": "cpu",
            "precision": "fp32",
            "memoryCapMb": ctx.memory_cap_mb,
        }
        outputs["calibration"].write_text(json.dumps(doc, indent=2), encoding="utf-8")
        ctx.set_meta("calibration", {"family": NAME, "batchSize": size, "secondsPerStep": doc["secondsPerStep"]})
        ctx.final_metric("seconds_per_step", seconds)
        ctx.log("calibrated", batchSize=size, secondsPerStep=seconds)
