"""nemotron_transcribe — the transcribe role of the Nemotron family: decode every utterance of a dataset with a
checkpoint in streaming at a latency profile and write ``hypotheses`` (R42): one JSON line per utterance with text,
words (start, end, confidence), the decoding config and its hash, family, weights hash and the partial events.

Version 3 decodes through NeMo's cache-aware streaming pipeline (:mod:`cadence_nemo.pipeline`), the decoder live
sessions use (``nemotron_live``), so an eval, a paced replay and a live session give the same words (spike A5 finding
2). Each file streams as one pipeline stream, closed with a forced end of utterance; the utterances of a batch step
together. The decoding config names the decoder (``"decoder": "nemo-pipeline-cache-aware"``), so records of
versions 1 and 2 (NeMo's cache-aware loop) never mix with these: their decoding hashes differ. Static phrase boosting
(R24, capability ``boosting: nemo-phrase-boosting``) is the pipeline's per-stream boosting tree from the optional
``boost`` input (a ``boost_list`` artifact), with the list's weight or ``boost_weight``; the list's hash and weight
enter the decoding config and so its hash. Help: docs/help/steps/nemotron-transcribe.md.
"""

from __future__ import annotations

import hashlib
import json
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import lang, pipeline, streaming
from cadence_nemo.family import NAME, RUNTIME, att_context_size, profile
from cadence_nemo.mixdata import read_dataset_dir
from cadence_nemo.steps.common import card, device
from cadence_worker import audio as audio_io
from cadence_worker import boost as boost_list
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.resample import resample_poly
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class TranscribeParams(BaseModel):
    profile: str = cadence_field(default_ref="packs.nemo.profile")
    batch_size: int = cadence_field(default_ref="packs.nemo.transcribe_batch_size")
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")
    boost_weight: float = cadence_field(default_ref="packs.nemo.boost_weight")
    stop_history_eou_ms: int = cadence_field(default_ref="packs.nemo.live_stop_history_eou_ms")


def decoding_hash(decoding: Mapping[str, Any]) -> str:
    return "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()


def decoding_config(
    prof: Mapping[str, Any], key: str, stop_history_eou_ms: int, boost: streaming.Boost | None
) -> dict[str, Any]:
    """The decoding a hypotheses row records (and its hash keys eval records by)."""
    out: dict[str, Any] = {
        "profile": prof["name"],
        "decoder": pipeline.DECODER,
        "attContextSize": att_context_size(prof),  # type: ignore[arg-type]
        "targetLang": key,
        "stripLangTags": True,
        "stopHistoryEouMs": stop_history_eou_ms,
        "wordConfidence": "min",
        "wordTimestamps": "pipeline-segments",
    }
    if boost is not None:
        out["boost"] = boost.decoding()
    return out


def hypothesis_row(
    audio_hash: str, r: pipeline.FileResult, decoding: Mapping[str, Any], dhash: str, whash: str
) -> dict[str, Any]:
    return {
        "audio": audio_hash,
        "text": r.text,
        "words": r.words,
        "decoding": dict(decoding),
        "decodingHash": dhash,
        "family": NAME,
        "weightsHash": whash,
        "partials": r.partials,
    }


def boost_input(inputs: Mapping[str, Path], default_weight: float) -> streaming.Boost | None:
    """The optional ``boost`` input as the decoder's boost, or None when the pipeline wired none ."""
    if "boost" not in inputs:
        return None
    try:
        bl = boost_list.read(inputs["boost"])
    except boost_list.BoostListError as e:
        raise StepInputError(str(e)) from e
    return streaming.Boost(terms=bl.terms, weight=default_weight if bl.weight is None else bl.weight, list_hash=bl.hash)


def read_clip(path: Path) -> np.ndarray[Any, np.dtype[np.float32]]:
    """A dataset clip as 16 kHz float mono (channel 0), as a live session would hear it."""
    a = audio_io.read(path)
    x = np.asarray(a.samples, dtype=np.float32)
    if a.channels > 1:
        x = x[: x.size - x.size % a.channels].reshape(-1, a.channels)[:, 0]
    return resample_poly(x, a.sample_rate, pipeline.SR)


class TranscribeStep:
    version: ClassVar[str] = "3"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint", "data": "dataset", "boost": "boost_list"}
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"boost"})
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 4, "jobKind": "eval"}
    role: ClassVar[str] = "transcribe"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = TranscribeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = TranscribeParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"{NAME} has no latency profile {p.profile!r}") from e
        meta = ck.read_checkpoint(inputs["model"])
        part = read_dataset_dir(inputs["data"])
        boost = boost_input(inputs, p.boost_weight)
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        # Restoring a .nemo unpacks it into the temp directory (2.5 GB): keep that in the lease's scratch.
        tempfile.tempdir = str(ctx.work_dir)
        ctx.progress(0.0, "loading the model")
        model = pipeline.load(
            str(inputs["model"] / ck.NEMO_FILE),
            {prof["name"]: att_context_size(prof)},
            stop_history_eou_ms=p.stop_history_eou_ms,
            boosting=boost is not None,
            batch_size=p.batch_size,
            device=device(),
        )
        langs = {c.language for c in part.clips}
        keys = lang.prompt_keys(langs, model.prompt_dictionary(), p.target_lang)
        if len(set(keys.values())) > 1:
            raise StepInputError(f"the dataset mixes languages {sorted(langs)}; transcribe one language per step")
        key = next(iter(keys.values()))
        decoding = decoding_config(prof, key, p.stop_history_eou_ms, boost)
        dhash = decoding_hash(decoding)
        whash = str(meta.get("weightsHash") or ck.weights_hash(inputs["model"] / ck.NEMO_FILE))
        pipe = model.pipelines[prof["name"]]
        att = model.att[prof["name"]]
        clips = part.clips
        n = len(clips)
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for i in range(0, n, p.batch_size):
                batch = clips[i : i + p.batch_size]
                streams = [
                    pipeline.PipelineStream(
                        target="A",
                        pipeline=pipe,
                        att=att,
                        profile=prof["name"],
                        language=key,
                        stop_history_eou_ms=p.stop_history_eou_ms,
                        boost_cfg=boost,
                    )
                    for _ in batch
                ]
                results = pipeline.decode_batch(streams, [read_clip(c.audio) for c in batch])
                for c, r in zip(batch, results, strict=True):
                    row = hypothesis_row(hash_file(c.audio), r, decoding, dhash, whash)
                    f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
                done = min(n, i + p.batch_size)
                ctx.progress(done / max(n, 1), f"{done}/{n} utterances")
        out_meta: dict[str, Any] = {
            "family": NAME,
            "profile": prof["name"],
            "weightsHash": whash,
            "decodingHash": dhash,
            "decoder": pipeline.DECODER,
            "utterances": n,
            "language": key,
        }
        if boost is not None:
            out_meta["boost"] = {"list": boost.list_hash, "weight": boost.weight, "terms": len(boost.terms)}
        ctx.set_meta("hypotheses", out_meta)
        ctx.log(
            "transcribed", utterances=n, profile=prof["name"], boosted=boost is not None, loadS=round(model.load_s, 1)
        )
