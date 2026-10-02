"""toy_checkpoint_from_base — the materialize role of toy-ctc: turn a ``base_model`` artifact into a ``checkpoint`` with
the layout a trained toy checkpoint has (``model.pt``, ``config.json``, ``tokenizer.json``; neutral meta family, step,
valWer, weightsHash), so the eval seams can transcribe a base model. The toy family has no upstream weights: its "base
model" is the untrained network initialised from the seed the base model names (``model.seed``, default 0). Help:
docs/help/steps/toy-checkpoint-from-base.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

import torch
from pydantic import BaseModel

from cadence_toy.family import NAME, RUNTIME
from cadence_toy.model import CharTokenizer, TinyCTC, save_checkpoint, use_one_thread, weights_hash
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

BASE_MODEL_FORMAT = "cadence.base_model/1"


class MaterializeParams(BaseModel):
    """No parameters: the base model artifact names the seed."""


def read_base_model(path: Path) -> dict[str, Any]:
    try:
        doc = json.loads(path.read_bytes())
    except (OSError, ValueError) as e:
        raise StepInputError(f"the base input is not a base_model artifact: {e}") from e
    if not isinstance(doc, dict) or doc.get("format") != BASE_MODEL_FORMAT:
        raise StepInputError(f"the base input is not a {BASE_MODEL_FORMAT} artifact")
    family = doc.get("family") or {}
    name = family.get("name") if isinstance(family, dict) else family
    if name and name != NAME:
        raise StepInputError(f"the base model belongs to family {name!r}, not {NAME}")
    model = doc.get("model") or {}
    if not isinstance(model, dict):
        raise StepInputError("the base model's model entry is not an object")
    return model


class CheckpointFromBaseStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"base": "base_model"}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    role: ClassVar[str] = "materialize"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = MaterializeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        if "base" not in inputs or inputs["base"].is_dir():
            raise StepInputError("toy_checkpoint_from_base needs a base input (a base_model artifact)")
        model_doc = read_base_model(inputs["base"])
        seed = model_doc.get("seed", 0)
        if not isinstance(seed, int) or isinstance(seed, bool) or seed < 0:
            raise StepInputError(f"the base model's seed must be a non-negative integer, not {seed!r}")
        torch.manual_seed(seed)
        out = outputs["checkpoint"]
        save_checkpoint(out, TinyCTC(), CharTokenizer(), step=0)
        meta = {"family": NAME, "step": 0, "valWer": None, "weightsHash": weights_hash(out / "model.pt")}
        ctx.set_meta("checkpoint", meta)
        ctx.log("materialized", seed=seed, weightsHash=meta["weightsHash"])
