"""checkpoint_average — the average role of the Nemotron family: the element-wise mean of two or more checkpoints'
weights (one checkpoint-typed input wired as ``checkpoints.0``, ``checkpoints.1``, …), written as one new
``checkpoint`` with the configuration and tokenizer of the first. Runs on the CPU on the ``.nemo`` tars (no NeMo
import). Help: docs/help/steps/checkpoint-average.md.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo.family import RUNTIME
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext


class AverageParams(BaseModel):
    """No parameters: the inputs say what to average."""


def checkpoint_inputs(inputs: Mapping[str, Path], name: str = "checkpoints") -> list[Path]:
    """``checkpoints.0``, ``checkpoints.1``, … in index order (a bare ``checkpoints`` first)."""
    keyed: list[tuple[int, Path]] = []
    for k, p in inputs.items():
        if k == name:
            keyed.append((-1, p))
        elif k.startswith(name + "."):
            idx = k[len(name) + 1 :]
            if not idx.isdigit():
                raise StepInputError(f"input {k!r}: expected {name}.<n>")
            keyed.append((int(idx), p))
    return [p for _, p in sorted(keyed)]


class AverageStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"checkpoints": "checkpoint"}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "memoryGb": 12, "diskGb": 10, "jobKind": "training"}
    role: ClassVar[str] = "average"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = AverageParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        dirs = checkpoint_inputs(inputs)
        if len(dirs) < 2:
            raise StepInputError("averaging needs at least two checkpoints (checkpoints.0, checkpoints.1, …)")
        docs = [ck.read_checkpoint(d) for d in dirs]
        bases = {
            str((d.get("base") or {}).get("hfRepo")) + "@" + str((d.get("base") or {}).get("revision")) for d in docs
        }
        if len(bases) > 1:
            raise StepInputError(
                f"the checkpoints descend from different base models ({sorted(bases)}); cannot average"
            )
        out = outputs["checkpoint"]
        ck.average_nemo(
            [d / ck.NEMO_FILE for d in dirs],
            out / ck.NEMO_FILE,
            progress=lambda i, n: ctx.progress(i / n, f"read {i}/{n}"),
        )
        steps = [int(d["step"]) for d in docs if isinstance(d.get("step"), int)]
        doc = ck.write_checkpoint(
            out,
            {
                "step": max(steps) if steps else None,
                "valWer": None,
                "averagedFrom": [str(d.get("weightsHash")) for d in docs],
                "averagedSteps": steps,
                "base": docs[0].get("base"),
                "tokenizer": docs[0].get("tokenizer"),
                "init": "average",
            },
        )
        ctx.set_meta("checkpoint", ck.neutral_meta(doc))
        ctx.log("averaged", checkpoints=len(dirs), steps=steps)
