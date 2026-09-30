"""oomptimizer_calibrate — the calibrate role of the Nemotron family: OOMptimizer under the lease's memory cap (the
largest batch per duration bucket whose training step and optimiser update fit), then timed optimiser steps on the
mix's own data with those buckets. Writes a ``calibration`` artifact the estimate switches to (runs README
"Calibration"): seconds per step with its spread, the bucket batch sizes and the bucket configuration measured for.
Help: docs/help/steps/oomptimizer-calibrate.md.
"""

from __future__ import annotations

import json
import math
import statistics
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import gpu, lang, oomptimizer, training
from cadence_nemo.augment import Profile
from cadence_nemo.family import NAME, RUNTIME
from cadence_nemo.mixdata import Clip, input_cfg
from cadence_nemo.steps.common import base_input, card, data_input
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

FALLBACK_TOKENS_PER_SECOND = 14.0  # spike A3's invocation, when the mix has no measurable clip


class CalibrateParams(BaseModel):
    precision: str = cadence_field(default_ref="training.precision")
    bucket_bins: list[float] = cadence_field(default_ref="packs.nemo.bucket_bins")
    tokens_per_second: float = cadence_field(default_ref="packs.nemo.tokens_per_second")
    start_batch_size: int = cadence_field(default_ref="packs.nemo.start_batch_size")
    search_threshold: float = cadence_field(default_ref="packs.nemo.search_threshold")
    timed_steps: int = cadence_field(default_ref="packs.nemo.calibrate_timed_steps")
    warmup_steps: int = cadence_field(default_ref="packs.nemo.calibrate_warmup_steps")
    min_duration: float = cadence_field(default_ref="packs.nemo.min_duration")
    num_workers: int = cadence_field(default_ref="packs.nemo.num_workers")
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    grad_clip: float = cadence_field(default_ref="packs.nemo.grad_clip")
    seed: int = cadence_field(default_ref="packs.nemo.seed")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")


