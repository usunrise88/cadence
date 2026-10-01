"""toy_train — the train role of toy-ctc: a few optimiser steps with CTC loss, metrics loss / lr / val_wer, a
``checkpoint`` (weights, config, tokenizer and the neutral meta family, step, valWer, weightsHash) and a separate
``training-state`` (optimiser and step, used only to resume); every validation before the last publishes its
checkpoint while training runs (``ctx.publish``). Stops at a checkpoint boundary when asked. Help:
docs/help/steps/toy-train.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import ClassVar

import torch
from pydantic import BaseModel

from cadence_toy.data import read_dataset, splits
from cadence_toy.family import NAME, RUNTIME
from cadence_toy.model import CharTokenizer, TinyCTC, save_checkpoint, use_one_thread, weights_hash
from cadence_toy.training import evaluate, sampler, train_step
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class TrainParams(BaseModel):
    steps: int = cadence_field(default_ref="packs.toy.train_steps")
    learning_rate: float = cadence_field(default_ref="packs.toy.learning_rate")
    batch_size: int = cadence_field(default_ref="packs.toy.batch_size")
    val_every: int = cadence_field(default_ref="packs.toy.val_every")
    seed: int = cadence_field(default_ref="packs.toy.seed")


def save_state(d: Path, model: TinyCTC, opt: torch.optim.Optimizer, step: int, seed: int) -> None:
    d.mkdir(parents=True, exist_ok=True)
    torch.save(model.state_dict(), d / "model.pt")
    torch.save(opt.state_dict(), d / "optimizer.pt")
    (d / "state.json").write_text(json.dumps({"family": NAME, "step": step, "seed": seed}), encoding="utf-8")


def publish_checkpoint(ctx: StepContext, model: TinyCTC, tok: CharTokenizer, step: int, val_wer: float) -> None:
    """Every validation's checkpoint is registered while training runs (ctx.publish), not only the last."""
    d = ctx.work_dir / "published" / f"checkpoint-{step}"
    save_checkpoint(d, model, tok, step)
    meta = {"family": NAME, "step": step, "valWer": val_wer, "weightsHash": weights_hash(d / "model.pt")}
    ctx.publish("checkpoint", d, meta, {"val_wer": val_wer})


class TrainStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"checkpoint": "checkpoint", "state": "training-state"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "training"}
    role: ClassVar[str] = "train"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = TrainParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        p = TrainParams.model_validate(params.model_dump())
        torch.manual_seed(p.seed)
        train, val = splits(read_dataset(inputs["data"]))
        tok = CharTokenizer()
        model = TinyCTC()
        opt = torch.optim.Adam(model.parameters(), lr=p.learning_rate)
        step = 0
        if ctx.resume_from is not None:
            state = json.loads((ctx.resume_from / "state.json").read_text(encoding="utf-8"))
            if state.get("family") != NAME:
                raise StepInputError(f"the training state belongs to {state.get('family')!r}, not {NAME}")
            model.load_state_dict(torch.load(ctx.resume_from / "model.pt", weights_only=True))
            opt.load_state_dict(torch.load(ctx.resume_from / "optimizer.pt", weights_only=True))
            step = int(state["step"])
            ctx.log("resumed", step=step)
        size = max(1, int(p.batch_size * ctx.batch_scale))
        val_wer: float | None = None
        while step < p.steps:
            if ctx.should_stop():
                save_state(outputs["state"], model, opt, step, p.seed)
                ctx.set_meta("state", {"family": NAME, "step": step})
                ctx.log("stopped; training state written", step=step)
                return
            loss = train_step(model, opt, sampler(train, size, p.seed, step), tok)
            step += 1
            ctx.metric("loss", loss, step=step)
            ctx.metric("lr", p.learning_rate, step=step)
            if step % p.val_every == 0 or step == p.steps:
                val_wer = evaluate(model, tok, val)
                ctx.metric("val_wer", val_wer, step=step)
                if step < p.steps:  # the last validation's checkpoint is the step's output
                    publish_checkpoint(ctx, model, tok, step, val_wer)
            ctx.progress(step / p.steps, f"step {step}/{p.steps}")
        if val_wer is None:
            val_wer = evaluate(model, tok, val)
            ctx.metric("val_wer", val_wer, step=step)
        save_checkpoint(outputs["checkpoint"], model, tok, step)
        save_state(outputs["state"], model, opt, step, p.seed)
        meta = {
            "family": NAME,
            "step": step,
            "valWer": val_wer,
            "weightsHash": weights_hash(outputs["checkpoint"] / "model.pt"),
        }
        ctx.set_meta("checkpoint", meta)
        ctx.set_meta("state", {"family": NAME, "step": step})
