"""A client of the OASIS ASR contract (wire package ``oasis.v1``, vendored at proto/oasis_contract/v1/asr.proto; source
commit in proto/SOURCE.yaml): the unary ``Transcribe`` and ``GetModelInfo`` calls the pseudo-label member needs.

The two methods are called by their full names over a plain channel, with the generated messages
(``oasis_gen/asr_pb2``) as serialisers, so no generated stub module is involved. A bearer token (OASIS's
``security.mode: isolated``) goes in the ``authorization`` metadata of every call. A call the service does not answer
(unavailable, deadline exceeded) raises :class:`~cadence_worker.steps.base.AuxiliaryUnavailable`; a call it refuses
(unauthenticated, invalid argument) raises :class:`~cadence_worker.steps.base.StepInputError`, since retrying does not
help.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import grpc

from cadence_services.oasis_gen import asr_pb2
from cadence_worker.steps.base import AuxiliaryUnavailable, StepInputError

PROTOCOL = "oasis.v1"
TRANSCRIBE = "/oasis.v1.Asr/Transcribe"
MODEL_INFO = "/oasis.v1.Asr/GetModelInfo"
SAMPLE_RATE = 16000

UNANSWERED = {grpc.StatusCode.UNAVAILABLE, grpc.StatusCode.DEADLINE_EXCEEDED, grpc.StatusCode.CANCELLED}
REASONS = {0: "UNSPECIFIED", 1: "OK", 2: "NON_SPEECH", 3: "DEGRADED"}


@dataclass(frozen=True)
class Info:
    engine_id: str
    model_version: str
    supported_languages: list[str] = field(default_factory=list)  # empty: unconstrained


@dataclass(frozen=True)
class Result:
    text: str
    confidence: float
    reason: str
    model_version: str


def _status(e: grpc.RpcError) -> grpc.StatusCode | None:
    code = getattr(e, "code", None)
    return code() if callable(code) else None


def _details(e: grpc.RpcError) -> str:
    details = getattr(e, "details", None)
    return str(details()) if callable(details) else str(e)


class Client:
    """One channel to one endpoint, for one step."""

    def __init__(self, endpoint: str, token: str = "", timeout_s: float = 60.0, health_timeout_s: float = 5.0) -> None:
        self.endpoint = endpoint
        self.timeout_s = timeout_s
        self.health_timeout_s = health_timeout_s
        self._metadata: tuple[tuple[str, str | bytes], ...] = (("authorization", f"Bearer {token}"),) if token else ()
        self._channel = grpc.insecure_channel(endpoint)
        self._transcribe = self._channel.unary_unary(
            TRANSCRIBE,
            request_serializer=asr_pb2.TranscribeRequest.SerializeToString,
            response_deserializer=asr_pb2.FinalResult.FromString,
        )
        self._info = self._channel.unary_unary(
            MODEL_INFO,
            request_serializer=asr_pb2.GetModelInfoRequest.SerializeToString,
            response_deserializer=asr_pb2.ModelInfo.FromString,
        )

    def close(self) -> None:
        self._channel.close()

    def __enter__(self) -> Client:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def _fail(self, what: str, e: grpc.RpcError) -> Exception:
        code = _status(e)
        msg = f"{what} at {self.endpoint}: {code.name if code else 'error'}: {_details(e)}"
        if code in UNANSWERED:
            return AuxiliaryUnavailable(msg + " (Cadence never starts the service; start it and retry)")
        if code in (grpc.StatusCode.UNAUTHENTICATED, grpc.StatusCode.PERMISSION_DENIED):
            return StepInputError(msg + " (check the token secret the auxiliary names)")
        return StepInputError(msg)

    def model_info(self) -> Info:
        """GetModelInfo: the health check before any audio is sent."""
        try:
            info = self._info(asr_pb2.GetModelInfoRequest(), timeout=self.health_timeout_s, metadata=self._metadata)
        except grpc.RpcError as e:
            raise self._fail("GetModelInfo", e) from e
        return Info(
            engine_id=info.engine_id,
            model_version=info.model_version,
            supported_languages=list(info.supported_languages),
        )

    def transcribe(self, pcm: bytes, lang: str, campaign_id: str = "") -> Result:
        """One-shot Transcribe of 16 kHz PCM16LE mono audio in a mandatory language (a bare code: sr, hr, he, …)."""
        req = asr_pb2.TranscribeRequest(lang=lang, campaign_id=campaign_id, sample_rate=SAMPLE_RATE, pcm=pcm)
        try:
            r = self._transcribe(req, timeout=self.timeout_s, metadata=self._metadata)
        except grpc.RpcError as e:
            raise self._fail("Transcribe", e) from e
        return Result(
            text=r.text,
            confidence=float(r.confidence),
            reason=REASONS.get(int(r.reason), str(r.reason)),
            model_version=r.model_version,
        )
