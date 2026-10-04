"""Whisper through Hugging Face transformers, loaded for one job (R45's one-off allowance; plan decision 1): the
``whisper_transcribe`` pseudo-label member, with Whisper's language token as its ``detectedLanguage`` (the ensemble's
second opinion on the language; lid_classify@2 in the omni pack is the classifier).

transformers (5.x), torch and huggingface_hub come from the nemo-speech image; nothing here is pinned. The weights are
read from the Hugging Face cache at the auxiliary's pinned revision (``HF_HOME``, or a read-only cache in
``CADENCE_HF_READONLY_CACHES``) and downloaded into ``HF_HOME`` only when no cache holds them, so after one fetch a job
runs offline. The pure parts (language tokens, ranking, batching) are unit-tested without transformers.
"""

from __future__ import annotations

import os
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from cadence_worker.steps.base import StepInputError

SR = 16000
WINDOW_S = 30.0  # Whisper's receptive field; longer audio decodes long-form (sequential windows with timestamps)
# What a snapshot needs: config, tokenizer, preprocessor and the safetensors weights (never the .bin or Flax copies).
ALLOW = ["*.json", "*.txt", "*.safetensors", "*.tiktoken", "*.model"]


def language_code(token: str) -> str:
    """``<|he|>`` → ``he``."""
    return token.removeprefix("<|").removesuffix("|>")


def rank_languages(probs: Sequence[float], codes: Sequence[str], k: int) -> list[tuple[str, float]]:
    """The k most probable languages with their probabilities, most probable first (ties by code)."""
    order = sorted(range(len(codes)), key=lambda i: (-probs[i], codes[i]))
    return [(codes[i], float(probs[i])) for i in order[:k]]


def softmax(x: np.ndarray[Any, np.dtype[np.float64]]) -> np.ndarray[Any, np.dtype[np.float64]]:
    z = x - x.max(axis=-1, keepdims=True)
    e = np.exp(z)
    out: np.ndarray[Any, np.dtype[np.float64]] = e / e.sum(axis=-1, keepdims=True)
    return out


def batches(durations: Sequence[float], size: int) -> list[list[int]]:
    """Indices in batches of ``size``; audio longer than Whisper's window decodes alone (long-form)."""
    out: list[list[int]] = []
    cur: list[int] = []
    for i, d in enumerate(durations):
        if d > WINDOW_S:
            out.append([i])
            continue
        cur.append(i)
        if len(cur) >= size:
            out.append(cur)
            cur = []
    if cur:
        out.append(cur)
    return out


def whisper_language(tag: str, known: Iterable[str]) -> str:
    """The Whisper language code of a BCP-47 tag (``he-IL`` → ``he``), or StepInputError when Whisper lacks it."""
    code = tag.split("-", 1)[0].lower()
    if code not in set(known):
        raise StepInputError(f"Whisper has no language {code!r} (from {tag!r}); pick another member or language")
    return code


def snapshot(repo: str, revision: str) -> Path:
    """The model directory at its pinned revision: from a cache when one holds it, else downloaded into HF_HOME."""
    import huggingface_hub as hf  # from the nemo-speech image

    caches: list[str | None] = [None, *filter(None, os.environ.get("CADENCE_HF_READONLY_CACHES", "").split(":"))]
    for cache in caches:
        try:
            return Path(
                hf.snapshot_download(
                    repo, revision=revision, cache_dir=cache, allow_patterns=ALLOW, local_files_only=True
                )
            )
        except Exception:  # not in this cache (LocalEntryNotFoundError and friends): try the next
            continue
    try:
        return Path(hf.snapshot_download(repo, revision=revision, allow_patterns=ALLOW))
    except Exception as e:
        raise StepInputError(f"{repo}@{revision} is in no Hugging Face cache and cannot be downloaded: {e}") from e


@dataclass
class Detection:
    language: str
    confidence: float
    top: list[tuple[str, float]]


class Whisper:
    """A Whisper model and its processor on one device, for one job."""

    def __init__(self, repo: str, revision: str, device: str) -> None:
        import torch
        from transformers import WhisperForConditionalGeneration, WhisperProcessor  # from the nemo-speech image

        path = snapshot(repo, revision)
        self.device = device
        self.dtype = torch.float16 if device == "cuda" else torch.float32
        self.processor: Any = WhisperProcessor.from_pretrained(path)
        self.model: Any = WhisperForConditionalGeneration.from_pretrained(path, dtype=self.dtype).to(device).eval()
        lang_to_id: Mapping[str, int] = getattr(self.model.generation_config, "lang_to_id", None) or {}
        if not lang_to_id:
            raise StepInputError(f"{repo} is not a multilingual Whisper (its generation config has no lang_to_id)")
        self.codes = [language_code(t) for t in lang_to_id]
        self.lang_ids = list(lang_to_id.values())

    def features(self, clips: Sequence[np.ndarray[Any, np.dtype[np.float32]]]) -> dict[str, Any]:
        long_form = any(len(c) > WINDOW_S * SR for c in clips)
        kw: dict[str, Any] = {"sampling_rate": SR, "return_tensors": "pt", "return_attention_mask": True}
        if long_form:
            kw.update(truncation=False, padding="longest")
        f = self.processor.feature_extractor(list(clips), **kw)
        out = {"input_features": f.input_features.to(self.device, self.dtype)}
        if long_form:
            out["attention_mask"] = f.attention_mask.to(self.device)
        return out

    def detect(self, clips: Sequence[np.ndarray[Any, np.dtype[np.float32]]], top_k: int = 3) -> list[Detection]:
        """Whisper's language identification: the distribution over its language tokens after the start token, from
        the first 30 s of each clip."""
        import torch

        heads = [c[: int(WINDOW_S * SR)] for c in clips]
        feats = self.processor.feature_extractor(heads, sampling_rate=SR, return_tensors="pt").input_features
        start = self.model.generation_config.decoder_start_token_id
        with torch.inference_mode():
            enc = self.model.get_encoder()(feats.to(self.device, self.dtype))
            ids = torch.full((len(heads), 1), start, dtype=torch.long, device=self.device)
            logits = self.model(encoder_outputs=enc, decoder_input_ids=ids).logits[:, -1, :]
            sel = logits[:, self.lang_ids].float().cpu().numpy().astype(np.float64)
        probs = softmax(sel)
        out = []
        for row in probs:
            top = rank_languages(row.tolist(), self.codes, max(1, top_k))
            out.append(Detection(language=top[0][0], confidence=top[0][1], top=top))
        return out

    def transcribe(
        self, clips: Sequence[np.ndarray[Any, np.dtype[np.float32]]], language: str, num_beams: int
    ) -> list[str]:
        """Decode with the language forced (task transcribe); long clips decode long-form with timestamps."""
        import torch

        feats = self.features(clips)
        long_form = "attention_mask" in feats
        kw: dict[str, Any] = {"language": language, "task": "transcribe", "num_beams": num_beams}
        if long_form:
            kw.update(return_timestamps=True, condition_on_prev_tokens=False)
        with torch.inference_mode():
            ids = self.model.generate(**feats, **kw)
        if not isinstance(ids, torch.Tensor):  # long-form returns a dict in some versions
            ids = ids["sequences"]
        texts: list[str] = self.processor.batch_decode(ids, skip_special_tokens=True)
        return [t.strip() for t in texts]
