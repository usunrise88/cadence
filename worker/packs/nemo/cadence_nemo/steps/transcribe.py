"""nemotron_transcribe — the transcribe role of the Nemotron family: decode every utterance of a dataset with a
checkpoint in streaming simulation at a latency profile, with the real cache-aware streaming decoder, and write
``hypotheses`` (R42): one JSON line per utterance with text, words (start, end, confidence), the decoding config and
its hash, family, weights hash and the partial events. Evals use it from phase 3. Help:
docs/help/steps/nemotron-transcribe.md.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import lang, streaming, training
from cadence_nemo.family import NAME, RUNTIME, att_context_size, profile
from cadence_nemo.mixdata import read_dataset_dir
from cadence_nemo.steps.common import card, device
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class TranscribeParams(BaseModel):
    profile: str = cadence_field(default_ref="packs.nemo.profile")
    batch_size: int = cadence_field(default_ref="packs.nemo.transcribe_batch_size")
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")


def decoding_hash(decoding: Mapping[str, Any]) -> str:
    return "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()


def hypothesis_row(
    audio_hash: str, s: streaming.Stream, decoding: Mapping[str, Any], dhash: str, whash: str
) -> dict[str, Any]:
    return {
        "audio": audio_hash,
        "text": s.text,
        "words": streaming.words_from_partials(s.text, s.partials, s.word_confidence),
        "decoding": dict(decoding),
        "decodingHash": dhash,
        "family": NAME,
        "weightsHash": whash,
        "partials": s.partials,
    }


class TranscribeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint", "data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 2, "jobKind": "eval"}
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
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)

        import torch

        model = training.load_model(inputs["model"] / ck.NEMO_FILE, device()).to(torch.float32)
        facts = training.model_facts(model)
        langs = {c.language for c in part.clips}
        keys = lang.prompt_keys(langs, facts.prompt_dictionary, p.target_lang)
        if len(set(keys.values())) > 1:
            raise StepInputError(f"the dataset mixes languages {sorted(langs)}; transcribe one language per step")
        key = next(iter(keys.values()))
        decoding: dict[str, Any] = {"profile": prof["name"], **streaming.prepare(model, att_context_size(prof), key)}
        dhash = decoding_hash(decoding)
        whash = str(meta.get("weightsHash") or ck.weights_hash(inputs["model"] / ck.NEMO_FILE))
        stride_ms = float(training.to_container(model.cfg.preprocessor).get("window_stride", 0.01)) * 1000
        files = [c.audio for c in part.clips]
        streams = streaming.decode_all(
            model, files, p.batch_size, stride_ms, lambda i, n: ctx.progress(i / n, f"{i}/{n} utterances")
        )
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for c, s in zip(part.clips, streams, strict=True):
                row = hypothesis_row(hash_file(c.audio), s, decoding, dhash, whash)
                f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
        ctx.set_meta(
            "hypotheses",
            {
                "family": NAME,
                "profile": prof["name"],
                "weightsHash": whash,
                "decodingHash": dhash,
                "utterances": len(streams),
                "language": key,
            },
        )
        ctx.log("transcribed", utterances=len(streams), profile=prof["name"])
