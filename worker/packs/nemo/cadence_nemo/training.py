"""NeMo glue for the calibrate and train steps: load the model, build the Lhotse data configurations from a mix, the
optimiser from a peak learning rate, and a Lightning trainer whose callback reports through the step context.

The config builders are plain functions over dicts (tested without NeMo); everything that imports NeMo, Lightning or
OmegaConf does so inside a function, so this module imports anywhere. Settings follow spike A3's run
(docs/spikes/a3/run_train.sh): bf16-mixed, AdamW + NoamAnnealing, Lhotse bucketing with OOMptimizer bucket sizes,
``unified`` prompt mode (50 % ``auto``), validation with the language prompt.
"""

from __future__ import annotations

import contextlib
import math
from collections.abc import Callable, Iterator, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from cadence_worker.steps.base import StepInputError

PRECISION = {"bf16": "bf16-mixed", "fp16": "16-mixed", "fp32": "32-true"}
AUTOCAST = {"bf16": "bfloat16", "fp16": "float16", "fp32": "float32"}


@dataclass(frozen=True)
class ModelFacts:
    prompt_dictionary: dict[str, int]
    num_prompts: int
    subsampling_factor: int
    d_model: int
    sample_rate: int
    vocab_size: int


def load_model(nemo: Path, map_location: str = "cpu", trainer: Any = None) -> Any:
    from nemo.collections.asr.models import ASRModel

    return ASRModel.restore_from(restore_path=str(nemo), map_location=map_location, trainer=trainer)


def to_container(cfg: Any) -> dict[str, Any]:
    if cfg is None:
        return {}
    if isinstance(cfg, Mapping):
        return {str(k): v for k, v in cfg.items()}
    from omegaconf import OmegaConf

    out = OmegaConf.to_container(cfg, resolve=True)
    return dict(out) if isinstance(out, dict) else {}


def model_facts(model: Any) -> ModelFacts:
    cfg = model.cfg
    defaults = to_container(cfg.get("model_defaults"))
    prompts = {str(k): int(v) for k, v in dict(defaults.get("prompt_dictionary") or {}).items()}
    if not prompts:
        raise StepInputError("the model has no prompt_dictionary; it is not a Nemotron prompt model")
    encoder = to_container(cfg.get("encoder"))
    optim = to_container(cfg.get("optim"))
    sched = dict(optim.get("sched") or {})
    return ModelFacts(
        prompt_dictionary=prompts,
        num_prompts=int(defaults.get("num_prompts") or cfg.get("num_prompts") or 128),
        subsampling_factor=int(encoder.get("subsampling_factor") or cfg.get("subsampling_factor") or 8),
        d_model=int(encoder.get("d_model") or sched.get("d_model") or 1024),
        sample_rate=int(cfg.get("sample_rate") or 16000),
        vocab_size=int(model.tokenizer.vocab_size),
    )


def single_piece(model: Any, key: str) -> bool:
    """Whether ``<key>`` is one piece of the model's sentencepiece vocabulary (then training text ends with it; the
    tokenizer writes `` <he-IL>`` as ``▁`` + ``<he-IL>``, spike A3's format)."""
    sp: Any = getattr(model.tokenizer, "tokenizer", None)
    try:
        return int(sp.piece_to_id(f"<{key}>")) != int(sp.unk_id())
    except Exception:
        return False


def scaled_batches(batches: Sequence[int], batch_scale: float, min_speed: float) -> list[int]:
    """Bucket batch sizes for this attempt: the OOM retry's scale, and room for speed perturbation below 1 (a slower
    clip is longer than its bucket's bound)."""
    factor = batch_scale * min(1.0, min_speed)
    return [max(1, math.floor(b * factor)) for b in batches]


def train_ds_config(
    base: Mapping[str, Any],
    *,
    input_cfg: list[dict[str, Any]],
    bins: Sequence[float],
    batches: Sequence[int],
    max_duration: float,
    min_duration: float,
    num_workers: int,
    seed: int,
    prompt_mode: str,
    auto_ratio: float,
    facts: ModelFacts,
) -> dict[str, Any]:
    """The training ``train_ds``: the base model's own section with the mix as Lhotse ``input_cfg`` (plain NeMo
    manifests of blob paths, not tarred) and the calibrated buckets."""
    if len(bins) != len(batches) or not bins:
        raise StepInputError("the calibration has no bucket_duration_bins / bucket_batch_size of equal length")
    cfg = dict(base)
    cfg.update(
        {
            "use_lhotse": True,
            "input_cfg": input_cfg,
            "manifest_filepath": None,
            "is_tarred": False,
            "tarred_audio_filepaths": None,
            "shard_manifests": False,
            "use_bucketing": True,
            "bucket_duration_bins": [float(b) for b in bins],
            "bucket_batch_size": [int(b) for b in batches],
            "num_buckets": len(bins),
            "batch_size": None,
            "batch_duration": None,
            "quadratic_duration": None,
            "bucket_buffer_size": 4000,
            "shuffle_buffer_size": 4000,
            "max_duration": float(max_duration),
            "min_duration": float(min_duration),
            "num_workers": int(num_workers),
            "shuffle": True,
            "seed": int(seed),
            "shard_seed": int(seed),
            "lang_field": "target_lang",
            "prompt_field": "target_lang",
            "prompt_dictionary": dict(facts.prompt_dictionary),
            "num_prompts": facts.num_prompts,
            "subsampling_factor": facts.subsampling_factor,
            "default_prompt_mode": prompt_mode,
            "unified_auto_ratio": float(auto_ratio),
            "sample_rate": facts.sample_rate,
            "defer_setup": False,
        }
    )
    return cfg


