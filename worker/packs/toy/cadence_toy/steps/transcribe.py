"""toy_transcribe — the transcribe role of toy-ctc: decode every utterance of a dataset with a checkpoint at a latency
profile and write ``hypotheses`` (R42): one JSON line per utterance with text, words (start, end, confidence), the
decoding config and its hash, family and weights hash; the streaming profile adds partial events (audio offset, emit
time, text). Help: docs/help/steps/toy-transcribe.md.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_toy.data import read_dataset
from cadence_toy.family import NAME, RUNTIME, profile
from cadence_toy.model import decode_offline, decode_streaming, load_checkpoint, use_one_thread, weights_hash
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class TranscribeParams(BaseModel):
    profile: str = cadence_field(default_ref="packs.toy.profile")


class TranscribeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint", "data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    role: ClassVar[str] = "transcribe"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = TranscribeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        p = TranscribeParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"toy-ctc has no latency profile {p.profile!r}") from e
        model, tok = load_checkpoint(inputs["model"])
        whash = weights_hash(inputs["model"] / "model.pt")
        utts = read_dataset(inputs["data"])
        decoding: dict[str, Any] = {"profile": prof["name"], "decoder": "greedy-ctc", **(prof.get("params") or {})}
        decoding_hash = "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()
        chunk_ms = prof.get("chunkMs")
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for i, u in enumerate(utts):
                row: dict[str, Any] = {"audio": u.audio}
                if chunk_ms:
                    dec, partials = decode_streaming(model, tok, u.samples, chunk_ms)
                    row["partials"] = partials
                else:
                    dec = decode_offline(model, tok, u.samples)
                row.update(
                    {
                        "text": dec.text(),
                        "words": dec.words(),
                        "decoding": decoding,
                        "decodingHash": decoding_hash,
                        "family": NAME,
                        "weightsHash": whash,
                    }
                )
                f.write(json.dumps(row, separators=(",", ":")) + "\n")
                ctx.progress((i + 1) / len(utts), f"{i + 1}/{len(utts)} utterances")
        ctx.set_meta(
            "hypotheses",
            {
                "family": NAME,
                "profile": prof["name"],
                "weightsHash": whash,
                "decodingHash": decoding_hash,
                "utterances": len(utts),
            },
        )
