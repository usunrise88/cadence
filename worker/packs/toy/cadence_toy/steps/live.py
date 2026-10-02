"""toy_live — the live role of toy-ctc: serve one transcription session on the CPU (R47, R48), so the live channel's
seams run end to end without a card. Each target streams its audio through the GRU with the hidden state carried, in
the profile's chunks (the ``offline`` profile streams in 320 ms chunks too: a session is always streaming); a
segment's final is the greedy CTC decode of its frames. ``finalize`` closes the segment and resets the state.
Help: docs/help/steps/toy-live.md.
"""

from __future__ import annotations

import time
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
import torch
from numpy.typing import NDArray
from pydantic import BaseModel

from cadence_toy.family import NAME, RUNTIME, profile
from cadence_toy.model import (
    FRAME_MS,
    HOP,
    N_FFT,
    SAMPLE_RATE,
    STACK,
    CharTokenizer,
    GreedyDecoder,
    TinyCTC,
    features,
    load_checkpoint,
    use_one_thread,
)
from cadence_toy.steps.materialize import read_base_model
from cadence_worker import live
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

DEFAULT_CHUNK_MS = 320
FRAME = HOP * STACK  # samples per model frame (20 ms)


@dataclass
class ToyDecoder:
    """A :class:`cadence_worker.live.Decoder` over the toy model."""

    target: str
    model: TinyCTC
    tok: CharTokenizer
    profile: str
    language: str
    chunk_ms: int = DEFAULT_CHUNK_MS
    load_s: float = 0.0
    decoder: str = "toy-greedy-ctc"
    boost: Mapping[str, Any] | None = None
    step_ms: list[float] = field(default_factory=list)
    buf: NDArray[np.float32] = field(default_factory=lambda: np.zeros(0, dtype=np.float32))
    h: torch.Tensor | None = None
    dec: GreedyDecoder | None = None
    seg_start: int = 0  # absolute sample where the segment began
    consumed: int = 0  # absolute samples whose frames were decoded
    segment: int = 0
    seq: int = 0
    last_partial: str = ""

    def _decoder(self) -> GreedyDecoder:
        if self.dec is None:
            self.dec = GreedyDecoder(self.tok)
        return self.dec

    @torch.no_grad()
    def _frames(self, final: bool) -> None:
        """Decode every whole model frame the buffer holds (all of it, zero-padded, when final)."""
        if final and self.buf.size:
            self.buf = np.pad(self.buf, (0, max(0, N_FFT - self.buf.size)))
        n = (self.buf.size - N_FFT) // HOP + 1 if self.buf.size >= N_FFT else 0
        usable = (n // STACK) * STACK
        if usable == 0:
            if final:
                self.consumed += int(self.buf.size)
                self.buf = np.zeros(0, dtype=np.float32)
            return
        t0 = time.perf_counter()
        feats = features(torch.from_numpy(np.ascontiguousarray(self.buf)))[: usable // STACK]
        logp, self.h = self.model(feats.unsqueeze(0), self.h)
        self._decoder().feed(logp[0])
        self.step_ms.append((time.perf_counter() - t0) * 1000)
        used = usable * HOP
        self.consumed += used
        self.buf = self.buf[used:]
        if final:
            self.consumed += int(self.buf.size)
            self.buf = np.zeros(0, dtype=np.float32)

    def push(self, x: NDArray[np.float32]) -> list[dict[str, Any]]:
        self.buf = np.concatenate([self.buf, np.asarray(x, dtype=np.float32)])
        chunk = round(self.chunk_ms / 1000 * SAMPLE_RATE)
        if self.buf.size < chunk + N_FFT:
            return []
        self._frames(final=False)
        text = self._decoder().text()
        if not text or text == self.last_partial:
            return []
        self.seq += 1
        self.last_partial = text
        return [
            {
                "type": "partial",
                "target": self.target,
                "segment": self.segment,
                "seq": self.seq,
                "text": text,
                "audioEnd": round(self.consumed / SAMPLE_RATE, 3),
            }
        ]

    def finalize(self, reason: str) -> list[dict[str, Any]]:
        self._frames(final=True)
        dec = self._decoder()
        off = self.seg_start / SAMPLE_RATE
        words = [
            {**w, "start": round(off + float(w["start"]), 3), "end": round(off + float(w["end"]), 3)}
            for w in dec.words()
        ]
        self.seq += 1
        ev = {
            "type": "final",
            "target": self.target,
            "segment": self.segment,
            "seq": self.seq,
            "text": dec.text(),
            "words": words,
            "endpoint": reason,
            "audioEnd": round(self.consumed / SAMPLE_RATE, 3),
            "space": True,
        }
        self.segment += 1
        self.last_partial = ""
        self.dec, self.h = None, None
        self.seg_start = self.consumed
        return [ev]


def load_model(path: Path) -> tuple[TinyCTC, CharTokenizer]:
    """A checkpoint directory, or a base model (the untrained network from its seed)."""
    if path.is_dir():
        return load_checkpoint(path)
    seed = read_base_model(path).get("seed", 0)
    if not isinstance(seed, int) or isinstance(seed, bool) or seed < 0:
        raise StepInputError(f"the base model's seed must be a non-negative integer, not {seed!r}")
    torch.manual_seed(seed)
    model = TinyCTC()
    model.eval()
    return model, CharTokenizer()


class LiveStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint", "base": "base_model", "audio": "audio"}
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"model", "base", "audio"})
    produces: ClassVar[Mapping[str, str]] = {}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "interactive"}
    role: ClassVar[str] = "live"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = live.LiveParams

    def decoders(self, p: live.LiveParams, inputs: Mapping[str, Path]) -> list[ToyDecoder]:
        models: dict[str, tuple[TinyCTC, CharTokenizer, float]] = {}
        out: list[ToyDecoder] = []
        for t in p.targets:
            if t.boost:
                raise StepInputError(f"target {t.target}: {NAME} cannot boost (capabilities.boosting is empty)")
            try:
                prof = profile(t.profile)
            except KeyError as e:
                raise StepInputError(f"target {t.target}: {NAME} has no latency profile {t.profile!r}") from e
            if t.model not in models:
                t0 = time.perf_counter()
                m, tok = load_model(inputs[t.model])
                models[t.model] = (m, tok, time.perf_counter() - t0)
            m, tok, load_s = models[t.model]
            chunk = int(prof.get("chunkMs") or DEFAULT_CHUNK_MS)
            out.append(ToyDecoder(t.target, m, tok, t.profile, t.language, chunk_ms=chunk, load_s=load_s))
        return out

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        p = live.LiveParams.model_validate(params.model_dump())
        live.check_targets(p, inputs)
        decoders = self.decoders(p, inputs)
        ctx.log("targets loaded", session=p.session, frameMs=FRAME_MS)
        channel = live.dial()
        ctx.progress(1.0, "live")
        try:
            summary = live.serve(
                channel,
                decoders,
                p,
                work_dir=ctx.work_dir,
                should_stop=ctx.should_stop,
                audio_path=inputs.get("audio"),
            )
        finally:
            channel.close(1000, "the job ended")
        ctx.log("session ended", session=p.session, audioS=(summary or {}).get("audioS"))