def val_ds_config(
    base: Mapping[str, Any], *, manifest: Path, batch_size: int, num_workers: int, facts: ModelFacts
) -> dict[str, Any]:
    cfg = dict(base)
    cfg.update(
        {
            "use_lhotse": True,
            "manifest_filepath": str(manifest),
            "is_tarred": False,
            "tarred_audio_filepaths": None,
            "use_bucketing": False,
            "batch_size": int(batch_size),
            "batch_duration": None,
            "max_cuts": None,
            "shuffle": False,
            "num_workers": int(num_workers),
            "lang_field": "target_lang",
            "prompt_field": "target_lang",
            "prompt_dictionary": dict(facts.prompt_dictionary),
            "num_prompts": facts.num_prompts,
            "subsampling_factor": facts.subsampling_factor,
            "default_prompt_mode": "langID",
            "sample_rate": facts.sample_rate,
        }
    )
    return cfg


def optim_config(
    base: Mapping[str, Any], *, scale: float, warmup_steps: int, min_lr: float, weight_decay: float, d_model: int
) -> dict[str, Any]:
    """AdamW + NoamAnnealing with the scale derived from the peak (cadence_nemo.noam)."""
    cfg = dict(base)
    cfg.update({"name": "adamw", "lr": float(scale), "weight_decay": float(weight_decay)})
    cfg.setdefault("betas", [0.9, 0.98])
    cfg["sched"] = {
        "name": "NoamAnnealing",
        "d_model": int(d_model),
        "warmup_steps": int(warmup_steps),
        "warmup_ratio": None,
        "min_lr": float(min_lr),
    }
    return cfg


def setup_train_dataloader(model: Any, cfg: dict[str, Any], profile: Any) -> None:
    """The model's training dataloader with :class:`~cadence_nemo.nemo_data.AugmentingDataset` (the model's own
    ``_setup_dataloader_from_config`` with the dataset swapped)."""
    from nemo.collections.common.data.lhotse import get_lhotse_dataloader_from_config
    from omegaconf import DictConfig, OmegaConf

    from cadence_nemo.nemo_data import AugmentingDataset

    oc = OmegaConf.create(cfg)
    model._update_dataset_config(dataset_name="train", config=oc)
    dataset_cfg = dict(cfg)
    dataset_cfg["encoder"] = to_container(model.cfg.encoder)
    dataset = AugmentingDataset(model.tokenizer, dataset_cfg, profile, int(cfg["sample_rate"]))
    model._train_dl = get_lhotse_dataloader_from_config(
        DictConfig(oc), global_rank=0, world_size=1, dataset=dataset, tokenizer=model.tokenizer
    )
    ignore_sigterm_in_workers(model._train_dl)


def setup_val_dataloader(model: Any, cfg: dict[str, Any]) -> None:
    from omegaconf import OmegaConf

    model.setup_validation_data(OmegaConf.create(cfg))
    ignore_sigterm_in_workers(getattr(model, "_validation_dl", None))


def setup_optimization(model: Any, cfg: dict[str, Any]) -> Any:
    from omegaconf import OmegaConf

    return model.setup_optimization(OmegaConf.create(cfg))


def strip_lang_tags(model: Any) -> None:
    decoding = getattr(model, "decoding", None)
    if decoding is not None and hasattr(decoding, "set_strip_lang_tags"):
        decoding.set_strip_lang_tags(True)


def grad_norm(module: Any) -> float:
    import torch

    norms = [p.grad.detach().float().norm() for p in module.parameters() if p.grad is not None]
    if not norms:
        return float("nan")
    return float(torch.linalg.vector_norm(torch.stack(norms)))


