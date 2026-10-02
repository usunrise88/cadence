"""nemotron_live — the live role of the Nemotron family: serve one transcription session (R47 to R50; spike A5) as an
interactive job.

The control plane leases the job with up to three targets (lanes A, B, C), each a model input (``model.<n>``, a
checkpoint, or ``base.<n>``, a base model fetched like ``checkpoint_from_base`` does), a latency profile, a language
and an optional boost list (``boost.<n>``). The step loads every distinct model once — targets of one model share its
weights, one pipeline per profile (:mod:`cadence_nemo.pipeline`, the decoder ``nemotron_transcribe@3`` evaluates
with) — then dials the relay (``CADENCE_LIVE_URL`` with the lease's ``CADENCE_LIVE_TOKEN``) and speaks the live channel
(:func:`cadence_worker.live.serve`) until the session ends. It writes nothing; the lease is released done. Help:
docs/help/steps/nemotron-live.md.
"""

from __future__ import annotations

import tempfile
import time
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import gpu, lang, pipeline, streaming
from cadence_nemo.family import NAME, RUNTIME, att_context_size, profile
from cadence_nemo.steps.common import card, device
from cadence_nemo.steps.transcribe import boost_input
from cadence_worker import live
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class NemotronLiveParams(live.LiveParams):
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")
    stop_history_eou_ms: int = cadence_field(default_ref="packs.nemo.live_stop_history_eou_ms")
    boost_weight: float = cadence_field(default_ref="packs.nemo.boost_weight")


def model_file(inputs: Mapping[str, Path], key: str) -> Path:
    """The ``.nemo`` of a target's model input: a checkpoint directory, or a base model (fetched into the HF cache)."""
    p = inputs[key]
    if p.is_dir():
        ck.read_checkpoint(p)
        return p / ck.NEMO_FILE
    return ck.read_base(p, ck.download_base).nemo


def load_targets(
    p: NemotronLiveParams, inputs: Mapping[str, Path], dev: str
) -> tuple[list[pipeline.PipelineStream], dict[str, Any]]:
    """Every target's decoder; one model per distinct model input, one pipeline per profile it is used at."""
    profiles: dict[str, dict[str, list[int]]] = {}
    boosted: dict[str, bool] = {}
    for t in p.targets:
        try:
            prof = profile(t.profile)
        except KeyError as e:
            raise StepInputError(f"target {t.target}: {NAME} has no latency profile {t.profile!r}") from e
        profiles.setdefault(t.model, {})[prof["name"]] = att_context_size(prof)
        boosted[t.model] = boosted.get(t.model, False) or bool(t.boost)
    models: dict[str, pipeline.Model] = {}
    t0 = time.perf_counter()
    for key, profs in profiles.items():
        models[key] = pipeline.load(
            str(model_file(inputs, key)),
            profs,
            stop_history_eou_ms=p.stop_history_eou_ms,
            boosting=boosted[key],
            cuda_graphs=len(profiles) == 1,  # two models' CUDA-graph decoders crash in one process (pipeline_cfg)
            batch_size=4,
            device=dev,
        )
    streams: list[pipeline.PipelineStream] = []
    for t in p.targets:
        m = models[t.model]
        key = lang.resolve_prompt_key(t.language, m.prompt_dictionary())
        boost: streaming.Boost | None = None
        if t.boost:
            boost = boost_input({"boost": inputs[t.boost]}, p.boost_weight)
        streams.append(
            pipeline.PipelineStream(
                target=t.target,
                pipeline=m.pipelines[t.profile],
                att=m.att[t.profile],
                profile=t.profile,
                language=key,
                stop_history_eou_ms=p.stop_history_eou_ms,
                boost_cfg=boost,
                load_s=m.load_s,
            )
        )
    load = {
        "perModelS": {k: round(m.load_s, 2) for k, m in models.items()},
        "totalS": round(time.perf_counter() - t0, 2),
    }
    return streams, load


class LiveStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {
        "model": "checkpoint",
        "base": "base_model",
        "boost": "boost_list",
        "audio": "audio",
    }
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"model", "base", "boost", "audio"})
    produces: ClassVar[Mapping[str, str]] = {}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 6, "diskGb": 6, "jobKind": "interactive"}
    role: ClassVar[str] = "live"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = NemotronLiveParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = NemotronLiveParams.model_validate(params.model_dump())
        live.check_targets(p, inputs)
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        tempfile.tempdir = str(ctx.work_dir)  # a .nemo restore unpacks 2.5 GB: keep it in the lease's scratch
        ctx.progress(0.0, "loading the targets")
        streams, load = load_targets(p, inputs, device())
        ctx.log("targets loaded", session=p.session, **load)
        if ctx.should_stop():
            return
        ctx.progress(0.5, "waiting for the session")
        channel = live.dial()
        ctx.progress(1.0, "live")
        try:
            summary = live.serve(
                channel,
                streams,
                p,
                work_dir=ctx.work_dir,
                should_stop=ctx.should_stop,
                audio_path=inputs.get("audio"),
                gpu=gpu.peak_mb,
                load=load,
            )
        finally:
            channel.close(1000, "the job ended")
        if summary is None:
            ctx.log("session ended without a summary (the socket closed or the job was stopped)", session=p.session)
        else:
            ctx.log("session ended", session=p.session, audioS=summary.get("audioS"), rtf=summary.get("rtf"))
