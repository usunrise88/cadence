"""nemotron_finetune — the train role of the Nemotron family: fine-tune from a base model (or a checkpoint) on a mix,
NeMo + Lightning in bf16 with the calibrated Lhotse buckets, the language prompt per clip and telephony augmentation
on the fly.

Outputs: ``checkpoint`` (the weights at the end, validated), ``checkpoint_best`` (the best validation pass of this
lease; the same artifact as ``checkpoint`` when that was the last one) and ``state`` (``training-state``, resume only).
Metrics during training through the step context (loss, lr, grad_norm, throughput_audio_s_per_s, gpu_memory_mb,
val_wer); the peak learning rate and the Noam scale derived from it go to the log, the output meta and the final
metrics. A stop (cancel, pause, a closing window) ends at the next step with a training state, as does every
``state_every_minutes``; ``overrides.resumeFrom`` continues to the total ``steps``. Help:
docs/help/steps/nemotron-finetune.md.
"""

from __future__ import annotations

import os
import time
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import gpu, lang, noam, training
from cadence_nemo.augment import parse_profile
from cadence_nemo.family import NAME, RUNTIME
from cadence_nemo.mixdata import Clip, TrainingData, input_cfg, nemo_rows, write_jsonl
from cadence_nemo.monitor import TrainingMonitor
from cadence_nemo.steps.common import base_input, card, data_input, stop_grace
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class FinetuneParams(BaseModel):
    steps: int = cadence_field(default_ref="training.steps")
    seed: int = cadence_field(default_ref="packs.nemo.seed")
    precision: str = cadence_field(default_ref="training.precision")
    peak_lr: float = cadence_field(default_ref="packs.nemo.peak_lr")
    warmup_steps: int = cadence_field(default_ref="packs.nemo.warmup_steps")
    min_lr: float = cadence_field(default_ref="packs.nemo.min_lr")
    weight_decay: float = cadence_field(default_ref="packs.nemo.weight_decay")
    grad_clip: float = cadence_field(default_ref="packs.nemo.grad_clip")
    val_every: int = cadence_field(default_ref="packs.nemo.val_every")
    val_max_utterances: int = cadence_field(default_ref="packs.nemo.val_max_utterances")
    val_batch_size: int = cadence_field(default_ref="packs.nemo.val_batch_size")
    log_every: int = cadence_field(default_ref="packs.nemo.log_every")
    state_every_minutes: float = cadence_field(default_ref="packs.nemo.state_every_minutes")
    max_duration: float = cadence_field(default_ref="packs.nemo.max_duration")
    min_duration: float = cadence_field(default_ref="packs.nemo.min_duration")
    num_workers: int = cadence_field(default_ref="packs.nemo.num_workers")
    prompt_mode: str = cadence_field(default_ref="packs.nemo.prompt_mode")
    unified_auto_ratio: float = cadence_field(default_ref="packs.nemo.unified_auto_ratio")
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    augmentation: dict[str, Any] = cadence_field(default_ref="packs.nemo.augmentation")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")


def read_calibration(path: Path | None) -> tuple[list[float], list[int]]:
    """Bucket bounds and batch sizes from a calibration artifact."""
    import json

    if path is None:
        raise StepInputError("the step needs a calibration input (oomptimizer_calibrate's output)")
    try:
        doc = json.loads(path.read_bytes())
    except (OSError, ValueError) as e:
        raise StepInputError(f"the calibration input is not JSON: {e}") from e
    bins = doc.get("bucketDurationBins") or (doc.get("batchSizes") or {}).get("bucket_duration_bins")
    batches = doc.get("bucketBatchSize") or (doc.get("batchSizes") or {}).get("bucket_batch_size")
    if not isinstance(bins, list) or not isinstance(batches, list) or len(bins) != len(batches) or not bins:
        raise StepInputError("the calibration has no bucket_duration_bins / bucket_batch_size")
    if doc.get("family") not in (None, NAME):
        raise StepInputError(f"the calibration was measured for family {doc.get('family')!r}, not {NAME}")
    return [float(b) for b in bins], [int(b) for b in batches]


def fit_buckets(bins: list[float], batches: list[int], max_duration: float) -> tuple[list[float], list[int], float]:
    """The calibrated buckets up to ``max_duration``: those whose bound is within it, plus — when a longer bucket
    exists — one ending at ``max_duration`` with that bucket's batch size (its clips are shorter than it was sized for).
    Returns the bounds, the batch sizes and the effective longest clip."""
    keep = [(b, n) for b, n in zip(bins, batches, strict=True) if b <= max_duration]
    if len(keep) < len(bins) and (not keep or keep[-1][0] < max_duration):
        keep.append((max_duration, batches[len(keep)]))
    if not keep:
        raise StepInputError(f"no calibrated bucket fits max_duration {max_duration} s")
    return [b for b, _ in keep], [n for _, n in keep], keep[-1][0]