def direct_checkpoint_io() -> Any:
    """Lightning's checkpoint IO without its in-memory copy: ``TorchCheckpointIO`` serialises the whole checkpoint into
    a BytesIO before writing it (7.7 GB for this model: about 45 s and twice the host memory), which does not fit a
    stop grace; this writes it straight to a temporary file and renames it."""
    import os

    import torch
    from lightning.pytorch.plugins import TorchCheckpointIO

    class DirectCheckpointIO(TorchCheckpointIO):  # type: ignore[misc]
        def save_checkpoint(self, checkpoint: dict[str, Any], path: Any, storage_options: Any = None) -> None:
            target = Path(path)
            target.parent.mkdir(parents=True, exist_ok=True)
            tmp = target.with_name(target.name + ".part")
            torch.save(checkpoint, tmp)
            os.replace(tmp, target)

    return DirectCheckpointIO()


def make_trainer(
    *,
    steps: int,
    precision: str,
    val_every: int,
    grad_clip: float,
    callbacks: list[Any],
    log_every_n_steps: int = 50,
) -> Any:
    import lightning.pytorch as pl
    import torch

    trainer = pl.Trainer(
        accelerator="gpu" if torch.cuda.is_available() else "cpu",
        devices=1,
        strategy="auto",
        precision=PRECISION[precision],
        max_steps=steps,
        max_epochs=-1,
        val_check_interval=max(1, min(val_every, steps)),
        check_val_every_n_epoch=None,
        num_sanity_val_steps=0,
        gradient_clip_val=grad_clip if grad_clip > 0 else None,
        accumulate_grad_batches=1,
        logger=False,
        enable_checkpointing=False,
        enable_progress_bar=False,
        enable_model_summary=False,
        log_every_n_steps=log_every_n_steps,
        use_distributed_sampler=False,
        callbacks=callbacks,
        plugins=[direct_checkpoint_io()],
    )
    # The harness owns SIGTERM (run_step sets the step's stop event; the callback stops at the next step with a
    # training state). Lightning's own handler would raise SIGTERMException at the end of the step and exit before a
    # state is written, so it is not installed.
    connector = getattr(trainer, "_signal_connector", None)
    if connector is not None:
        connector.register_signal_handlers = lambda: None
    return trainer


def _ignore_sigterm(worker_id: int) -> None:
    import signal

    signal.signal(signal.SIGTERM, signal.SIG_IGN)


def ignore_sigterm_in_workers(loader: Any) -> None:
    """Dataloader workers share the step's process group, which receives the stop signal: they ignore it and end with
    the loader (the stop is the main process's business)."""
    if loader is None or not hasattr(loader, "worker_init_fn"):
        return
    inner = loader.worker_init_fn

    def init(worker_id: int) -> None:
        _ignore_sigterm(worker_id)
        if inner is not None:
            inner(worker_id)

    loader.worker_init_fn = init


class Hooks:
    """What the Lightning callback asks the train step to do (save a training state, register a validation's
    checkpoint)."""

    def save_state(self, trainer: Any, step: int) -> None:
        raise NotImplementedError

    def validated(self, module: Any, step: int, wer: float, best: bool) -> None:
        """After each validation pass (not the sanity check): ``best`` when its WER is the lowest so far."""
        raise NotImplementedError

    def on_stop(self, trainer: Any, step: int) -> None:
        raise NotImplementedError


def lightning_callback(monitor: Any, hooks: Hooks, sample_rate: int) -> Any:
    """A Lightning callback that feeds :class:`~cadence_nemo.monitor.TrainingMonitor` and runs the hooks."""
    import lightning.pytorch as pl
    import torch

    mib = 1024 * 1024

    class Reporter(pl.Callback):  # type: ignore[misc]
        def __init__(self) -> None:
            self.grad: float | None = None

        def on_train_batch_start(self, trainer: Any, module: Any, batch: Any, batch_idx: int) -> None:
            with contextlib.suppress(TypeError, IndexError, AttributeError):
                monitor.batch_audio(float(batch[1].sum()) / sample_rate)

        def on_before_optimizer_step(self, trainer: Any, module: Any, optimizer: Any) -> None:
            self.grad = grad_norm(module) if monitor.is_log_step(trainer.global_step + 1) else None

        def on_train_batch_end(self, trainer: Any, module: Any, outputs: Any, batch: Any, batch_idx: int) -> None:
            step = int(trainer.global_step)
            loss = float("nan")
            if isinstance(outputs, Mapping) and "loss" in outputs:
                loss = float(outputs["loss"])
            elif torch.is_tensor(outputs):
                loss = float(outputs)
            lr = float(trainer.optimizers[0].param_groups[0]["lr"]) if trainer.optimizers else float("nan")
            mem = None
            if torch.cuda.is_available():
                mem = torch.cuda.max_memory_reserved() / mib
                if monitor.is_log_step(step):
                    torch.cuda.reset_peak_memory_stats()
            monitor.step_end(step, loss, lr, self.grad, mem)
            if monitor.stop_requested():
                hooks.on_stop(trainer, step)
                monitor.stopped = True
                trainer.should_stop = True
                return
            if monitor.state_due():
                hooks.save_state(trainer, step)

        def on_validation_end(self, trainer: Any, module: Any) -> None:
            if trainer.sanity_checking:
                return
            wer = trainer.callback_metrics.get("val_wer")
            if wer is None:
                return
            step = int(trainer.global_step)
            best = monitor.validation(step, float(wer))
            hooks.validated(module, step, float(wer), best)

    return Reporter()