def measured_ratio(clips: list[Clip], count: Any, sample: int = 2000) -> float | None:
    """The 99th percentile of tokens per second over up to ``sample`` training clips (``count(clip)`` → tokens)."""
    step = max(1, len(clips) // sample)
    return oomptimizer.tokens_per_second((count(c), c.duration) for c in clips[::step] if c.duration > 0)


def calibration_doc(
    *,
    buckets: list[oomptimizer.Bucket],
    requested: list[float],
    ratio: float,
    seconds: list[float],
    audio: list[float],
    sizes: list[int],
    precision: str,
    applied: Mapping[str, Any],
    peak: Mapping[str, int],
) -> dict[str, Any]:
    """The calibration artifact (and, trimmed, its meta): what the calibration hook and the train step read."""
    mean = statistics.fmean(seconds)
    std = statistics.pstdev(seconds) if len(seconds) > 1 else 0.0
    bins = [b.max_duration for b in buckets]
    batches = [b.batch_size for b in buckets]
    return {
        "format": "cadence.calibration/1",
        "family": NAME,
        "method": "oomptimizer",
        "precision": precision,
        "memoryCapMb": applied.get("memoryCapMb"),
        "allocatorCapMb": applied.get("allocatorCapMb"),
        "device": applied.get("device"),
        "secondsPerStep": round(mean, 4),
        "secondsPerStepStd": round(std, 4),
        "plusMinus": round(min(1.0, 2 * std / mean), 4) if mean > 0 else None,
        "stepsTimed": len(seconds),
        "audioSecondsPerStep": round(statistics.fmean(audio), 2) if audio else None,
        "batchSize": max(1, round(statistics.fmean(sizes))) if sizes else batches[0],
        "batchSizes": {"bucket_duration_bins": bins, "bucket_batch_size": batches},
        "bucketConfig": {"bins": [float(b) for b in requested], "tokensPerSecond": ratio, "maxDuration": max(bins)},
        "bucketDurationBins": bins,
        "bucketBatchSize": batches,
        "maxDuration": max(bins),
        "peakMemoryMb": peak.get("maxReservedMb"),
    }


class CalibrateStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"base": "base_model", "data": "mix"}
    produces: ClassVar[Mapping[str, str]] = {"calibration": "calibration"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 24, "diskGb": 10, "jobKind": "training"}
    role: ClassVar[str] = "calibrate"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = CalibrateParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = CalibrateParams.model_validate(params.model_dump())
        if not p.bucket_bins or any(b <= 0 for b in p.bucket_bins):
            raise StepInputError("bucket_bins must be positive durations")
        applied = card(ctx, p.cuda_context_reserve_mb)
        base = base_input(inputs)
        data = data_input(inputs, ctx)

        import lightning.pytorch as pl
        import torch

        pl.seed_everything(p.seed, workers=True)
        trainer = pl.Trainer(barebones=True, accelerator="gpu", devices=1, logger=False)
        trainer.log_every_n_steps = 10**6  # no WER decode inside the profiled steps
        ctx.progress(0.02, "loading the model")
        model = training.load_model(base.nemo, "cuda", trainer=trainer)
        facts = training.model_facts(model)
        keys = lang.prompt_keys(data.languages(), facts.prompt_dictionary, p.target_lang)
        tagged = {k: training.single_piece(model, k) for k in set(keys.values())}

        def text(c: Clip) -> str:
            k = keys[c.language]
            return lang.with_tag(c.text, k) if tagged[k] else c.text

        train = [c for c in data.train_clips() if c.duration >= p.min_duration] or data.clips()
        ratio = p.tokens_per_second or (
            measured_ratio(train, lambda c: len(model.tokenizer.text_to_ids(text(c)))) or FALLBACK_TOKENS_PER_SECOND
        )
        ratio = float(math.ceil(ratio))
        main_key = max(keys.values(), key=lambda k: sum(1 for c in train if keys[c.language] == k))
        ctx.log("search", tokensPerSecond=ratio, prompt=main_key, buckets=p.bucket_bins)

        optimizer, _ = model.setup_optimization({"name": "adamw", "lr": 1e-7, "weight_decay": 0.0})
        model.train()
        dtype = getattr(torch, training.AUTOCAST[p.precision])
        try_batch = oomptimizer.profile_step(
            model, optimizer, facts.prompt_dictionary[main_key], facts.sample_rate, facts.vocab_size
        )
        done = [0]

        def log(msg: str) -> None:
            done[0] += 1
            ctx.log(msg)
            ctx.progress(min(0.6, 0.05 + 0.02 * done[0]), msg)

        with torch.autocast("cuda", dtype=dtype, enabled=p.precision != "fp32"):
            found = oomptimizer.search_buckets(
                p.bucket_bins,
                ratio,
                try_batch,
                start=max(1, int(p.start_batch_size * ctx.batch_scale)),
                threshold=p.search_threshold,
                log=log,
                should_stop=ctx.should_stop,
            )
        if ctx.should_stop():
            ctx.log("stopped during the batch search; no calibration written")
            return
        buckets = oomptimizer.merge_buckets(found)
        if not buckets:
            raise StepInputError(
                f"not one clip of {min(p.bucket_bins)} s fits under the card's memory cap "
                f"({applied.get('memoryCapMb')} MiB); raise the cap or lower bucket_bins"
            )
        ctx.log("buckets", bins=[b.max_duration for b in buckets], batches=[b.batch_size for b in buckets])

        # Timed steps: the real data with the calibrated buckets (no augmentation; the dataloader overlaps it anyway).
        manifests = ctx.work_dir / "manifests"
        max_duration = max(b.max_duration for b in buckets)
        cfg = training.train_ds_config(
            training.to_container(model.cfg.get("train_ds")),
            input_cfg=input_cfg(
                data,
                manifests,
                text,
                lambda c: keys[c.language],
                lambda c: c.split == "train" and p.min_duration <= c.duration <= max_duration,
            ),
            bins=[b.max_duration for b in buckets],
            batches=[b.batch_size for b in buckets],
            max_duration=max_duration,
            min_duration=p.min_duration,
            num_workers=p.num_workers,
            seed=p.seed,
            prompt_mode="langID",
            auto_ratio=0.0,
            facts=facts,
        )
        training.setup_train_dataloader(model, cfg, Profile(profile="clean"))
        torch.cuda.reset_peak_memory_stats()
        seconds, audio, sizes = training.timed_steps(
            model,
            optimizer,
            model._train_dl,
            warmup=p.warmup_steps,
            steps=p.timed_steps,
            precision=p.precision,
            sample_rate=facts.sample_rate,
            grad_clip=p.grad_clip,
            should_stop=ctx.should_stop,
            progress=lambda i, n: ctx.progress(0.6 + 0.4 * i / n, f"timed step {i}/{n}"),
        )
        if ctx.should_stop() or not seconds:
            ctx.log("stopped during the timed steps; no calibration written")
            return
        doc = calibration_doc(
            buckets=buckets,
            requested=list(p.bucket_bins),
            ratio=ratio,
            seconds=seconds,
            audio=audio,
            sizes=sizes,
            precision=p.precision,
            applied=applied,
            peak=gpu.peak_mb(),
        )
        outputs["calibration"].write_text(json.dumps(doc, indent=2), encoding="utf-8")
        meta_keys = (
            "family",
            "precision",
            "secondsPerStep",
            "secondsPerStepStd",
            "plusMinus",
            "batchSize",
            "batchSizes",
            "bucketConfig",
            "memoryCapMb",
            "audioSecondsPerStep",
            "peakMemoryMb",
        )
        ctx.set_meta("calibration", {k: doc[k] for k in meta_keys if doc.get(k) is not None})
        ctx.final_metric("seconds_per_step", doc["secondsPerStep"])
        ctx.log(
            "calibrated",
            secondsPerStep=doc["secondsPerStep"],
            std=doc["secondsPerStepStd"],
            batches=doc["bucketBatchSize"],
        )
