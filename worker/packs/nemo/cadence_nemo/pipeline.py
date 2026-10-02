"""One decoder for live sessions and evals (spike A5 finding 2): NeMo's cache-aware streaming pipeline API
(``nemo.collections.asr.inference``, NeMo Speech 26.07 / NeMo 3.0.0) with two shims.

A :class:`PipelineStream` turns 16 kHz float audio pushed in any frame size into chunk-sized ``Frame``\\ s of one
pipeline stream and the pipeline's outputs into the live channel's events (R48): ``partial`` replaces the segment's
previous partial, ``final`` never changes, every event states the audio it covers (``audioEnd``). ``finalize`` pads
the pending chunk with silence and sends it as the stream's last frame (the pipeline flushes with
``keep_all_outputs`` and forces an end of utterance); the next audio opens a new pipeline stream at the same audio
clock, with a fresh encoder cache — a segment boundary, not just a flush.

``nemotron_live`` serves sessions with it and ``nemotron_transcribe@3`` decodes golden sets with it
(:func:`decode_batch`), so a live session, a paced replay and an eval give the same words (A5: 22/22 clips).

Shims, both NeMo 3.0.0 gaps that belong upstream:

1. :func:`patch_prompt`: ``CacheAwareRNNTInferenceWrapper.execute_step`` takes the per-stream ``prompt_vectors`` the
   pipeline builds from ``ASRRequestOptions.language_code`` but never applies them, so a prompt model such as
   Nemotron 3.5 gets un-prompted encoder output and emits only blanks. The patch applies the model's
   ``prompt_kernel`` per stream, as ``PromptStreamingMixin._apply_prompt_to_encoded`` does for its single index.
2. The pipeline keeps the model's trailing locale tag (``… <he-IL>``) in its text and words: :func:`lang.strip_tags`.

Boosting: the pipeline supports per-stream phrase boosting (``ASRRequestOptions.biasing_cfg``, a GPU boosting tree
per stream in the label-looping greedy decoder's multi-model) when the decoding config enables
``greedy.enable_per_stream_biasing``; :func:`pipeline_cfg` enables it when a target boosts, so boost on and boost off
can share one pipeline.

Targets of one checkpoint share one model: a pipeline per latency profile over the same ``asr_model``, with
``att_context_size`` switched on the encoder before each step (:func:`activate`; it only recomputes the streaming
config).
"""

from __future__ import annotations

import itertools
import time
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

import numpy as np
from numpy.typing import NDArray

from cadence_nemo.lang import strip_tags
from cadence_nemo.streaming import Boost

SR = 16000
DECODER = "nemo-pipeline-cache-aware"
Audio = NDArray[np.float32]
Event = dict[str, Any]

# Stream ids are unique per process: two targets may share one pipeline (boost on and off at one profile).
_stream_ids = itertools.count(1)


def biasing_request(boost: Boost) -> Any:
    """NeMo's per-stream biasing request for a boost list (its boosting tree is cached per list and weight within the
    process, so boost on and off at one profile build it once)."""
    from nemo.collections.asr.parts.context_biasing.biasing_multi_model import BiasingRequestItemConfig
    from nemo.collections.asr.parts.context_biasing.boosting_graph_batched import BoostingTreeModelConfig

    return BiasingRequestItemConfig(
        boosting_model_cfg=BoostingTreeModelConfig(
            key_phrases_list=list(boost.terms),
            context_score=boost.context_score,
            depth_scaling=boost.depth_scaling,
        ),
        boosting_model_alpha=boost.weight,
        cache_key=f"{boost.list_hash}:{boost.weight}",
    )


