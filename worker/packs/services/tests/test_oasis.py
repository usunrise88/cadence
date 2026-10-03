"""oasis_transcribe@1 against a fake OASIS: an in-process gRPC server that answers the vendored contract's
``Transcribe`` and ``GetModelInfo`` (the real service is never started). Audio is synthesised (no licence needed)."""

from __future__ import annotations

import json
import socket
from collections.abc import Iterator
from concurrent import futures
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import grpc
import numpy as np
import pytest

from cadence_services import oasis
from cadence_services.oasis_gen import asr_pb2
from cadence_services.steps.oasis_member import OasisParams, OasisTranscribeStep
from cadence_worker import audio as audio_io
from cadence_worker.cas import hash_file
from cadence_worker.errors import classify
from cadence_worker.steps.base import AuxiliaryUnavailable, StepInputError
from cadence_worker.steps.context import StepContext

TOKEN = "s3cret"


@dataclass
class Fake:
    """What the fake service saw and how it answers."""

    languages: list[str] = field(default_factory=lambda: ["ru", "sr"])
    requests: list[tuple[str, int, int]] = field(default_factory=list)  # (lang, sample rate, pcm bytes)
    auth: list[str] = field(default_factory=list)

    def transcribe(self, req: asr_pb2.TranscribeRequest, ctx: grpc.ServicerContext) -> asr_pb2.FinalResult:
        auth = str(dict(ctx.invocation_metadata()).get("authorization", ""))
        self.auth.append(auth)
        if auth != f"Bearer {TOKEN}":
            ctx.abort(grpc.StatusCode.UNAUTHENTICATED, "missing bearer token")
        self.requests.append((req.lang, req.sample_rate, len(req.pcm)))
        loud = np.abs(np.frombuffer(req.pcm, dtype="<i2")).max() > 1000
        if not loud:
            return asr_pb2.FinalResult(text="", confidence=0.99, reason=asr_pb2.NON_SPEECH, model_version="fake@1")
        return asr_pb2.FinalResult(text="Добар дан", confidence=0.875, reason=asr_pb2.OK, model_version="fake@1")

    def info(self, req: asr_pb2.GetModelInfoRequest, ctx: grpc.ServicerContext) -> asr_pb2.ModelInfo:
        return asr_pb2.ModelInfo(engine_id="fake-ensemble", model_version="fake@1", supported_languages=self.languages)


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


@pytest.fixture
def fake() -> Iterator[tuple[Fake, str]]:
    f = Fake()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
    handlers: dict[str, grpc.RpcMethodHandler[Any, Any]] = {
        "Transcribe": grpc.unary_unary_rpc_method_handler(
            f.transcribe,
            request_deserializer=asr_pb2.TranscribeRequest.FromString,
            response_serializer=asr_pb2.FinalResult.SerializeToString,
        ),
        "GetModelInfo": grpc.unary_unary_rpc_method_handler(
            f.info,
            request_deserializer=asr_pb2.GetModelInfoRequest.FromString,
            response_serializer=asr_pb2.ModelInfo.SerializeToString,
        ),
    }
    server.add_generic_rpc_handlers((grpc.method_handlers_generic_handler("oasis.v1.Asr", handlers),))
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    try:
        yield f, f"127.0.0.1:{port}"
    finally:
        server.stop(None)


