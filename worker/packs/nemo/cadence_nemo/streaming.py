"""File decode in streaming simulation with the real cache-aware streaming decoder (spike A3 step 3: NeMo's
``speech_to_text_cache_aware_streaming_infer.py`` logic, chunk by chunk with the encoder caches carried; never an
offline decode re-labelled): ``att_context_size`` from the latency profile, greedy RNNT, the language prompt, tags
stripped from the text.

Per utterance: the final text, words, and partial events — after every chunk that advanced a stream, the audio it has
consumed (``audioOffsetMs``, including the chunk's right context), the wall time since the batch's decode started
(``emitMs``; the streams of a batch are decoded together) and the text so far. Word timings come from those emissions:
a word ends at the offset of the first partial that contains it and starts where the previous word ended (the
resolution is one chunk, i.e. the profile's latency); confidence comes from NeMo's word confidence when the decoder
reports token confidence (the minimum over a word's tokens; NeMo's own word aggregation miscounts the locale tag as a
word), else it is null.
"""

from __future__ import annotations

import re
import time
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


@dataclass
class Stream:
    frames: int  # feature frames of the utterance
    partials: list[dict[str, Any]] = field(default_factory=list)
    text: str = ""
    word_confidence: list[float] | None = None


def chunk_frames(chunk_size: Any, first: bool) -> int:
    """The feature frames a chunk covers (the first chunk of a cache-aware encoder is shorter)."""
    if isinstance(chunk_size, list | tuple):
        return int(chunk_size[0] if first else chunk_size[1])
    return int(chunk_size)


def record_partials(
    streams: Sequence[Stream], texts: Sequence[str], prev_frames: int, chunk: int, stride_ms: float, emit_ms: float
) -> None:
    """After one chunk: a partial event for every stream the chunk reached."""
    for s, text in zip(streams, texts, strict=True):
        if prev_frames >= s.frames:
            continue
        seen = min(prev_frames + chunk, s.frames)
        s.partials.append({"audioOffsetMs": round(seen * stride_ms), "emitMs": round(emit_ms, 2), "text": text})
        s.text = text


def words_from_partials(
    text: str, partials: Sequence[dict[str, Any]], confidence: Sequence[float] | None
) -> list[dict[str, Any]]:
    """Words of the final text timed by the emission that first contained them."""
    words = text.split()
    conf = list(confidence) if confidence is not None and len(confidence) == len(words) else None
    out: list[dict[str, Any]] = []
    prev_end = 0
    for i, w in enumerate(words):
        end = None
        for p in partials:
            if len(str(p["text"]).split()) > i:
                end = int(p["audioOffsetMs"])
                break
        if end is None:
            end = int(partials[-1]["audioOffsetMs"]) if partials else prev_end
        end = max(end, prev_end)
        out.append(
            {
                "word": w,
                "start": round(prev_end / 1000, 3),
                "end": round(end / 1000, 3),
                "confidence": round(float(conf[i]), 4) if conf else None,
            }
        )
        prev_end = end
    return out


def hyp_text(h: Any) -> str:
    return str(getattr(h, "text", h) or "")


TAG_PIECE = re.compile(r"▁?<(?:[a-z]{2,3}-[A-Z]{2}|auto)>")


def word_confidence(pieces: Sequence[str], confidence: Sequence[float]) -> list[float]:
    """Per word, the minimum confidence of its sentencepiece tokens (``▁`` starts a word; locale tags are skipped)."""
    words: list[float] = []
    cur: list[float] = []
    for piece, c in zip(pieces, confidence, strict=True):
        if TAG_PIECE.fullmatch(piece):
            continue
        if piece.startswith("▁"):
            if cur:
                words.append(min(cur))
            cur = [] if piece == "▁" else [float(c)]
        else:
            cur.append(float(c))
    if cur:
        words.append(min(cur))
    return words


def hyp_word_confidence(h: Any, ids_to_tokens: Callable[[list[int]], list[str]]) -> list[float] | None:
    tc = getattr(h, "token_confidence", None)
    seq = getattr(h, "y_sequence", None)
    if tc is None or seq is None:
        return None
    try:
        ids = [int(x) for x in (seq.tolist() if hasattr(seq, "tolist") else seq)]
        conf = [float(x) for x in tc]
        if len(ids) != len(conf):
            return None
        return word_confidence(ids_to_tokens(ids), conf)
    except (TypeError, ValueError):
        return None