def pipeline_cfg(
    model_path: str,
    att_context_size: Sequence[int],
    *,
    stop_history_eou_ms: int = 800,
    batch_size: int = 8,
    boosting: bool = False,
    cuda_graphs: bool = True,
    device: str = "cuda",
) -> Any:
    """The cache-aware RNNT pipeline's configuration: greedy decoding with word confidence, EOU endpointing, no ITN,
    fp32 (evals and the live channel decode in the precision the weights were trained in). ``cuda_graphs`` False turns
    NeMo's CUDA-graph greedy decoder off: with two distinct models in one process (a live session of two checkpoints)
    their graph decoders end in an illegal memory access (NeMo 3.0.0, this stream's GPU check); the words are the same
    either way (22/22 clips) and so is the decode time at batch 8."""
    from omegaconf import OmegaConf

    greedy: dict[str, Any] = {"max_symbols": 10, "preserve_frame_confidence": True}
    if not cuda_graphs:
        greedy["use_cuda_graph_decoder"] = False
    if boosting:
        greedy.update({"loop_labels": True, "enable_per_stream_biasing": True})
    return OmegaConf.create(
        {
            "asr": {
                "model_name": model_path,
                "device": device,
                "device_id": 0,
                "compute_dtype": "float32",
                "use_amp": False,
                "decoding": {"strategy": "greedy_batch", "fused_batch_size": -1, "greedy": greedy},
            },
            "itn": {
                "input_case": "lower_cased",
                "whitelist": None,
                "overwrite_cache": False,
                "max_number_of_permutations_per_split": 729,
                "left_padding_size": 4,
                "batch_size": 32,
                "n_jobs": 1,
            },
            "confidence": {
                "exclude_blank": True,
                "aggregation": "min",
                "method_cfg": {"name": "entropy", "entropy_type": "tsallis", "alpha": 0.5, "entropy_norm": "exp"},
            },
            "endpointing": {"stop_history_eou": stop_history_eou_ms, "residue_tokens_at_end": 2},
            "streaming": {
                "sample_rate": SR,
                "batch_size": batch_size,
                "word_boundary_tolerance": 4,
                "att_context_size": list(att_context_size),
                "use_cache": True,
                "use_feat_cache": True,
                "chunk_size_in_secs": None,
                "request_type": "frame",
                "num_slots": max(16, batch_size),
            },
            "matmul_precision": "highest",
            "log_level": 30,
            "pipeline_type": "cache_aware",
            "asr_decoding_type": "rnnt",
            "enable_itn": False,
            "enable_nmt": False,
            "asr_output_granularity": "word",
            "cache_dir": None,
            "lang": None,
            "return_tail_result": False,
        }
    )


def patch_prompt() -> None:
    """Apply the per-stream language prompt in ``CacheAwareRNNTInferenceWrapper.execute_step`` (NeMo 3.0.0 builds
    the prompt vectors and drops them; see the module docstring)."""
    import torch
    from nemo.collections.asr.inference.model_wrappers import cache_aware_rnnt_inference_wrapper as w

    cls: Any = w.CacheAwareRNNTInferenceWrapper
    if getattr(cls, "_cadence_prompt_patch", False):
        return

    def execute_step(
        self: Any,
        processed_signal: Any,
        processed_signal_length: Any,
        context: Any,
        previous_hypotheses: Any,
        drop_extra_pre_encoded: Any,
        keep_all_outputs: bool,
        drop_left_context: Any = None,
        valid_out_len: Any = None,
        prompt_vectors: Any = None,
    ) -> Any:
        m = self.asr_model
        (encoded, encoded_len, c_ch, c_t, c_len) = m.encoder.cache_aware_stream_step(
            processed_signal=processed_signal,
            processed_signal_length=processed_signal_length,
            cache_last_channel=context.cache_last_channel,
            cache_last_time=context.cache_last_time,
            cache_last_channel_len=context.cache_last_channel_len,
            keep_all_outputs=keep_all_outputs,
            drop_extra_pre_encoded=drop_extra_pre_encoded,
        )
        if prompt_vectors is not None and getattr(m, "concat", False):
            e = encoded.transpose(1, 2)  # (B, T, D)
            p = prompt_vectors.to(e.dtype).unsqueeze(1).expand(-1, e.shape[1], -1)
            encoded = m.prompt_kernel(torch.cat([e, p], dim=-1)).to(e.dtype).transpose(1, 2)
        new_context = w.CacheAwareContext(cache_last_channel=c_ch, cache_last_time=c_t, cache_last_channel_len=c_len)
        if drop_left_context:
            encoded = encoded[:, :, drop_left_context:]
            encoded_len = encoded_len - drop_left_context
        if valid_out_len and not keep_all_outputs:
            encoded = encoded[:, :, :valid_out_len]
            encoded_len = torch.ones_like(encoded_len) * valid_out_len
        best_hyp = m.decoding.rnnt_decoder_predictions_tensor(
            encoded, encoded_len, return_hypotheses=True, partial_hypotheses=previous_hypotheses
        )
        return best_hyp, new_context

    cls.execute_step = execute_step
    cls._cadence_prompt_patch = True


