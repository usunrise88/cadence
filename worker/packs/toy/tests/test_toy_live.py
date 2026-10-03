"""toy_live end to end over a fake socket: a fixture clip streamed as the microphone gives the toy model's offline
decode, a finalize splits it into segments, and the kind's descriptor fills the live role."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np
import pytest
import torch

from cadence_toy.family import FAMILY
from cadence_toy.model import decode_offline, read_wav
from cadence_toy.steps.live import LiveStep, ToyDecoder
from cadence_worker import live
from cadence_worker.steps.base import StepInputError, descriptor, missing_metadata

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_toy" / "fixtures"


class Channel:
    def __init__(self, messages: list[str | bytes]) -> None:
        self.inbox = messages
        self.sent: list[dict[str, Any]] = []
        self.closed: tuple[int, str] | None = None

    def recv(self, timeout: float) -> str | bytes | None:
        if self.inbox:
            return self.inbox.pop(0)
        raise live.ChannelClosedError("closed")

    def send(self, text: str) -> None:
        self.sent.append(json.loads(text))

    def close(self, code: int = 1000, reason: str = "") -> None:
        self.closed = (code, reason)


def base_model(tmp: Path, seed: int = 3) -> Path:
    doc = {"format": "cadence.base_model/1", "family": {"name": "toy-ctc"}, "model": {"seed": seed}}
    (tmp / "base.json").write_text(json.dumps(doc), encoding="utf-8")
    return tmp / "base.json"


def params(**kw: Any) -> live.LiveParams:
    doc: dict[str, Any] = {
        "session": "trs_toy",
        "targets": [{"target": "A", "model": "base.0", "profile": "320ms", "language": "en"}],
        "input": {"kind": "microphone"},
    }
    doc.update(kw)
    return live.LiveParams.model_validate(doc)


def pcm16(x: torch.Tensor) -> bytes:
    return bytes(np.clip(np.round(x.numpy() * 32768), -32768, 32767).astype("<i2").tobytes())


def test_toy_live_streams_the_offline_decode(tmp_path: Path) -> None:
    step = LiveStep()
    inputs = {"base.0": base_model(tmp_path)}
    (dec,) = step.decoders(params(), inputs)
    assert isinstance(dec, ToyDecoder)
    samples = read_wav(FIXTURES / "clip01.wav")
    frames: list[str | bytes] = [pcm16(samples[i : i + 320]) for i in range(0, samples.numel(), 320)]
    start = json.dumps({"type": "start", "input": {"kind": "microphone", "sampleRate": 16000}})
    ch = Channel([start, *frames, json.dumps({"type": "end"})])
    summary = live.serve(ch, [dec], params(), work_dir=tmp_path)
    assert summary is not None
    finals = [m for m in ch.sent if m["type"] == "final"]
    assert len(finals) == 1
    offline = decode_offline(dec.model, dec.tok, samples)
    assert finals[0]["text"] == offline.text(), "frame-local features: streaming equals the offline decode"
    assert [w["word"] for w in finals[0]["words"]] == [w["word"] for w in offline.words()]
    partials = [m for m in ch.sent if m["type"] == "partial"]
    assert all(p["target"] == "A" for p in partials)
    assert ch.sent[0]["targets"][0]["chunkMs"] == 320
    assert ch.closed == (1000, "summary sent")


def test_toy_live_finalize_starts_a_new_segment(tmp_path: Path) -> None:
    (dec,) = LiveStep().decoders(params(), {"base.0": base_model(tmp_path)})
    samples = read_wav(FIXTURES / "clip02.wav")
    half = samples.numel() // 2
    msgs: list[str | bytes] = [json.dumps({"type": "start", "input": {"kind": "microphone", "sampleRate": 16000}})]
    msgs += [
        pcm16(samples[:half]),
        json.dumps({"type": "finalize"}),
        pcm16(samples[half:]),
        json.dumps({"type": "end"}),
    ]
    ch = Channel(msgs)
    live.serve(ch, [dec], params(), work_dir=tmp_path)
    finals = [m for m in ch.sent if m["type"] == "final"]
    assert [(f["segment"], f["endpoint"]) for f in finals] == [(0, "finalize"), (1, "end")]
    assert finals[1]["audioEnd"] == pytest.approx(samples.numel() / 16000, abs=0.03)
    second = [w for w in finals[1]["words"]]
    assert all(w["start"] >= finals[0]["audioEnd"] - 1e-6 for w in second), "word times are session audio time"


def test_toy_live_descriptor_and_refusals(tmp_path: Path) -> None:
    d = descriptor("toy_live", LiveStep)
    assert d.get("role") == "live"
    assert d["resources"].get("jobKind") == "interactive"
    assert d["resources"].get("gpu") is False
    assert missing_metadata(LiveStep) == []
    assert FAMILY.descriptor["roles"]["live"] == "toy_live"
    assert FAMILY.descriptor.get("interactive") == {"memoryMb": 512}
    with pytest.raises(StepInputError, match="cannot boost"):
        LiveStep().decoders(
            params(targets=[{"target": "A", "model": "base.0", "profile": "320ms", "language": "en", "boost": "b"}]),
            {"base.0": base_model(tmp_path)},
        )
    with pytest.raises(StepInputError, match="no latency profile"):
        LiveStep().decoders(
            params(targets=[{"target": "A", "model": "base.0", "profile": "80ms", "language": "en"}]),
            {"base.0": base_model(tmp_path)},
        )
