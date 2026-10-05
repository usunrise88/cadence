"""nemotron_parity — the parity reference of the Nemotron family (phase 5 · stream D1; R31, spike E1): the parity
sample decoded exactly as ``nemotron_transcribe@4`` decodes an eval (NeMo's cache-aware streaming pipeline, fp32 at
matmul precision "highest", the eval's batch of 8), with each utterance's token ids, so ``parity_score`` can compare
them with the tokens the served engine returned for the same audio.

``tokens`` is the hypothesis the pipeline hands its greedy decoder on a stream's last step (endpointing never resets
the RNN-T state, so it covers the whole stream; the trailing locale tag included) — spike E1's capture, on which the
pipeline's joined finals equalled the detokenised sequence for 200/200 clips.

The ``smoke`` output (``cadence.smoke-inputs/1``) holds, for the first ``smoke_utterances`` utterances in dataset
order, the feature buffers this decoder sends (``cadence.nemo-chunks/1``: pre-encode cache and chunk, right-padded to
the profile's buffer): the inputs the delivery script's smoke check streams through the production server.
Help: docs/help/steps/nemotron-parity.md.
"""

from __future__ import annotations

import json
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import deploy, lang, pipeline
from cadence_nemo.family import NAME, RUNTIME, att_context_size, profile
from cadence_nemo.mixdata import read_dataset_dir
from cadence_nemo.steps.common import card, device
from cadence_nemo.steps.transcribe import decoding_config, decoding_hash, hypothesis_row, read_clip
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext


class ParityParams(BaseModel):
    profile: str = cadence_field(default_ref="packs.nemo.profile")
    batch_size: int = cadence_field(
        default_ref="packs.nemo.transcribe_batch_size",
        description="Utterances streamed together; parity compares at the eval's batch (spike E1)",
    )
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")
    stop_history_eou_ms: int = cadence_field(default_ref="packs.nemo.live_stop_history_eou_ms")
    smoke_utterances: int = cadence_field(default_ref="packs.nemo.parity_smoke_utterances")


def capture_tokens() -> dict[int, list[int]]:
    """Record each pipeline stream's hypothesis on its last step, by stream id (patches the pipeline class once)."""
    from nemo.collections.asr.inference.pipelines.cache_aware_rnnt_pipeline import CacheAwareRNNTPipeline

    cls: Any = CacheAwareRNNTPipeline
    if not getattr(cls, "_cadence_tokens_patch", False):
        orig = cls.run_greedy_decoder

        def run_greedy_decoder(self: Any, state: Any, request: Any, hyp: Any) -> bool:
            if request.is_last:
                y = hyp.y_sequence
                cls._cadence_tokens[int(request.stream_id)] = [
                    int(v) for v in (y.tolist() if hasattr(y, "tolist") else y)
                ]
            return bool(orig(self, state, request, hyp))

        cls.run_greedy_decoder = run_greedy_decoder
        cls._cadence_tokens = {}
        cls._cadence_tokens_patch = True
    got: dict[int, list[int]] = cls._cadence_tokens
    return got


def stream_chunks(stream: pipeline.PipelineStream, audio: Any) -> list[tuple[Any, int, bool]]:
    """The feature buffers the pipeline decoder sends for one whole utterance: (features, valid frames, first)."""
    stream._open()
    assert stream.feats is not None
    stream.feats.push(audio)
    stream.feats.finish()
    out = [
        (c.features.detach().float().cpu().numpy(), int(c.length), bool(c.first)) for c in stream._chunks(final=True)
    ]
    stream.feats = None
    stream.stream_open = False
    return out


class ParityStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint", "data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "smoke": "smoke_inputs"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 4, "jobKind": "eval"}
    role: ClassVar[str] = "parity"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = ParityParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ParityParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"{NAME} has no latency profile {p.profile!r}") from e
        meta = ck.read_checkpoint(inputs["model"])
        part = read_dataset_dir(inputs["data"])
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        tempfile.tempdir = str(ctx.work_dir)
        ctx.progress(0.0, "loading the model")
        model = pipeline.load(
            str(inputs["model"] / ck.NEMO_FILE),
            {prof["name"]: att_context_size(prof)},
            stop_history_eou_ms=p.stop_history_eou_ms,
            batch_size=p.batch_size,
            device=device(),
        )
        prompts = model.prompt_dictionary()
        keys = lang.prompt_keys({c.language for c in part.clips}, prompts, p.target_lang)
        if len(set(keys.values())) > 1:
            raise StepInputError(f"the dataset mixes languages {sorted(keys)}; compare one language per step")
        key = next(iter(keys.values()))
        decoding = decoding_config(prof, key, p.stop_history_eou_ms, None)
        dhash = decoding_hash(decoding)
        whash = str(meta.get("weightsHash") or ck.weights_hash(inputs["model"] / ck.NEMO_FILE))
        pipe = model.pipelines[prof["name"]]
        att = model.att[prof["name"]]
        got = capture_tokens()
        clips = part.clips
        n = len(clips)

        def new_stream() -> pipeline.PipelineStream:
            return pipeline.PipelineStream(
                target="A",
                pipeline=pipe,
                att=att,
                profile=prof["name"],
                language=key,
                stop_history_eou_ms=p.stop_history_eou_ms,
            )

        missing = 0
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for i in range(0, n, p.batch_size):
                batch = clips[i : i + p.batch_size]
                streams = [new_stream() for _ in batch]
                results = pipeline.decode_batch(streams, [read_clip(c.audio) for c in batch])
                for c, s, r in zip(batch, streams, results, strict=True):
                    row = hypothesis_row(hash_file(c.audio), r, decoding, dhash, whash)
                    toks = got.pop(s.stream_id, None)
                    if toks is None:
                        missing += 1
                        toks = []
                    row["tokens"] = toks
                    f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
                done = min(n, i + p.batch_size)
                ctx.progress(0.9 * done / max(n, 1), f"{done}/{n} utterances")
        if missing:
            ctx.log(f"{missing} utterances have no captured token sequence (empty audio)", level="warn")
        # The smoke inputs: what this decoder sends for the first utterances, one stream each.
        smoke = outputs["smoke"]
        smoke.mkdir(parents=True, exist_ok=True)
        frames = int(pipe.expected_feature_buffer_len)
        items: list[dict[str, str]] = []
        for k, c in enumerate(clips[: max(0, p.smoke_utterances)]):
            chunks = stream_chunks(new_stream(), read_clip(c.audio))
            name = deploy.smoke_name(k)
            deploy.write_json(smoke / name, deploy.encode_chunks(prompts[key], frames, chunks))
            items.append({"audio": hash_file(c.audio), "file": name})
        deploy.write_json(smoke / deploy.SMOKE_JSON, deploy.smoke_doc(items))
        ctx.set_meta(
            "hypotheses",
            {
                "family": NAME,
                "profile": prof["name"],
                "weightsHash": whash,
                "decodingHash": dhash,
                "decoder": pipeline.DECODER,
                "utterances": n,
                "language": key,
                "tokens": True,
                "batchSize": p.batch_size,
            },
        )
        ctx.set_meta("smoke", {"schema": deploy.SMOKE_SCHEMA, "format": deploy.CHUNKS_FORMAT, "items": len(items)})
        ctx.progress(1.0, f"{n} utterances, {len(items)} smoke inputs")
