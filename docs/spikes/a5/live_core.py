"""A5 throwaway: the worker-side live decoder on NeMo's streaming pipeline API.

NeMo Speech 26.07 (NeMo 3.0.0) ships ``nemo.collections.asr.inference``: ``CacheAwarePipelineBuilder`` builds a
``CacheAwareRNNTPipeline`` (one per model x latency profile, because ``att_context_size`` is a property of the
encoder), and ``pipeline.transcribe_step([Frame, ...])`` advances any number of streams by one chunk each
(continuous batching, per-stream state pool, per-stream language prompt, greedy RNNT, EOU endpointing).

``LiveTarget`` turns 16 kHz float audio pushed in any frame size into chunk-sized ``Frame``s and the pipeline's
outputs into R48 events (``partial`` replaces, ``final`` never changes, every result carries the audio offset it
covers). ``finalize`` pads the pending chunk with silence and sends it as the stream's last frame (the pipeline then
flushes with ``keep_all_outputs`` and forces EOU); the next audio opens a new pipeline stream at the same audio clock.
``StreamingResampler`` reproduces ``scipy.signal.resample_poly`` (the import resampler of
``cadence_worker.audio.resample``) frame by frame; ``Telephony`` is 16k -> 8k -> G.711 mu-law -> 16k with it.
"""

from __future__ import annotations

import math
import re
import time
from dataclasses import dataclass, field
from typing import Any

import numpy as np

SR = 16000
# NeMo 3.0.0's streaming pipeline keeps the model's trailing locale tag ("... <he-IL>"); the pack strips it
# (``strip_lang_tags``), so the live layer does too.
TAG = re.compile(r"\s*<(?:[a-z]{2,3}-[A-Z]{2}|auto)>")


def strip_tags(t: str) -> str:
    return " ".join(TAG.sub(" ", t).split())

PROFILES = {"80ms": 0, "160ms": 1, "320ms": 3, "560ms": 6, "1120ms": 13}
LEFT = 56