@dataclass(frozen=True)
class Boost:
    """Static phrase boosting (NeMo's GPU phrase boosting tree, fused into greedy RNNT label-looping decoding):
    ``final score = acoustic score + weight * boosting-tree score``; ``context_score`` and ``depth_scaling`` shape the
    tree (NeMo's defaults for RNNT)."""

    terms: tuple[str, ...]
    weight: float
    list_hash: str
    context_score: float = 1.0
    depth_scaling: float = 2.0

    def decoding(self) -> dict[str, Any]:
        return {
            "method": "nemo-phrase-boosting",
            "list": self.list_hash,
            "terms": len(self.terms),
            "weight": self.weight,
            "contextScore": self.context_score,
            "depthScaling": self.depth_scaling,
        }


def prepare(model: Any, att_context_size: list[int], prompt_key: str, boost: Boost | None = None) -> dict[str, Any]:
    """Put the model in streaming mode at a profile, with static phrase boosting when ``boost`` is given: returns the
    decoding configuration it now uses (without a boost list it is the same as before boosting existed, so its hash
    is too)."""
    from nemo.collections.asr.parts.submodules.rnnt_decoding import RNNTDecodingConfig
    from omegaconf import OmegaConf, open_dict

    model.encoder.set_default_att_context_size(att_context_size=att_context_size)
    dcfg = OmegaConf.structured(RNNTDecodingConfig(fused_batch_size=-1, strategy="greedy_batch"))
    with open_dict(dcfg):
        dcfg.confidence_cfg.preserve_token_confidence = True
        if boost is not None:
            dcfg.greedy.loop_labels = True  # the label-looping decoder is the one that fuses a boosting tree
            dcfg.greedy.boosting_tree.key_phrases_list = list(boost.terms)
            dcfg.greedy.boosting_tree.context_score = boost.context_score
            dcfg.greedy.boosting_tree.depth_scaling = boost.depth_scaling
            dcfg.greedy.boosting_tree_alpha = boost.weight
    model.change_decoding_strategy(dcfg, verbose=False)
    model.set_inference_prompt(prompt_key)
    model.decoding.set_strip_lang_tags(True)
    model.eval()
    model.encoder.setup_streaming_params()
    out: dict[str, Any] = {
        "decoder": "rnnt-greedy-batch",
        "attContextSize": list(att_context_size),
        "targetLang": prompt_key,
        "stripLangTags": True,
        "wordConfidence": "min",
        "wordTimestamps": "emission",
    }
    if boost is not None:
        out["boost"] = boost.decoding()
    return out


def decode_batch(model: Any, files: Sequence[Path], stride_ms: float) -> list[Stream]:
    """Stream a batch of files through the cache-aware encoder; one :class:`Stream` per file."""
    import torch
    from nemo.collections.asr.parts.utils.streaming_utils import CacheAwareStreamingAudioBuffer

    buf = CacheAwareStreamingAudioBuffer(model=model, online_normalization=False, pad_and_drop_preencoded=False)
    for f in files:
        buf.append_audio_file(str(f), stream_id=-1)
    lengths = [int(x) for x in buf.streams_length.tolist()]
    streams = [Stream(frames=n) for n in lengths]
    cfg = model.encoder.streaming_cfg
    cache_ch, cache_t, cache_len = model.encoder.get_initial_cache_state(batch_size=len(files))
    prev_hyps = None
    pred_out = None
    t0 = time.monotonic()
    prev = 0
    hyps: Any = None
    for step, (chunk_audio, chunk_lengths) in enumerate(buf):
        with torch.inference_mode():
            (pred_out, hyps, cache_ch, cache_t, cache_len, prev_hyps) = model.conformer_stream_step(
                processed_signal=chunk_audio,
                processed_signal_length=chunk_lengths,
                cache_last_channel=cache_ch,
                cache_last_time=cache_t,
                cache_last_channel_len=cache_len,
                keep_all_outputs=buf.is_buffer_empty(),
                previous_hypotheses=prev_hyps,
                previous_pred_out=pred_out,
                drop_extra_pre_encoded=0 if step == 0 else cfg.drop_extra_pre_encoded,
                return_transcription=True,
            )
        record_partials(
            streams,
            [hyp_text(h) for h in hyps],
            prev,
            chunk_frames(cfg.chunk_size, step == 0),
            stride_ms,
            (time.monotonic() - t0) * 1000,
        )
        prev = int(buf.buffer_idx)
    for s, h in zip(streams, hyps or [], strict=False):
        s.text = hyp_text(h)
        s.word_confidence = hyp_word_confidence(h, model.tokenizer.ids_to_tokens)
        if s.partials:
            s.partials[-1]["final"] = True
            s.partials[-1]["text"] = s.text
    return streams


def decode_all(
    model: Any, files: Sequence[Path], batch_size: int, stride_ms: float, progress: Callable[[int, int], None]
) -> list[Stream]:
    out: list[Stream] = []
    for i in range(0, len(files), batch_size):
        out.extend(decode_batch(model, files[i : i + batch_size], stride_ms))
        progress(min(len(files), i + batch_size), len(files))
    return out