def _dataset(root: Path, language: str = "sr-RS") -> list[str]:
    """Two 8 kHz clips (a tone and silence): the member resamples them to 16 kHz PCM16 for the service."""
    (root / "audio").mkdir(parents=True)
    t = np.arange(8000) / 8000
    clips = {"tone.wav": 0.5 * np.sin(2 * np.pi * 440 * t), "silence.wav": np.zeros(8000)}
    rows = []
    for name, x in clips.items():
        (root / "audio" / name).write_bytes(audio_io.wav_bytes(audio_io.Audio(x, 8000, 1)))
        rows.append({"audio": f"audio/{name}", "duration": 1.0, "language": language})
    (root / "dataset.json").write_text(json.dumps({"format": "cadence.dataset/1", "name": "fx"}), encoding="utf-8")
    (root / "manifest.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    return [hash_file(root / "audio" / n) for n in clips]


def _aux(endpoint: str, protocol: str = oasis.PROTOCOL) -> dict[str, Any]:
    return {
        "versionId": "ver_oasis",
        "name": "auxiliary/oasis",
        "version": "2026-10-03.abcdef012345",
        "payload": {
            "roles": ["pseudolabel"],
            "licence": "Apache-2.0",
            "outputsCommercialUse": True,
            "languages": ["sr"],
            "service": {"kind": "grpc-asr", "endpoint": endpoint, "protocol": protocol, "tokenSecret": "oasis-token"},
        },
    }


def _run(
    tmp: Path, endpoint: str, monkeypatch: pytest.MonkeyPatch, **params: Any
) -> tuple[list[dict[str, Any]], StepContext]:
    monkeypatch.setenv("OASIS_TOKEN", TOKEN)
    ctx = StepContext(lambda e: None, work_dir=tmp, auxiliaries={"auxiliary": _aux(endpoint)})
    out = tmp / "hyp.jsonl"
    OasisTranscribeStep().run(OasisParams(**params), {"data": tmp / "data"}, {"hypotheses": out}, ctx)
    return [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()], ctx


def test_member_transcribes_every_utterance(
    tmp_path: Path, fake: tuple[Fake, str], monkeypatch: pytest.MonkeyPatch
) -> None:
    f, endpoint = fake
    tone, silence = _dataset(tmp_path / "data")
    rows, ctx = _run(tmp_path, endpoint, monkeypatch)
    by = {r["audio"]: r for r in rows}
    assert by[tone]["text"] == "Добар дан"
    assert by[tone]["confidence"] == 0.875
    assert by[tone]["vote"] is True
    assert by[tone]["member"] == "oasis"
    assert by[tone]["language"] == "sr"
    assert by[silence]["text"] == ""  # NON_SPEECH
    assert by[silence]["reason"] == "NON_SPEECH"
    # 1 s at 8 kHz went out as 1 s of 16 kHz PCM16: 32000 bytes, in the bare language code.
    assert sorted(f.requests) == [("sr", 16000, 32000), ("sr", 16000, 32000)]
    assert set(f.auth) == {f"Bearer {TOKEN}"}
    assert ctx.meta["hypotheses"]["modelVersion"] == "fake@1"


def test_transliteration_applies_to_the_text(
    tmp_path: Path, fake: tuple[Fake, str], monkeypatch: pytest.MonkeyPatch
) -> None:
    _, endpoint = fake
    _dataset(tmp_path / "data")
    rows, _ = _run(tmp_path, endpoint, monkeypatch, transliterate="sr-Cyrl-Latn")
    assert {r["text"] for r in rows} == {"Dobar dan", ""}


def test_a_language_the_service_lacks_is_refused_before_any_audio(
    tmp_path: Path, fake: tuple[Fake, str], monkeypatch: pytest.MonkeyPatch
) -> None:
    f, endpoint = fake
    _dataset(tmp_path / "data", language="he-IL")
    with pytest.raises(StepInputError, match="not 'he'"):
        _run(tmp_path, endpoint, monkeypatch)
    assert f.requests == []


def test_a_service_that_does_not_answer_is_auxiliary_unavailable(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _dataset(tmp_path / "data")
    with pytest.raises(AuxiliaryUnavailable) as exc:
        _run(tmp_path, f"127.0.0.1:{_free_port()}", monkeypatch, health_timeout_s=1)
    err = classify(exc.value)
    assert err["message"].startswith("auxiliary-unavailable: GetModelInfo")
    assert err.get("retryable") is True


def test_a_wrong_token_is_an_input_error(
    tmp_path: Path, fake: tuple[Fake, str], monkeypatch: pytest.MonkeyPatch
) -> None:
    _, endpoint = fake
    _dataset(tmp_path / "data")
    monkeypatch.setenv("OASIS_TOKEN", "wrong")
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": _aux(endpoint)})
    with pytest.raises(StepInputError, match="UNAUTHENTICATED"):
        OasisTranscribeStep().run(OasisParams(), {"data": tmp_path / "data"}, {"hypotheses": tmp_path / "h"}, ctx)


def test_another_protocol_is_refused(tmp_path: Path) -> None:
    _dataset(tmp_path / "data")
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": _aux("h:1", protocol="other.v2")})
    with pytest.raises(StepInputError, match=r"speaks 'other\.v2'"):
        OasisTranscribeStep().run(OasisParams(), {"data": tmp_path / "data"}, {"hypotheses": tmp_path / "h"}, ctx)
