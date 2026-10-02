"""One decoder for live sessions and evals (spike A5 finding 2): NeMo's cache-aware streaming pipeline API
(``nemo.collections.asr.inference``, NeMo Speech 26.07 / NeMo 3.0.0) with five shims.

A :class:`PipelineStream` turns 16 kHz float audio pushed in any frame size into log-mel features (:class:`Features`,
equal to the whole stream's), cuts them into the feature buffers of the reference cache-aware loop
(``nemotron_transcribe@2``'s, NeMo's ``CacheAwareStreamingAudioBuffer``: a short first chunk without cache, then the
pre-encode cache and a chunk) and turns the pipeline's outputs into the live channel's events (R48): ``partial``
replaces the segment's previous partial, ``final`` never changes, every event states the audio it covers
(``audioEnd``). ``finalize`` sends the rest of the stream's features as its last buffer (the pipeline keeps all
outputs and forces an end of utterance); the next audio opens a new pipeline stream at the same audio clock, with a
fresh encoder cache — a segment boundary, not just a flush.

``nemotron_live`` serves sessions with it and ``nemotron_transcribe@3`` decodes golden sets with it
(:func:`decode_batch`): a live session in 20 ms frames and a file decode give the same words (22/22 FLEURS he and ru
clips at every profile, batch 1), and the same WER as the reference loop at every profile (this stream's GPU check:
he 75.6 / 77.9 / 65.1 / 66.3 / 69.8 at 80 / 160 / 320 / 560 / 1120 ms, same empty clips). The words differ from the
reference loop only where it drops a short tail (the clip's last tokens, "כתבית." → "כתבי").

Shims, NeMo 3.0.0 gaps that belong upstream:

1. :func:`patch_prompt`: ``CacheAwareRNNTInferenceWrapper.execute_step`` takes the per-stream ``prompt_vectors`` the
   pipeline builds from ``ASRRequestOptions.language_code`` but never applies them, so a prompt model such as
   Nemotron 3.5 gets un-prompted encoder output and emits only blanks. The patch applies the model's
   ``prompt_kernel`` per stream, as ``PromptStreamingMixin._apply_prompt_to_encoded`` does for its single index. It
   also drops no extra pre-encoded frames on a stream's first step (it has no cache) and reads the count for the
   others from the encoder's current streaming config (the wrapper read it once, at load).
2. The pipeline keeps the model's trailing locale tag (``… <he-IL>``) in its text and words: :func:`lang.strip_tags`.
3. :func:`patch_load`: restore on the CPU, then move to the card.
4. :class:`Features`: NeMo's frame path featurizes each chunk alone, so the frames at every chunk edge see zero
   padding where audio belongs (he fixtures at 160 ms: 5 empty transcripts instead of 2, WER 80.2 against 77.9).
5. :func:`fix_buffer_length`: NeMo sizes a feature buffer as ``int(seconds / stride)``, 16 instead of 17 frames at
   80 ms (WER 91.9 against 75.6).

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
        if getattr(cls, "_cadence_first_step", False):
            # A stream's first chunk carries no pre-encode cache, so nothing extra was pre-encoded to drop (the
            # reference loop passes 0 on its first step; see Features)
            drop_extra_pre_encoded = 0
        elif drop_extra_pre_encoded:
            # The wrapper read this once, at load, from whatever look-ahead the encoder had then; the encoder's
            # streaming config is the current profile's (activate), as in the reference loop
            drop_extra_pre_encoded = int(m.encoder.streaming_cfg.drop_extra_pre_encoded)
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
    for n, p in pipes.items():
        fix_buffer_length(p, profiles[n])
    return Model(
        path=model_path,
        pipelines=pipes,
        att={n: [int(v) for v in a] for n, a in profiles.items()},
        load_s=time.perf_counter() - t0,
    )


def fix_buffer_length(pipeline: Any, att: Sequence[int]) -> None:
    """The fifth shim: NeMo 3.0.0 sizes a feature buffer as ``int(seconds / window_stride)``, and at 80 ms
    ``(0.09 + 0.08) / 0.01`` is 16.999…, so every buffer lost its newest frame (he fixtures at 80 ms: WER 91.9 against
    the reference loop's 75.6). The length is the encoder's pre-encode cache plus its chunk, in frames."""
    activate(pipeline, att)
    sc = pipeline.asr_model.asr_model.encoder.streaming_cfg

    def later(v: Any) -> int:
        return int(v[1]) if isinstance(v, list | tuple) else int(v)

    pipeline.expected_feature_buffer_len = later(sc.pre_encode_cache_size) + later(sc.chunk_size)


def activate(pipeline: Any, att: Sequence[int]) -> None:
    """Put the shared encoder at this pipeline's look-ahead before a step."""
    enc = pipeline.asr_model.asr_model.encoder
    want = [int(v) for v in att]
    if [int(v) for v in enc.att_context_size] != want:
        enc.att_context_size = want
        enc.setup_streaming_params()


def chunk_samples(pipeline: Any) -> int:
    return round(float(pipeline.chunk_size_in_secs) * SR)


def _first_step_flag(on: bool) -> None:
    """Mark the next ``execute_step`` as a stream's first step (no extra pre-encoded frames to drop; see
    :func:`patch_prompt`)."""
    from nemo.collections.asr.inference.model_wrappers import cache_aware_rnnt_inference_wrapper as w

    w.CacheAwareRNNTInferenceWrapper._cadence_first_step = on


@dataclass
class Features:
    """The log-mel features of one pipeline stream, computed as its audio arrives and equal to the features of the
    whole stream's audio (the fourth shim, from the follow-up GPU check).

    NeMo's own frame path computes a chunk's features from that chunk's audio plus 10 ms, so the frames at every chunk
    edge see zero padding where the next chunk's audio belongs: on the ten FLEURS he fixtures that turned 2 empty
    transcripts into 5 (WER 77.9 → 80.2 at 160 ms) against the reference cache-aware loop
    (``nemotron_transcribe@2``), which featurizes the whole file. Here a frame is computed only once its whole STFT
    window has arrived (``half`` samples past its centre, 16 ms at 512-point FFT), from a window of audio that starts
    two frames before it (the pre-emphasis of the window's first sample is the only thing a cut changes, and no kept
    frame reads it); at the stream's end the last frames see the end padding a whole-file featurization sees.
    """

    preprocessor: Any
    hop: int
    half: int
    audio: Audio = field(default_factory=lambda: np.zeros(0, dtype=np.float32))
    a0: int = 0  # stream sample index of audio[0]
    total: int = 0  # stream samples received
    feats: Any = None  # (n_mels, k) tensor of frames f0 .. n-1
    f0: int = 0
    n: int = 0  # frames computed
    final: bool = False

    def push(self, x: Audio) -> None:
        self.audio = np.concatenate([self.audio, np.asarray(x, dtype=np.float32)])
        self.total += int(np.asarray(x).size)
        self._compute(final=False)

    def finish(self) -> None:
        self.final = True
        self._compute(final=True)

    def _compute(self, final: bool) -> None:
        import torch

        if self.total == 0:
            return
        last = self.total // self.hop if final else (self.total - self.half) // self.hop
        if last < self.n:
            return
        a = max(0, (self.n - 2) * self.hop)
        seg = self.audio[a - self.a0 :]
        t = next(itertools.chain(self.preprocessor.parameters(), self.preprocessor.buffers()), None)
        dev = t.device if t is not None else "cpu"
        with torch.inference_mode():
            sig = torch.from_numpy(np.ascontiguousarray(seg))[None].to(dev)
            f, _ = self.preprocessor(input_signal=sig, length=torch.tensor([seg.size], device=dev))
        j0, j1 = self.n - a // self.hop, last - a // self.hop + 1
        new = f[0, :, j0:j1].float()
        self.feats = new if self.feats is None else torch.cat([self.feats, new], dim=1)
        self.n = last + 1
        keep = max(0, (self.n - 2) * self.hop)  # the next window starts here
        if keep > self.a0:
            self.audio = self.audio[keep - self.a0 :]
            self.a0 = keep

    def frames(self, start: int, end: int) -> Any:
        """Frames [start, end) (all computed, none trimmed)."""
        return self.feats[:, start - self.f0 : end - self.f0]

    def trim(self, keep_from: int) -> None:
        if self.feats is not None and keep_from > self.f0:
            self.feats = self.feats[:, keep_from - self.f0 :]
            self.f0 = keep_from


@dataclass
class Chunk:
    """One feature buffer of a stream: the pre-encode cache and a chunk of frames (the reference cache-aware loop's
    chunking, NeMo's ``CacheAwareStreamingAudioBuffer``: a shorter first chunk without cache, then chunks with
    ``pre_encode_cache_size`` frames of cache), and the stream samples it accounts for."""

    features: Any
    length: int  # valid frames (with the cache); the buffer is right-padded to the pipeline's length when last
    first: bool
    last: bool
    real: int


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
    stream_open: bool = False
    stream_start: int = 0  # absolute 16 kHz sample where the current pipeline stream began
    consumed: int = 0  # absolute real samples handed to the pipeline
    segment: int = 0
    seq: int = 0
    last_partial: str = ""
    new_word: bool = True  # the next final starts a word (nothing before it, or a stream boundary)
    feats: Features | None = None
    idx: int = 0  # the stream's next feature frame to send
    sent: int = 0  # stream samples accounted for by the chunks sent
    first_pending: bool = True
    cfg: tuple[int, int, int, int, int, int] | None = None  # c0, c1, p0, p1, sampling0, sampling1

    @property
    def chunk(self) -> int:
        return chunk_samples(self.pipeline)

    @property
    def chunk_ms(self) -> int:
        return round(self.chunk / SR * 1000)

    @property
    def boost(self) -> dict[str, Any] | None:
        return {"terms": len(self.boost_cfg.terms), "weight": self.boost_cfg.weight} if self.boost_cfg else None

    # -------------------------------------------------------------- features and chunks

    def _streaming_cfg(self) -> tuple[int, int, int, int, int, int]:
        if self.cfg is None:
            activate(self.pipeline, self.att)
            enc = self.pipeline.asr_model.asr_model.encoder
            sc = enc.streaming_cfg

            def two(v: Any) -> tuple[int, int]:
                return (int(v[0]), int(v[1])) if isinstance(v, list | tuple) else (int(v), int(v))

            c0, c1 = two(sc.chunk_size)
            p0, p1 = two(sc.pre_encode_cache_size)
            pe = getattr(enc, "pre_encode", None)
            sf = pe.get_sampling_frames() if pe is not None and hasattr(pe, "get_sampling_frames") else 1
            s0, s1 = two(sf)
            self.cfg = (c0, c1, p0, p1, s0, s1)
        return self.cfg

    def _open(self) -> None:
        if self.stream_open:
            return
        am = self.pipeline.asr_model.asr_model
        pcfg = am.cfg.preprocessor
        hop = round(float(pcfg.window_stride) * SR)
        half = int(pcfg.get("n_fft", 512) or 512) // 2
        self.stream_id = next(_stream_ids)
        self.stream_open = True
        self.stream_start = self.consumed
        self.feats = Features(preprocessor=am.preprocessor, hop=hop, half=half)
        self.idx, self.sent, self.first_pending = 0, 0, True
        self._streaming_cfg()

    def _buffer(self, start: int, length: int, first: bool, last: bool) -> Any:
        import torch

        assert self.feats is not None
        assert self.cfg is not None
        _, _, p0, p1, _, _ = self.cfg
        f = self.feats
        n_mels = int(
            f.feats.shape[0] if f.feats is not None else self.pipeline.asr_model.asr_model.cfg.preprocessor.features
        )
        dev = f.feats.device if f.feats is not None else "cpu"
        if first:
            cache = torch.zeros((n_mels, p0), device=dev)
        else:
            lo = max(0, start - p1)
            cache = f.frames(lo, start)
            if cache.shape[1] < p1:
                cache = torch.cat([torch.zeros((n_mels, p1 - cache.shape[1]), device=dev), cache], dim=1)
        body = f.frames(start, start + length) if length > 0 else torch.zeros((n_mels, 0), device=dev)
        buf = torch.cat([cache, body], dim=1)
        full = int(self.pipeline.expected_feature_buffer_len)
        if last and buf.shape[1] < full:
            buf = torch.cat([buf, torch.zeros((n_mels, full - buf.shape[1]), device=dev)], dim=1)
        return buf, cache.shape[1] + length

    def _chunks(self, final: bool) -> list[Chunk]:
        """The chunks the features computed so far make: whole chunks while the stream goes on; at its end, the rest,
        the last one marked."""
        assert self.feats is not None
        c0, c1, _, p1, _, _ = self._streaming_cfg()
        hop = self.feats.hop
        plan: list[tuple[int, int, bool]] = []
        idx, first = self.idx, self.first_pending
        while True:
            cs = c0 if first else c1
            avail = self.feats.n - idx
            if not final and avail < cs:
                break
            if final and avail <= 0:
                break
            plan.append((idx, min(cs, avail), first))
            idx += cs
            first = False
        out: list[Chunk] = []
        # At the end a tail shorter than the subsampling's frames still goes out (the reference loop drops it and with
        # it the clip's last tokens, "כתבית." → "כתבי"); with the cache before it the step has enough frames. When every
        # frame went out in whole chunks, the stream still ends with a step (the cache alone) that keeps all outputs.
        if final and not plan:
            plan.append((self.feats.n, 0, self.first_pending))
        for k, (start, length, first_chunk) in enumerate(plan):
            last = final and k == len(plan) - 1
            buf, valid = self._buffer(start, length, first_chunk, last)
            real = (self.feats.total - self.sent) if last else length * hop
            real = max(0, min(real, self.feats.total - self.sent))
            self.sent += real
            out.append(Chunk(features=buf, length=valid, first=first_chunk, last=last, real=real))
        if plan:
            self.idx = plan[-1][0] + (c0 if plan[-1][2] else c1)
            self.first_pending = False
        self.feats.trim(max(0, self.idx - p1))
        return out

    def _request(self, ch: Chunk) -> Any:
        from nemo.collections.asr.inference.streaming.framing.request import FeatureBuffer
        from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

        options = None
        if ch.first:
            options = ASRRequestOptions(
                language_code=self.language,
                asr_output_granularity="word",
                stop_history_eou=self.stop_history_eou_ms,
                biasing_cfg=biasing_request(self.boost_cfg) if self.boost_cfg else None,
            )
        return FeatureBuffer(
            features=ch.features,
            stream_id=self.stream_id,
            is_first=ch.first,
            is_last=ch.last,
            length=ch.length if ch.last else -1,
            options=options,
        )

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

    def _send(self, chunks: Sequence[Chunk], reason: str) -> list[Event]:
        ev: list[Event] = []
        for ch in chunks:
            t0 = time.perf_counter()
            activate(self.pipeline, self.att)
            _first_step_flag(ch.first)
            try:
                (out,) = self.pipeline.transcribe_step([self._request(ch)])
            finally:
                _first_step_flag(False)
            self.step_ms.append((time.perf_counter() - t0) * 1000)
            ev += self.consume(out, ch.real, ch.last, reason)
        if chunks and chunks[-1].last:
            self.feats = None
        return ev

    # -------------------------------------------------------------- the live Decoder

    def push(self, x: Audio) -> list[Event]:
        x = np.asarray(x, dtype=np.float32)
        if x.size == 0:
            return []
        self._open()
        assert self.feats is not None
        self.feats.push(x)
        return self._send(self._chunks(final=False), "")

    def finalize(self, reason: str) -> list[Event]:
        """Close the pipeline stream: the rest of its features (with the end padding a whole-file featurization sees)
        go out, the last step keeps all outputs and forces the end of utterance. The next audio opens a new stream."""
        if not self.stream_open or self.feats is None or self.feats.total == 0:
            self.stream_open = False
            self.feats = None
            return [self._final([], reason, self.consumed, "", True)]
        self.feats.finish()
        return self._send(self._chunks(final=True), reason)


# ---------------------------------------------------------------- file decoding (nemotron_transcribe@3)


@dataclass
class FileResult:
    text: str
    words: list[dict[str, Any]]
    partials: list[dict[str, Any]]
    # (audio available ms, compute ms) of every chunk the stream stepped, silent ones included: latency_score
    # simulates real-time pace from them (a partial's ``step`` indexes its chunk)
    steps: list[tuple[int, float]] = field(default_factory=list)


def join_finals(finals: Sequence[Event]) -> str:
    """The transcript of a sequence of finals: a final with ``space: false`` continues the previous one's last word."""
    out = ""
    for f in finals:
        t = str(f.get("text") or "")
        if not t:
            continue
        out = out + (" " if out and f.get("space", True) else "") + t
    return out.strip()


def collect(
    events: Sequence[Event],
    t0: float,
    emitted: Sequence[float],
    chunks: Sequence[int] | None = None,
    steps: Sequence[tuple[int, float]] = (),
) -> FileResult:
    """A decode's events as a hypotheses row's text, words and partial events (``audioOffsetMs``, ``emitMs``, the text
    so far: finals plus the current partial, and ``step``, the index in ``steps`` of the chunk that emitted it)."""
    finals: list[Event] = []
    words: list[dict[str, Any]] = []
    partials: list[dict[str, Any]] = []
    for k, (e, at) in enumerate(zip(events, emitted, strict=True)):
        if e["type"] == "final":
            finals.append(e)
            words += e["words"]
            so_far = join_finals(finals)
        else:
            so_far = join_finals([*finals, {"text": e["text"], "space": True}])
        p: dict[str, Any] = {
            "audioOffsetMs": round(float(e["audioEnd"]) * 1000),
            "emitMs": round((at - t0) * 1000, 2),
            "text": so_far,
        }
        if chunks is not None and k < len(chunks) and steps:
            p["step"] = chunks[k]
        partials.append(p)
    text = join_finals(finals)
    if partials:
        partials[-1]["final"] = True
        partials[-1]["text"] = text
    return FileResult(text=text, words=words, partials=partials, steps=list(steps))


def decode_batch(streams: Sequence[PipelineStream], audios: Sequence[Audio]) -> list[FileResult]:
    """Decode whole files, one stream each, stepping every stream of the batch together (``transcribe_step`` over a
    list of feature buffers: continuous batching across streams); each file ends with ``finalize("end")``. The
    streams must share one pipeline; their chunks are the same as a live session's of the same audio, so a file
    decodes to the same words either way.

    Clips of different lengths batch safely: a clip's words do not depend on which clips share its steps (a sub-chunk
    clip, a short and a long one decode as each alone at 80, 160 and 1120 ms). They can depend on how many do, at
    80 ms: the card's batched matrix products round differently from batch 1, and the base model's near-ties there
    flip (1 of the 10 he fixtures and a 3-clip concatenation differ by words between batch 1 and batch 2+, the same at
    any batch of 2+; 160 and 1120 ms are unaffected; this stream's GPU check, 2026-10-02). The batch size therefore
    stays in an eval's decoding hash. Each hypotheses row also carries ``steps``, the audio each chunk made available
    and its compute (the step's wall time over the streams it stepped), and each partial the ``step`` that emitted it,
    from which latency_score simulates real-time pace."""
    if not streams:
        return []
    pipe = streams[0].pipeline
    events: list[list[Event]] = [[] for _ in streams]
    stamps: list[list[float]] = [[] for _ in streams]
    steps: list[list[tuple[int, float]]] = [[] for _ in streams]  # per stream: (audio ms, compute ms) per chunk
    chunk_of: list[list[int]] = [[] for _ in streams]  # per event: the chunk whose step emitted it
    plans: list[list[Chunk]] = []
    for s, a in zip(streams, audios, strict=True):
        x = np.asarray(a, dtype=np.float32)
        if x.size == 0:
            plans.append([])
            continue
        s._open()
        assert s.feats is not None
        s.feats.push(x)
        s.feats.finish()
        plans.append(s._chunks(final=True))
    t0 = time.perf_counter()
    activate(pipe, streams[0].att)
    step = 0
    while True:
        batch = [(i, plan[step]) for i, plan in enumerate(plans) if step < len(plan)]
        if not batch:
            break
        _first_step_flag(step == 0)
        started = time.perf_counter()
        try:
            outs = pipe.transcribe_step([streams[i]._request(ch) for i, ch in batch])
        finally:
            _first_step_flag(False)
        now = time.perf_counter()
        # The step's compute per stream: what one stream of the batch costs, whatever the batch size (at batch 1 a
        # step also pays the card's fixed cost per call, so this is a lower bound of a lone stream's compute)
        share = (now - started) * 1000 / len(batch)
        for (i, ch), out in zip(batch, outs, strict=True):
            evs = streams[i].consume(out, ch.real, ch.last, "end")
            steps[i].append((round(streams[i].consumed / SR * 1000), round(share, 2)))
            events[i] += evs
            stamps[i] += [now] * len(evs)
            chunk_of[i] += [len(steps[i]) - 1] * len(evs)
            if ch.last:
                streams[i].feats = None
        step += 1
    for i, plan in enumerate(plans):
        if not plan:  # an empty file: one empty final
            events[i].append(streams[i]._final([], "end", streams[i].consumed, "", True))
            stamps[i].append(time.perf_counter())
    return [collect(e, t0, st, ch, sp) for e, st, ch, sp in zip(events, stamps, chunk_of, steps, strict=True)]