DATA_PATH_KEYS = (
    ("train_ds", "input_cfg"),
    ("train_ds", "manifest_filepath"),
    ("validation_ds", "input_cfg"),
    ("validation_ds", "manifest_filepath"),
)


def clean_data_config(model: Any) -> dict[tuple[str, str], Any]:
    """Drop the lease's scratch paths from the configuration saved inside the .nemo; returns what it dropped."""
    from omegaconf import open_dict

    dropped: dict[tuple[str, str], Any] = {}
    with open_dict(model.cfg):
        for section, key in DATA_PATH_KEYS:
            if section in model.cfg and model.cfg[section] is not None and key in model.cfg[section]:
                dropped[(section, key)] = model.cfg[section][key]
                model.cfg[section][key] = None
    return dropped


@contextlib.contextmanager
def portable_config(model: Any) -> Iterator[None]:
    """The data paths dropped for a save during training (a validation checkpoint), then put back."""
    from omegaconf import open_dict

    dropped = clean_data_config(model)
    try:
        yield
    finally:
        with open_dict(model.cfg):
            for (section, key), value in dropped.items():
                model.cfg[section][key] = value


def install_raw_reference_wer(model: Any, texts: Sequence[str]) -> int:
    """Validation WER against the validation manifest's raw texts, normalised as evaluations are
    (:mod:`cadence_nemo.valwer`), on every WER metric the model validates with; returns how many it patched."""
    from cadence_nemo import valwer

    metrics: list[Any] = []
    for m in (getattr(model, "wer", None), getattr(getattr(model, "joint", None), "_wer", None)):
        if m is not None and hasattr(m, "decoding") and all(m is not x for x in metrics):
            metrics.append(m)
    if not metrics:
        return 0
    decoding = metrics[0].decoding
    refs = valwer.RawReferences(texts, lambda t: str(decoding.decode_ids_to_str(model.tokenizer.text_to_ids(t))))
    for m in metrics:
        valwer.install(m, refs)
    return len(metrics)


def save_nemo(model: Any, d: Path) -> None:
    """Write ``model.nemo`` of the model as it is now into directory d, during training."""
    from cadence_nemo.checkpoint import NEMO_FILE

    d.mkdir(parents=True, exist_ok=True)
    with portable_config(model):
        model.save_to(str(d / NEMO_FILE))


def timed_steps(
    model: Any,
    optimizer: Any,
    dataloader: Any,
    *,
    warmup: int,
    steps: int,
    precision: str,
    sample_rate: int,
    grad_clip: float,
    should_stop: Callable[[], bool],
    progress: Callable[[int, int], None],
) -> tuple[list[float], list[float], list[int]]:
    """Real optimiser steps on the mix's data with the calibrated buckets: per timed step its seconds, audio seconds
    and batch size (the warm-up steps are not timed)."""
    import time

    import torch

    dtype = getattr(torch, AUTOCAST[precision])
    seconds: list[float] = []
    audio: list[float] = []
    sizes: list[int] = []
    model.train()
    it = iter(dataloader)
    total = warmup + steps
    for i in range(total):
        if should_stop():
            break
        try:
            batch = next(it)
        except StopIteration:
            it = iter(dataloader)
            batch = next(it)
        batch = tuple(t.cuda(non_blocking=True) if torch.is_tensor(t) else t for t in batch)
        torch.cuda.synchronize()
        t0 = time.perf_counter()
        with torch.autocast("cuda", dtype=dtype, enabled=precision != "fp32"):
            out = model.training_step(batch, i)
        loss = out["loss"]
        (loss if loss.dim() == 0 else loss.mean()).backward()  # per-utterance losses outside a Lightning loop
        if grad_clip > 0:
            torch.nn.utils.clip_grad_norm_(model.parameters(), grad_clip)
        optimizer.step()
        optimizer.zero_grad(set_to_none=True)
        torch.cuda.synchronize()
        if i >= warmup:
            seconds.append(time.perf_counter() - t0)
            audio.append(float(batch[1].sum()) / sample_rate)
            sizes.append(int(batch[0].shape[0]))
        progress(i + 1, total)
    return seconds, audio, sizes