def pipeline_cfg(model_path: str, profile: str, dtype: str = "float32", batch_size: int = 8) -> Any:
    from omegaconf import OmegaConf

    return OmegaConf.create(
        {
            "asr": {
                "model_name": model_path,
                "device": "cuda",
                "device_id": 0,
                "compute_dtype": dtype,
                "use_amp": False,
                "decoding": {
                    "strategy": "greedy_batch",
                    "fused_batch_size": -1,
                    "greedy": {"max_symbols": 10, "preserve_frame_confidence": True},
                },
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
            "endpointing": {"stop_history_eou": 800, "residue_tokens_at_end": 2},
            "streaming": {
                "sample_rate": SR,
                "batch_size": batch_size,
                "word_boundary_tolerance": 4,
                "att_context_size": [LEFT, PROFILES[profile]],
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
    """NeMo 3.0.0 gap: ``CacheAwareRNNTInferenceWrapper.execute_step`` takes ``prompt_vectors`` (the pipeline builds
    them per stream from ``ASRRequestOptions.language_code``) but never applies them, so a prompt model such as
    Nemotron 3.5 gets un-prompted encoder output and emits only blanks. Apply the model's ``prompt_kernel`` per stream
    exactly as ``PromptStreamingMixin._apply_prompt_to_encoded`` does for its single global index."""
    import torch
    from nemo.collections.asr.inference.model_wrappers import cache_aware_rnnt_inference_wrapper as w

    cls = w.CacheAwareRNNTInferenceWrapper
    if getattr(cls, "_a5_patched", False):
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
    cls._a5_patched = True


def build_pipeline(model_path: str, profile: str, dtype: str = "float32", batch_size: int = 8) -> tuple[Any, float]:
    import os

    import torch
    from nemo.collections.asr.inference.factory.pipeline_builder import PipelineBuilder

    # Keep a live job <= 8 GB on the shared card: the allocator cap (the CUDA context, ~0.5-1 GB, sits outside it).
    cap = float(os.environ.get("A5_CAP_GIB", "7"))
    total = torch.cuda.get_device_properties(0).total_memory / 2**30
    torch.cuda.set_per_process_memory_fraction(min(1.0, cap / total))
    if os.environ.get("A5_NO_PATCH") != "1":
        patch_prompt()
    t0 = time.perf_counter()
    p = PipelineBuilder.build_pipeline(pipeline_cfg(model_path, profile, dtype, batch_size))
    return p, time.perf_counter() - t0


def build_shared(model_path: str, profiles: list[str], dtype: str = "float32") -> tuple[dict[str, Any], float]:
    """One model, one pipeline per profile: the targets "same checkpoint at 160ms and at 1120ms" share the weights.
    ``att_context_size`` lives on the encoder, so ``activate(pipeline)`` switches it before each step (cheap: it only
    recomputes the streaming config)."""
    from nemo.collections.asr.inference.pipelines.cache_aware_rnnt_pipeline import CacheAwareRNNTPipeline

    first, load_s = build_pipeline(model_path, profiles[0], dtype)
    out = {profiles[0]: first}
    for p in profiles[1:]:
        if p not in out:
            out[p] = CacheAwareRNNTPipeline(pipeline_cfg(model_path, p, dtype), first.asr_model)
    return out, load_s


def activate(pipeline: Any, profile: str) -> None:
    enc = pipeline.asr_model.asr_model.encoder
    ctx = [LEFT, PROFILES[profile]]
    if list(enc.att_context_size) != ctx:
        enc.att_context_size = ctx
        enc.setup_streaming_params()


# ------------------------------------------------------------------ resampling and telephony


class StreamingResampler:
    """``resample_poly(x, up, down)`` computed frame by frame: identical output (to float rounding) for any split of
    the input, with a lookahead of the filter's half length (< 1 ms at 48k/44.1k -> 16k)."""

    def __init__(self, src: int, dst: int) -> None:
        g = math.gcd(src, dst)
        self.up, self.down = dst // g, src // g
        self.identity = src == dst
        # resample_poly's default filter: half length 10 * max(up, down) taps in the upsampled domain
        self.margin = int(math.ceil(10 * max(self.up, self.down) / self.up)) + self.down + 2
        self.buf = np.zeros(0, dtype=np.float64)
        self.base = 0  # absolute input index of buf[0] (a multiple of down)
        self.emitted = 0  # absolute output samples emitted
        self.total_in = 0

    def _run(self, final: bool) -> np.ndarray:
        from scipy import signal

        if self.buf.size == 0:
            return np.zeros(0, dtype=np.float32)
        y = signal.resample_poly(self.buf, self.up, self.down)
        # output sample m of this window is absolute output (base*up/down + m)
        first_abs = self.base * self.up // self.down
        if final:
            end_abs = int(math.ceil(self.total_in * self.up / self.down))
        else:
            safe_in = self.total_in - self.margin  # inputs beyond this still change the outputs before it
            end_abs = max(self.emitted, int(math.floor(safe_in * self.up / self.down)))
        out = y[self.emitted - first_abs : end_abs - first_abs]
        self.emitted = end_abs
        # drop input no longer needed (keep margin before the next output, aligned to down)
        need_from = (self.emitted * self.down) // self.up - self.margin
        need_from = max(self.base, (need_from // self.down) * self.down)
        self.buf = self.buf[need_from - self.base :]
        self.base = need_from
        return out.astype(np.float32)

    def push(self, x: np.ndarray) -> np.ndarray:
        if self.identity:
            return x.astype(np.float32, copy=False)
        self.buf = np.concatenate([self.buf, x.astype(np.float64)])
        self.total_in += x.size
        return self._run(False)

    def flush(self) -> np.ndarray:
        if self.identity:
            return np.zeros(0, dtype=np.float32)
        return self._run(True)


MU = 255.0


def mulaw(x: np.ndarray) -> np.ndarray:
    """G.711 mu-law compand, 8-bit quantise, expand (cadence_nemo.augment.mulaw)."""
    c = np.sign(x) * np.log1p(MU * np.minimum(np.abs(x), 1.0)) / np.log1p(MU)
    q = np.round((c + 1) / 2 * 255) / 255 * 2 - 1
    return np.asarray(np.sign(q) * ((1 + MU) ** np.abs(q) - 1) / MU, dtype=np.float32)


class Telephony:
    def __init__(self) -> None:
        self.down = StreamingResampler(SR, 8000)
        self.up = StreamingResampler(8000, SR)

    def push(self, x: np.ndarray) -> np.ndarray:
        return self.up.push(mulaw(self.down.push(x)))

    def flush(self) -> np.ndarray:
        a = self.up.push(mulaw(self.down.flush()))
        return np.concatenate([a, self.up.flush()])


# ------------------------------------------------------------------ the live target


@dataclass
class LiveTarget:
    name: str
    pipeline: Any
    lang: str
    profile: str
    stream_seq: int = 0
    buf: np.ndarray = field(default_factory=lambda: np.zeros(0, dtype=np.float32))
    stream_open: bool = False
    stream_start: int = 0  # absolute 16 kHz sample where the current pipeline stream began
    consumed: int = 0  # absolute real samples handed to the pipeline
    total: int = 0  # absolute samples received
    segment: int = 0
    seq: int = 0
    last_partial: str = ""
    step_ms: list[float] = field(default_factory=list)

    @property
    def chunk(self) -> int:
        return int(round(self.pipeline.chunk_size_in_secs * SR))

    def push(self, x: np.ndarray) -> list[dict[str, Any]]:
        self.buf = np.concatenate([self.buf, x.astype(np.float32, copy=False)])
        self.total += x.size
        ev: list[dict[str, Any]] = []
        while self.buf.size >= self.chunk:
            frame, self.buf = self.buf[: self.chunk], self.buf[self.chunk :]
            ev += self._step(frame, last=False, valid=self.chunk, real=self.chunk, reason="")
        return ev

    def finalize(self, reason: str) -> list[dict[str, Any]]:
        """Pad the pending audio with silence to a whole chunk and close the pipeline stream (forced EOU)."""
        pending = self.buf.size
        if not self.stream_open and pending == 0:
            return [self._final([], reason, self.total)]
        frame = np.zeros(self.chunk, dtype=np.float32)
        frame[:pending] = self.buf
        self.buf = np.zeros(0, dtype=np.float32)
        # with no pending audio the whole chunk is silence given to the model as right context
        valid = pending if pending > 0 else self.chunk
        return self._step(frame, last=True, valid=valid, real=pending, reason=reason)

    def _final(
        self, words: list[dict[str, Any]], reason: str, end: int, text: str | None = None, space: bool = True
    ) -> dict[str, Any]:
        self.seq += 1
        ev = {
            "type": "final",
            "target": self.name,
            "segment": self.segment,
            "seq": self.seq,
            # the transcript, not the word list joined: at 80 ms NeMo's word segmenter can split a word ("То лько")
            "text": text if text is not None else " ".join(w["word"] for w in words),
            "wordsJoinMatches": text is None or " ".join(w["word"] for w in words) == text,
            # NeMo's endpointer can close a segment mid-word (seen at 80 ms right after the first token): the next
            # final then continues the word, and the pipeline joins them without a separator
            "space": space,
            "words": words,
            "endpoint": reason,
            "audioEnd": round(end / SR, 3),
        }
        self.segment += 1
        self.last_partial = ""
        return ev

    def _step(self, frame: np.ndarray, last: bool, valid: int, real: int, reason: str) -> list[dict[str, Any]]:
        import torch
        from nemo.collections.asr.inference.streaming.framing.request import Frame
        from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

        first = not self.stream_open
        if first:
            self.stream_seq += 1
            self.stream_open = True
            self.stream_start = self.consumed
        sid = self.stream_seq
        fr = Frame(
            samples=torch.from_numpy(frame),
            stream_id=sid,
            is_first=first,
            is_last=last,
            length=valid if last else -1,
            options=ASRRequestOptions(language_code=self.lang, asr_output_granularity="word") if first else None,
        )
        t0 = time.perf_counter()
        activate(self.pipeline, self.profile)
        (out,) = self.pipeline.transcribe_step([fr])
        self.step_ms.append((time.perf_counter() - t0) * 1000)
        self.consumed += real
        end = self.consumed
        off = self.stream_start / SR
        ev: list[dict[str, Any]] = []
        if out.final_transcript.strip() or last:
            words = [
                {
                    "word": strip_tags(s.text),
                    "start": round(off + float(s.start), 3),
                    "end": round(off + float(s.end), 3),
                    "confidence": round(float(s.conf), 4),
                }
                for s in (out.final_segments or [])
                if strip_tags(s.text)
            ]
            if not words and strip_tags(out.final_transcript):
                words = [
                    {"word": w, "start": None, "end": None, "confidence": None}
                    for w in strip_tags(out.final_transcript).split()
                ]
            raw = out.final_transcript
            ev.append(
                self._final(words, reason if last else "eou", end, strip_tags(raw), space=raw[:1].isspace())
            )
        partial = strip_tags(out.partial_transcript) if not last else ""
        if partial and partial != self.last_partial:
            self.seq += 1
            self.last_partial = partial
            ev.append(
                {
                    "type": "partial",
                    "target": self.name,
                    "segment": self.segment,
                    "seq": self.seq,
                    "text": partial,
                    "audioEnd": round(end / SR, 3),
                }
            )
        if last:
            self.stream_open = False
        return ev


def pcm16_to_float(b: bytes) -> np.ndarray:
    return np.frombuffer(b, dtype="<i2").astype(np.float32) / 32768.0