def patch_load() -> None:
    """Restore a ``.nemo`` on the CPU, then move it to the card (a third shim, from this stream's GPU check).

    As shipped the pipeline restores straight onto the card, so the state dict and the model sit there together: a
    5.6 GB load peak for 2.5 GB of fp32 weights (spike A5). Restoring on the CPU keeps the card at the model's own size
    while loading, which is what lets a second distinct model load beside the first under the session's reservation.
    The second restore in one process also trips PyTorch's ``weights_only`` unpickler (NeMo 3.0.0 restores through
    ``torch.load`` with its default); the restore runs with ``TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD`` — the files are the
    content store's own checkpoints or the pinned base model, never an upload.
    """
    import os

    from nemo.collections.asr.inference.model_wrappers import asr_inference_wrapper as w

    cls: Any = w.ASRInferenceWrapper
    if getattr(cls, "_cadence_load_patch", False):
        return

    def load_model(model_name: str, map_location: Any) -> Any:
        from nemo.collections.asr.models import ASRModel

        prev = os.environ.get("TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD")
        os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
        try:
            model = ASRModel.restore_from(model_name, map_location="cpu")
            model = model.to(map_location)
            model.eval()
            return model
        except Exception as e:
            raise RuntimeError(f"Failed to load model {model_name}: {e}") from e
        finally:
            if prev is None:
                os.environ.pop("TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD", None)
            else:
                os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = prev

    cls.load_model = staticmethod(load_model)
    cls._cadence_load_patch = True


@dataclass
class Model:
    """One loaded model and its pipelines by latency profile name."""

    path: str
    pipelines: dict[str, Any]
    att: dict[str, list[int]]
    load_s: float

    @property
    def asr_model(self) -> Any:
        return next(iter(self.pipelines.values())).asr_model

    def prompt_dictionary(self) -> dict[str, int]:
        from cadence_nemo.training import model_facts

        return model_facts(self.asr_model.asr_model).prompt_dictionary


def load(
    model_path: str,
    profiles: Mapping[str, Sequence[int]],
    *,
    stop_history_eou_ms: int,
    boosting: bool = False,
    cuda_graphs: bool = True,
    batch_size: int = 8,
    device: str = "cuda",
) -> Model:
    """Restore ``model_path`` once and build a pipeline per latency profile (name → att_context_size) over it."""
    from nemo.collections.asr.inference.factory.pipeline_builder import PipelineBuilder
    from nemo.collections.asr.inference.pipelines.cache_aware_rnnt_pipeline import CacheAwareRNNTPipeline

    patch_prompt()
    patch_load()
    names = list(profiles)
    if not names:
        raise ValueError("load needs at least one latency profile")
    t0 = time.perf_counter()

    def cfg(att: Sequence[int]) -> Any:
        return pipeline_cfg(
            model_path,
            att,
            stop_history_eou_ms=stop_history_eou_ms,
            boosting=boosting,
            cuda_graphs=cuda_graphs,
            batch_size=batch_size,
            device=device,
        )

    first = PipelineBuilder.build_pipeline(cfg(profiles[names[0]]))
    pipes: dict[str, Any] = {names[0]: first}
    for n in names[1:]:
        pipes[n] = CacheAwareRNNTPipeline(cfg(profiles[n]), first.asr_model)
    return Model(
        path=model_path,
        pipelines=pipes,
        att={n: [int(v) for v in a] for n, a in profiles.items()},
        load_s=time.perf_counter() - t0,
    )


def activate(pipeline: Any, att: Sequence[int]) -> None:
    """Put the shared encoder at this pipeline's look-ahead before a step."""
    enc = pipeline.asr_model.asr_model.encoder
    want = [int(v) for v in att]
    if [int(v) for v in enc.att_context_size] != want:
        enc.att_context_size = want
        enc.setup_streaming_params()


def chunk_samples(pipeline: Any) -> int:
    return round(float(pipeline.chunk_size_in_secs) * SR)