def validation_clips(data: TrainingData, limit: int, min_duration: float, max_duration: float) -> list[Clip]:
    val = [c for c in data.validation_clips() if min_duration <= c.duration <= max_duration]
    if not val:  # a mix without a validation split validates on its first training clips
        val = [c for c in data.train_clips() if min_duration <= c.duration <= max_duration]
    return val[:limit]


class FinetuneStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"base": "base_model", "data": "mix", "calibration": "calibration"}
    produces: ClassVar[Mapping[str, str]] = {
        "checkpoint": "checkpoint",
        "checkpoint_best": "checkpoint",
        "state": "training-state",
    }
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 24, "diskGb": 40, "jobKind": "training"}
    role: ClassVar[str] = "train"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = FinetuneParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = FinetuneParams.model_validate(params.model_dump())
        profile = parse_profile(p.augmentation)
        resume = ck.read_state(ctx.resume_from) if ctx.resume_from is not None else None
        applied = card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        base = base_input(inputs)
        data = data_input(inputs, ctx)
        bins, batches = read_calibration(inputs.get("calibration"))
        bins, batches, max_duration = fit_buckets(bins, batches, p.max_duration)
        batches = training.scaled_batches(batches, ctx.batch_scale, profile.min_speed)
        if resume is not None and ctx.should_stop() and ctx.resume_from is not None:
            ck.link_checkpoint(ctx.resume_from, outputs["state"])  # stopped before starting: hand the state back
            return

        import lightning.pytorch as pl

        pl.seed_everything(p.seed, workers=True)
        ctx.progress(0.0, "loading the model")
        model = training.load_model(base.nemo, "cpu")
        facts = training.model_facts(model)
        keys = lang.prompt_keys(data.languages(), facts.prompt_dictionary, p.target_lang)
        tagged = {k: training.single_piece(model, k) for k in set(keys.values())}

        def train_text(c: Clip) -> str:
            k = keys[c.language]
            return lang.with_tag(c.text, k) if tagged[k] else c.text

        def prompt(c: Clip) -> str:
            return keys[c.language]

        work = ctx.work_dir
        train_cfg = training.train_ds_config(
            training.to_container(model.cfg.get("train_ds")),
            input_cfg=input_cfg(
                data,
                work / "manifests",
                train_text,
                prompt,
                lambda c: c.split == "train" and p.min_duration <= c.duration <= max_duration,
            ),
            bins=bins,
            batches=batches,
            max_duration=max_duration,
            min_duration=p.min_duration,
            num_workers=p.num_workers,
            seed=p.seed,
            prompt_mode=p.prompt_mode,
            auto_ratio=p.unified_auto_ratio,
            facts=facts,
        )
        val = validation_clips(data, p.val_max_utterances, p.min_duration, max_duration)
        val_manifest = work / "manifests" / "validation.json"
        write_jsonl(val_manifest, nemo_rows(val, lambda c: lang.strip_tags(c.text), prompt))
        val_cfg = training.val_ds_config(
            training.to_container(model.cfg.get("validation_ds")),
            manifest=val_manifest,
            batch_size=p.val_batch_size,
            num_workers=min(2, p.num_workers),
            facts=facts,
        )
        scale = noam.scale_for_peak(p.peak_lr, facts.d_model, p.warmup_steps)
        optim_cfg = training.optim_config(
            training.to_container(model.cfg.get("optim")),
            scale=scale,
            warmup_steps=p.warmup_steps,
            min_lr=p.min_lr,
            weight_decay=p.weight_decay,
            d_model=facts.d_model,
        )
        lr_info = {"peakLr": p.peak_lr, "noamScale": scale, "dModel": facts.d_model, "warmupSteps": p.warmup_steps}
        ctx.log("optimiser: AdamW + NoamAnnealing", optimiser=lr_info)
        ctx.final_metric("peak_lr", p.peak_lr)
        ctx.final_metric("noam_scale", scale)
        ctx.log(
            "data",
            train=len(data.train_clips()),
            validation=len(val),
            prompts=keys,
            languageTag=tagged,
            buckets=bins,
            batches=batches,
            augmentation=profile.model_dump(),
        )

        monitor = TrainingMonitor(
            ctx,
            total_steps=p.steps,
            log_every=p.log_every,
            state_every_s=p.state_every_minutes * 60,
            stop_grace_s=stop_grace(),
        )
        state_dir = outputs["state"]
        best: dict[str, Any] = {}
        start_step = int(resume.get("step", 0)) if resume else 0

        def save_state(trainer: Any, step: int) -> None:
            t0 = time.monotonic()
            state_dir.mkdir(parents=True, exist_ok=True)
            tmp = state_dir / (ck.STATE_CKPT + ".tmp")
            trainer.save_checkpoint(str(tmp))
            os.replace(tmp, state_dir / ck.STATE_CKPT)
            ck.write_state_json(
                state_dir,
                {
                    "step": step,
                    "seed": p.seed,
                    "bestValWer": monitor.best_wer,
                    "bestStep": monitor.best_step,
                    "base": base.reference,
                    "peakLr": p.peak_lr,
                },
            )
            seconds = time.monotonic() - t0
            monitor.state_saved(step, seconds)
            ctx.log("training state written", step=step, seconds=round(seconds, 1))

        class StepHooks(training.Hooks):
            def save_state(self, trainer: Any, step: int) -> None:
                save_state(trainer, step)

            def keep_best(self, module: Any, step: int) -> None:
                best["state"] = training.cpu_state(module)
                best["step"] = step

            def on_stop(self, trainer: Any, step: int) -> None:
                if monitor.save_fresh_on_stop():
                    save_state(trainer, step)
                else:
                    ctx.log("stop: releasing the periodic training state", step=monitor.last_state_step)

        trainer = training.make_trainer(
            steps=p.steps,
            precision=p.precision,
            val_every=p.val_every,
            grad_clip=p.grad_clip,
            callbacks=[training.lightning_callback(monitor, StepHooks(), facts.sample_rate)],
            log_every_n_steps=max(50, p.log_every),
        )
        model.set_trainer(trainer)
        training.strip_lang_tags(model)
        training.setup_train_dataloader(model, train_cfg, profile)
        training.setup_val_dataloader(model, val_cfg)
        training.setup_optimization(model, optim_cfg)
        ctx.progress(start_step / max(1, p.steps), f"training from step {start_step}")
        trainer.fit(model, ckpt_path=str(ctx.resume_from / ck.STATE_CKPT) if ctx.resume_from else None)

        step = int(trainer.global_step)
        if monitor.stopped or ctx.should_stop():
            if not (state_dir / ck.STATE_CKPT).is_file():
                save_state(trainer, step)
            ctx.set_meta("state", {"family": NAME, "step": monitor.last_state_step or step})
            ctx.log("stopped; training state written", step=monitor.last_state_step or step)
            return
        if monitor.last_val_step != step and val:
            trainer.validate(model, dataloaders=model._validation_dl, verbose=False)
        save_state(trainer, step)
        ctx.set_meta("state", {"family": NAME, "step": step})

        training.clean_data_config(model)
        lineage = {
            "base": base.reference,
            "tokenizer": base.tokenizer,
            "init": base.kind,
            "trainArgs": {
                "steps": p.steps,
                "seed": p.seed,
                "precision": p.precision,
                **lr_info,
                "gradClip": p.grad_clip,
                "buckets": bins,
                "batches": batches,
                "maxDuration": max_duration,
                "promptMode": p.prompt_mode,
                "prompts": keys,
                "augmentation": profile.model_dump(),
                "mix": data.mix,
                "datasets": data.dataset_ids(),
            },
        }
        last_dir = outputs["checkpoint"]
        last_dir.mkdir(parents=True, exist_ok=True)
        model.save_to(str(last_dir / ck.NEMO_FILE))
        last = ck.write_checkpoint(last_dir, {"step": step, "valWer": monitor.last_wer, **lineage})
        ctx.set_meta("checkpoint", ck.neutral_meta(last))
        best_dir = outputs["checkpoint_best"]
        if best.get("state") is not None and best.get("step") != step:
            model.load_state_dict(best["state"])
            best_dir.mkdir(parents=True, exist_ok=True)
            model.save_to(str(best_dir / ck.NEMO_FILE))
            best_doc = ck.write_checkpoint(best_dir, {"step": best["step"], "valWer": monitor.best_wer, **lineage})
        else:
            ck.link_checkpoint(last_dir, best_dir)
            best_doc = last
        ctx.set_meta("checkpoint_best", ck.neutral_meta(best_doc))
        ctx.log(
            "trained",
            step=step,
            valWer=monitor.last_wer,
            bestStep=best_doc.get("step"),
            card=applied,
            memory=gpu.peak_mb(),
        )
