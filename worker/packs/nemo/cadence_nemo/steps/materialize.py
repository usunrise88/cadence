"""checkpoint_from_base — the materialize role of the Nemotron family: turn a ``base_model`` artifact (the upstream
``.nemo`` at its pinned revision) into a ``checkpoint`` artifact with the payload layout a trained checkpoint has
(``model.nemo`` and ``checkpoint.json``), so evaluations transcribe a base model exactly as they transcribe a fine-tune
(docs/review/2026-10-02-phase-3-plan.md, decision 6). The weights hash is the BLAKE3 hash of ``model.nemo``, as for
every checkpoint of the family. Runs on the CPU and imports no NeMo: it copies the file from the Hugging Face cache
(downloading it there first when absent). Help: docs/help/steps/checkpoint-from-base.md.
"""

from __future__ import annotations

import shutil
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo.family import RUNTIME
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext


class MaterializeParams(BaseModel):
    """No parameters: the base model artifact pins repository, revision and file."""


def materialize(base: ck.Base, out: Path) -> dict[str, Any]:
    """Write the checkpoint directory ``out`` from a resolved base; returns checkpoint.json."""
    if base.kind != "base_model":
        raise StepInputError("the base input is already a checkpoint; wire it to the transcribe step directly")
    out.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(base.nemo, out / ck.NEMO_FILE)
    return ck.write_checkpoint(
        out,
        {"step": 0, "valWer": None, "base": base.reference, "tokenizer": base.tokenizer, "init": "base"},
    )


class CheckpointFromBaseStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"base": "base_model"}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "memoryGb": 1, "diskGb": 4, "jobKind": "eval"}
    role: ClassVar[str] = "materialize"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = MaterializeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        if "base" not in inputs:
            raise StepInputError("checkpoint_from_base needs a base input (a base_model artifact)")
        if inputs["base"].is_dir():
            raise StepInputError("the base input is already a checkpoint; wire it to the transcribe step directly")
        ctx.progress(0.0, "fetching the base model")
        base = ck.read_base(inputs["base"], ck.download_base)
        doc = materialize(base, outputs["checkpoint"])
        ctx.set_meta("checkpoint", ck.neutral_meta(doc))
        ctx.log("materialized", weightsHash=doc["weightsHash"], base=base.reference.get("hfRepo"))
        ctx.progress(1.0, "done")