@dataclass
class PipelineStream:
    """One target of a session (or one utterance of an eval): a model's pipeline at a profile, in a language."""

    target: str
    pipeline: Any
    att: list[int]
    profile: str
    language: str
    stop_history_eou_ms: int = 800
    boost_cfg: Boost | None = None
    load_s: float = 0.0
    decoder: str = DECODER
    step_ms: list[float] = field(default_factory=list)
    stream_id: int = 0
    buf: Audio = field(default_factory=lambda: np.zeros(0, dtype=np.float32))
    stream_open: bool = False
    stream_start: int = 0  # absolute 16 kHz sample where the current pipeline stream began
    consumed: int = 0  # absolute real samples handed to the pipeline
    segment: int = 0
    seq: int = 0
    last_partial: str = ""
    new_word: bool = True  # the next final starts a word (nothing before it, or a stream boundary)

    @property
    def chunk(self) -> int:
        return chunk_samples(self.pipeline)

    @property
    def chunk_ms(self) -> int:
        return round(self.chunk / SR * 1000)

    @property
    def boost(self) -> dict[str, Any] | None:
        return {"terms": len(self.boost_cfg.terms), "weight": self.boost_cfg.weight} if self.boost_cfg else None

    # -------------------------------------------------------------- frames

    def has_chunk(self) -> bool:
        return self.buf.size >= self.chunk

    def next_frame(self, last: bool = False) -> tuple[Any, int, int]:
        """The next frame of the stream: a whole chunk of pending audio, or (``last``) the rest padded with silence.
        Returns the frame, the samples of real audio it carries and its valid length."""
        from nemo.collections.asr.inference.streaming.framing.request import Frame
        from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

        if last:
            real = int(self.buf.size)
            frame = np.zeros(self.chunk, dtype=np.float32)
            frame[:real] = self.buf[: self.chunk]
            self.buf = np.zeros(0, dtype=np.float32)
            # with no pending audio the whole chunk is silence given to the model as right context
            valid = real if real > 0 else self.chunk
        else:
            frame, self.buf = self.buf[: self.chunk], self.buf[self.chunk :]
            real = valid = self.chunk
        first = not self.stream_open
        if first:
            self.stream_id = next(_stream_ids)
            self.stream_open = True
            self.stream_start = self.consumed
        import torch

        options = None
        if first:
            options = ASRRequestOptions(
                language_code=self.language,
                asr_output_granularity="word",
                stop_history_eou=self.stop_history_eou_ms,
                biasing_cfg=biasing_request(self.boost_cfg) if self.boost_cfg else None,
            )
        fr = Frame(
            samples=torch.from_numpy(np.ascontiguousarray(frame)),
            stream_id=self.stream_id,
            is_first=first,
            is_last=last,
            length=valid if last else -1,
            options=options,
        )
        return fr, real, valid

    def consume(self, out: Any, real: int, last: bool, reason: str) -> list[Event]:
        """The events of one step's output for this stream."""
        self.consumed += real
        end = self.consumed
        off = self.stream_start / SR
        ev: list[Event] = []
        raw = str(out.final_transcript or "")
        if raw.strip() or last:
            words: list[dict[str, Any]] = []
            for s in out.final_segments or []:
                w = strip_tags(str(s.text))
                if w:
                    words.append(
                        {
                            "word": w,
                            "start": round(off + float(s.start), 3),
                            "end": round(off + float(s.end), 3),
                            "confidence": round(float(s.conf), 4),
                        }
                    )
            text = strip_tags(raw)
            if not words and text:
                words = [{"word": w} for w in text.split()]
            # NeMo's endpointer can close a segment inside a word (at 80 ms right after the first token): the next
            # final then continues the word, and the pipeline joins them without a separator (A5 finding 4).
            space = raw[:1].isspace() or self.new_word
            ev.append(self._final(words, reason if last else "eou", end, text, space))
            if text or last:
                self.new_word = last
        partial = strip_tags(str(out.partial_transcript or "")) if not last else ""
        if partial and partial != self.last_partial:
            self.seq += 1
            self.last_partial = partial
            ev.append(
                {
                    "type": "partial",
                    "target": self.target,
                    "segment": self.segment,
                    "seq": self.seq,
                    "text": partial,
                    "audioEnd": round(end / SR, 3),
                }
            )
        if last:
            self.stream_open = False
        return ev

    def _final(self, words: list[dict[str, Any]], reason: str, end: int, text: str, space: bool) -> Event:
        self.seq += 1
        ev: Event = {
            "type": "final",
            "target": self.target,
            "segment": self.segment,
            "seq": self.seq,
            "text": text,  # the transcript, not the words joined: at 80 ms NeMo's word segmenter can split a word
            "words": words,
            "endpoint": reason,
            "audioEnd": round(end / SR, 3),
            "space": bool(space),
        }
        self.segment += 1
        self.last_partial = ""
        return ev

    def step(self, last: bool = False, reason: str = "") -> list[Event]:
        fr, real, _ = self.next_frame(last)
        t0 = time.perf_counter()
        activate(self.pipeline, self.att)
        (out,) = self.pipeline.transcribe_step([fr])
        self.step_ms.append((time.perf_counter() - t0) * 1000)
        return self.consume(out, real, last, reason)

    # -------------------------------------------------------------- the live Decoder

    def push(self, x: Audio) -> list[Event]:
        self.buf = np.concatenate([self.buf, np.asarray(x, dtype=np.float32)])
        ev: list[Event] = []
        while self.has_chunk():
            ev += self.step()
        return ev

    def finalize(self, reason: str) -> list[Event]:
        """Pad the pending audio with silence to a whole chunk and close the pipeline stream (forced EOU)."""
        if not self.stream_open and self.buf.size == 0:
            return [self._final([], reason, self.consumed, "", True)]
        return self.step(last=True, reason=reason)


# ---------------------------------------------------------------- file decoding (nemotron_transcribe@3)


@dataclass
class FileResult:
    text: str
    words: list[dict[str, Any]]
    partials: list[dict[str, Any]]


def join_finals(finals: Sequence[Event]) -> str:
    """The transcript of a sequence of finals: a final with ``space: false`` continues the previous one's last word."""
    out = ""
    for f in finals:
        t = str(f.get("text") or "")
        if not t:
            continue
        out = out + (" " if out and f.get("space", True) else "") + t
    return out.strip()


def collect(events: Sequence[Event], t0: float, emitted: Sequence[float]) -> FileResult:
    """A decode's events as a hypotheses row's text, words and partial events (``audioOffsetMs``, ``emitMs`` and the
    text so far: finals plus the current partial)."""
    finals: list[Event] = []
    words: list[dict[str, Any]] = []
    partials: list[dict[str, Any]] = []
    for e, at in zip(events, emitted, strict=True):
        if e["type"] == "final":
            finals.append(e)
            words += e["words"]
            so_far = join_finals(finals)
        else:
            so_far = join_finals([*finals, {"text": e["text"], "space": True}])
        partials.append(
            {"audioOffsetMs": round(float(e["audioEnd"]) * 1000), "emitMs": round((at - t0) * 1000, 2), "text": so_far}
        )
    text = join_finals(finals)
    if partials:
        partials[-1]["final"] = True
        partials[-1]["text"] = text
    return FileResult(text=text, words=words, partials=partials)


def decode_batch(streams: Sequence[PipelineStream], audios: Sequence[Audio]) -> list[FileResult]:
    """Decode whole files, one stream each, stepping every stream of the batch together (``transcribe_step`` over a
    list of frames: continuous batching across streams); each file ends with ``finalize("end")``. The streams must
    share one pipeline."""
    if not streams:
        return []
    pipe = streams[0].pipeline
    events: list[list[Event]] = [[] for _ in streams]
    stamps: list[list[float]] = [[] for _ in streams]
    for s, a in zip(streams, audios, strict=True):
        s.buf = np.asarray(a, dtype=np.float32)
    active = list(range(len(streams)))
    t0 = time.perf_counter()
    activate(pipe, streams[0].att)
    while active:
        frames, meta = [], []
        for i in active:
            s = streams[i]
            last = not s.has_chunk()
            fr, real, _ = s.next_frame(last)
            frames.append(fr)
            meta.append((i, real, last))
        outs = pipe.transcribe_step(frames)
        now = time.perf_counter()
        nxt = []
        for (i, real, last), out in zip(meta, outs, strict=True):
            evs = streams[i].consume(out, real, last, "end")
            events[i] += evs
            stamps[i] += [now] * len(evs)
            if not last:
                nxt.append(i)
        active = nxt
    return [collect(e, t0, st) for e, st in zip(events, stamps, strict=True)]
