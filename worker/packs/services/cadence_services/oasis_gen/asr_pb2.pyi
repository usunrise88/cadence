from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ResultReason(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RESULT_REASON_UNSPECIFIED: _ClassVar[ResultReason]
    OK: _ClassVar[ResultReason]
    NON_SPEECH: _ClassVar[ResultReason]
    DEGRADED: _ClassVar[ResultReason]
RESULT_REASON_UNSPECIFIED: ResultReason
OK: ResultReason
NON_SPEECH: ResultReason
DEGRADED: ResultReason

class ClientEvent(_message.Message):
    __slots__ = ("config", "audio", "vad", "barge_in", "postback", "boost", "close")
    CONFIG_FIELD_NUMBER: _ClassVar[int]
    AUDIO_FIELD_NUMBER: _ClassVar[int]
    VAD_FIELD_NUMBER: _ClassVar[int]
    BARGE_IN_FIELD_NUMBER: _ClassVar[int]
    POSTBACK_FIELD_NUMBER: _ClassVar[int]
    BOOST_FIELD_NUMBER: _ClassVar[int]
    CLOSE_FIELD_NUMBER: _ClassVar[int]
    config: SessionConfig
    audio: AudioFrame
    vad: VadEvent
    barge_in: BargeIn
    postback: Postback
    boost: PhraseBoost
    close: Close
    def __init__(self, config: _Optional[_Union[SessionConfig, _Mapping]] = ..., audio: _Optional[_Union[AudioFrame, _Mapping]] = ..., vad: _Optional[_Union[VadEvent, _Mapping]] = ..., barge_in: _Optional[_Union[BargeIn, _Mapping]] = ..., postback: _Optional[_Union[Postback, _Mapping]] = ..., boost: _Optional[_Union[PhraseBoost, _Mapping]] = ..., close: _Optional[_Union[Close, _Mapping]] = ...) -> None: ...

class SessionConfig(_message.Message):
    __slots__ = ("session_id", "client_id", "campaign_id", "lang", "sample_rate", "codec_hint")
    SESSION_ID_FIELD_NUMBER: _ClassVar[int]
    CLIENT_ID_FIELD_NUMBER: _ClassVar[int]
    CAMPAIGN_ID_FIELD_NUMBER: _ClassVar[int]
    LANG_FIELD_NUMBER: _ClassVar[int]
    SAMPLE_RATE_FIELD_NUMBER: _ClassVar[int]
    CODEC_HINT_FIELD_NUMBER: _ClassVar[int]
    session_id: str
    client_id: str
    campaign_id: str
    lang: str
    sample_rate: int
    codec_hint: str
    def __init__(self, session_id: _Optional[str] = ..., client_id: _Optional[str] = ..., campaign_id: _Optional[str] = ..., lang: _Optional[str] = ..., sample_rate: _Optional[int] = ..., codec_hint: _Optional[str] = ...) -> None: ...

class AudioFrame(_message.Message):
    __slots__ = ("pcm", "seq", "t_ms")
    PCM_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    T_MS_FIELD_NUMBER: _ClassVar[int]
    pcm: bytes
    seq: int
    t_ms: int
    def __init__(self, pcm: _Optional[bytes] = ..., seq: _Optional[int] = ..., t_ms: _Optional[int] = ...) -> None: ...

class VadEvent(_message.Message):
    __slots__ = ("kind", "t_ms")
    class Kind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        KIND_UNSPECIFIED: _ClassVar[VadEvent.Kind]
        VAD_START: _ClassVar[VadEvent.Kind]
        VAD_END: _ClassVar[VadEvent.Kind]
    KIND_UNSPECIFIED: VadEvent.Kind
    VAD_START: VadEvent.Kind
    VAD_END: VadEvent.Kind
    KIND_FIELD_NUMBER: _ClassVar[int]
    T_MS_FIELD_NUMBER: _ClassVar[int]
    kind: VadEvent.Kind
    t_ms: int
    def __init__(self, kind: _Optional[_Union[VadEvent.Kind, str]] = ..., t_ms: _Optional[int] = ...) -> None: ...

class BargeIn(_message.Message):
    __slots__ = ("t_ms",)
    T_MS_FIELD_NUMBER: _ClassVar[int]
    t_ms: int
    def __init__(self, t_ms: _Optional[int] = ...) -> None: ...

class Postback(_message.Message):
    __slots__ = ("kind", "turn_ref", "details")
    class Kind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        KIND_UNSPECIFIED: _ClassVar[Postback.Kind]
        USER_RETRY: _ClassVar[Postback.Kind]
    KIND_UNSPECIFIED: Postback.Kind
    USER_RETRY: Postback.Kind
    KIND_FIELD_NUMBER: _ClassVar[int]
    TURN_REF_FIELD_NUMBER: _ClassVar[int]
    DETAILS_FIELD_NUMBER: _ClassVar[int]
    kind: Postback.Kind
    turn_ref: str
    details: str
    def __init__(self, kind: _Optional[_Union[Postback.Kind, str]] = ..., turn_ref: _Optional[str] = ..., details: _Optional[str] = ...) -> None: ...

class PhraseBoost(_message.Message):
    __slots__ = ("phrases", "scope")
    class Scope(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        SCOPE_UNSPECIFIED: _ClassVar[PhraseBoost.Scope]
        NEXT_TURN_ONLY: _ClassVar[PhraseBoost.Scope]
        ALL_TURNS: _ClassVar[PhraseBoost.Scope]
    SCOPE_UNSPECIFIED: PhraseBoost.Scope
    NEXT_TURN_ONLY: PhraseBoost.Scope
    ALL_TURNS: PhraseBoost.Scope
    class Phrase(_message.Message):
        __slots__ = ("phrase", "weight")
        PHRASE_FIELD_NUMBER: _ClassVar[int]
        WEIGHT_FIELD_NUMBER: _ClassVar[int]
        phrase: str
        weight: float
        def __init__(self, phrase: _Optional[str] = ..., weight: _Optional[float] = ...) -> None: ...
    PHRASES_FIELD_NUMBER: _ClassVar[int]
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    phrases: _containers.RepeatedCompositeFieldContainer[PhraseBoost.Phrase]
    scope: PhraseBoost.Scope
    def __init__(self, phrases: _Optional[_Iterable[_Union[PhraseBoost.Phrase, _Mapping]]] = ..., scope: _Optional[_Union[PhraseBoost.Scope, str]] = ...) -> None: ...

class Close(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class TranscribeRequest(_message.Message):
    __slots__ = ("lang", "campaign_id", "sample_rate", "pcm", "context_pcm", "content_offset_ms", "content_dur_ms", "boost")
    LANG_FIELD_NUMBER: _ClassVar[int]
    CAMPAIGN_ID_FIELD_NUMBER: _ClassVar[int]
    SAMPLE_RATE_FIELD_NUMBER: _ClassVar[int]
    PCM_FIELD_NUMBER: _ClassVar[int]
    CONTEXT_PCM_FIELD_NUMBER: _ClassVar[int]
    CONTENT_OFFSET_MS_FIELD_NUMBER: _ClassVar[int]
    CONTENT_DUR_MS_FIELD_NUMBER: _ClassVar[int]
    BOOST_FIELD_NUMBER: _ClassVar[int]
    lang: str
    campaign_id: str
    sample_rate: int
    pcm: bytes
    context_pcm: bytes
    content_offset_ms: int
    content_dur_ms: int
    boost: PhraseBoost
    def __init__(self, lang: _Optional[str] = ..., campaign_id: _Optional[str] = ..., sample_rate: _Optional[int] = ..., pcm: _Optional[bytes] = ..., context_pcm: _Optional[bytes] = ..., content_offset_ms: _Optional[int] = ..., content_dur_ms: _Optional[int] = ..., boost: _Optional[_Union[PhraseBoost, _Mapping]] = ...) -> None: ...

class GetModelInfoRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class ServerEvent(_message.Message):
    __slots__ = ("partial", "early_endpoint", "final", "error")
    PARTIAL_FIELD_NUMBER: _ClassVar[int]
    EARLY_ENDPOINT_FIELD_NUMBER: _ClassVar[int]
    FINAL_FIELD_NUMBER: _ClassVar[int]
    ERROR_FIELD_NUMBER: _ClassVar[int]
    partial: PartialResult
    early_endpoint: EarlyEndpoint
    final: FinalResult
    error: ErrorEvent
    def __init__(self, partial: _Optional[_Union[PartialResult, _Mapping]] = ..., early_endpoint: _Optional[_Union[EarlyEndpoint, _Mapping]] = ..., final: _Optional[_Union[FinalResult, _Mapping]] = ..., error: _Optional[_Union[ErrorEvent, _Mapping]] = ...) -> None: ...

class L1Intent(_message.Message):
    __slots__ = ("top", "prob", "distribution")
    class DistributionEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: float
        def __init__(self, key: _Optional[str] = ..., value: _Optional[float] = ...) -> None: ...
    TOP_FIELD_NUMBER: _ClassVar[int]
    PROB_FIELD_NUMBER: _ClassVar[int]
    DISTRIBUTION_FIELD_NUMBER: _ClassVar[int]
    top: str
    prob: float
    distribution: _containers.ScalarMap[str, float]
    def __init__(self, top: _Optional[str] = ..., prob: _Optional[float] = ..., distribution: _Optional[_Mapping[str, float]] = ...) -> None: ...

class Timings(_message.Message):
    __slots__ = ("queue_ms", "infer_ms", "decode_ms", "e2e_ms", "presented_dur_ms", "content_dur_ms")
    QUEUE_MS_FIELD_NUMBER: _ClassVar[int]
    INFER_MS_FIELD_NUMBER: _ClassVar[int]
    DECODE_MS_FIELD_NUMBER: _ClassVar[int]
    E2E_MS_FIELD_NUMBER: _ClassVar[int]
    PRESENTED_DUR_MS_FIELD_NUMBER: _ClassVar[int]
    CONTENT_DUR_MS_FIELD_NUMBER: _ClassVar[int]
    queue_ms: int
    infer_ms: int
    decode_ms: int
    e2e_ms: int
    presented_dur_ms: int
    content_dur_ms: int
    def __init__(self, queue_ms: _Optional[int] = ..., infer_ms: _Optional[int] = ..., decode_ms: _Optional[int] = ..., e2e_ms: _Optional[int] = ..., presented_dur_ms: _Optional[int] = ..., content_dur_ms: _Optional[int] = ...) -> None: ...

class PartialResult(_message.Message):
    __slots__ = ("turn_id", "text", "l1_intent", "stable_ms", "seq")
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    L1_INTENT_FIELD_NUMBER: _ClassVar[int]
    STABLE_MS_FIELD_NUMBER: _ClassVar[int]
    SEQ_FIELD_NUMBER: _ClassVar[int]
    turn_id: str
    text: str
    l1_intent: L1Intent
    stable_ms: int
    seq: int
    def __init__(self, turn_id: _Optional[str] = ..., text: _Optional[str] = ..., l1_intent: _Optional[_Union[L1Intent, _Mapping]] = ..., stable_ms: _Optional[int] = ..., seq: _Optional[int] = ...) -> None: ...

class EarlyEndpoint(_message.Message):
    __slots__ = ("turn_id", "stable_ms")
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    STABLE_MS_FIELD_NUMBER: _ClassVar[int]
    turn_id: str
    stable_ms: int
    def __init__(self, turn_id: _Optional[str] = ..., stable_ms: _Optional[int] = ...) -> None: ...

class FinalResult(_message.Message):
    __slots__ = ("turn_id", "text", "confidence", "l1_intent", "blank_ratio", "reason", "timings", "model_version", "diagnostics")
    class DiagnosticsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: float
        def __init__(self, key: _Optional[str] = ..., value: _Optional[float] = ...) -> None: ...
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    TEXT_FIELD_NUMBER: _ClassVar[int]
    CONFIDENCE_FIELD_NUMBER: _ClassVar[int]
    L1_INTENT_FIELD_NUMBER: _ClassVar[int]
    BLANK_RATIO_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    TIMINGS_FIELD_NUMBER: _ClassVar[int]
    MODEL_VERSION_FIELD_NUMBER: _ClassVar[int]
    DIAGNOSTICS_FIELD_NUMBER: _ClassVar[int]
    turn_id: str
    text: str
    confidence: float
    l1_intent: L1Intent
    blank_ratio: float
    reason: ResultReason
    timings: Timings
    model_version: str
    diagnostics: _containers.ScalarMap[str, float]
    def __init__(self, turn_id: _Optional[str] = ..., text: _Optional[str] = ..., confidence: _Optional[float] = ..., l1_intent: _Optional[_Union[L1Intent, _Mapping]] = ..., blank_ratio: _Optional[float] = ..., reason: _Optional[_Union[ResultReason, str]] = ..., timings: _Optional[_Union[Timings, _Mapping]] = ..., model_version: _Optional[str] = ..., diagnostics: _Optional[_Mapping[str, float]] = ...) -> None: ...

class ErrorEvent(_message.Message):
    __slots__ = ("code", "message", "turn_id")
    class Code(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
        __slots__ = ()
        CODE_UNSPECIFIED: _ClassVar[ErrorEvent.Code]
        INTERNAL: _ClassVar[ErrorEvent.Code]
        DRAINING: _ClassVar[ErrorEvent.Code]
        RESOURCE_EXHAUSTED: _ClassVar[ErrorEvent.Code]
        INVALID_ARGUMENT: _ClassVar[ErrorEvent.Code]
        DEADLINE_EXCEEDED: _ClassVar[ErrorEvent.Code]
    CODE_UNSPECIFIED: ErrorEvent.Code
    INTERNAL: ErrorEvent.Code
    DRAINING: ErrorEvent.Code
    RESOURCE_EXHAUSTED: ErrorEvent.Code
    INVALID_ARGUMENT: ErrorEvent.Code
    DEADLINE_EXCEEDED: ErrorEvent.Code
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    TURN_ID_FIELD_NUMBER: _ClassVar[int]
    code: ErrorEvent.Code
    message: str
    turn_id: str
    def __init__(self, code: _Optional[_Union[ErrorEvent.Code, str]] = ..., message: _Optional[str] = ..., turn_id: _Optional[str] = ...) -> None: ...

class ModelInfo(_message.Message):
    __slots__ = ("engine_id", "model_version", "supported_buckets", "supported_batch_sizes", "supports_native_partials", "supports_phrase_boost", "adapter_format", "component_hashes", "graph_capture_note", "declared_thresholds", "supported_languages")
    class ComponentHashesEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: str
        def __init__(self, key: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...
    ENGINE_ID_FIELD_NUMBER: _ClassVar[int]
    MODEL_VERSION_FIELD_NUMBER: _ClassVar[int]
    SUPPORTED_BUCKETS_FIELD_NUMBER: _ClassVar[int]
    SUPPORTED_BATCH_SIZES_FIELD_NUMBER: _ClassVar[int]
    SUPPORTS_NATIVE_PARTIALS_FIELD_NUMBER: _ClassVar[int]
    SUPPORTS_PHRASE_BOOST_FIELD_NUMBER: _ClassVar[int]
    ADAPTER_FORMAT_FIELD_NUMBER: _ClassVar[int]
    COMPONENT_HASHES_FIELD_NUMBER: _ClassVar[int]
    GRAPH_CAPTURE_NOTE_FIELD_NUMBER: _ClassVar[int]
    DECLARED_THRESHOLDS_FIELD_NUMBER: _ClassVar[int]
    SUPPORTED_LANGUAGES_FIELD_NUMBER: _ClassVar[int]
    engine_id: str
    model_version: str
    supported_buckets: _containers.RepeatedScalarFieldContainer[float]
    supported_batch_sizes: _containers.RepeatedScalarFieldContainer[int]
    supports_native_partials: bool
    supports_phrase_boost: bool
    adapter_format: str
    component_hashes: _containers.ScalarMap[str, str]
    graph_capture_note: str
    declared_thresholds: _containers.RepeatedScalarFieldContainer[str]
    supported_languages: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, engine_id: _Optional[str] = ..., model_version: _Optional[str] = ..., supported_buckets: _Optional[_Iterable[float]] = ..., supported_batch_sizes: _Optional[_Iterable[int]] = ..., supports_native_partials: _Optional[bool] = ..., supports_phrase_boost: _Optional[bool] = ..., adapter_format: _Optional[str] = ..., component_hashes: _Optional[_Mapping[str, str]] = ..., graph_capture_note: _Optional[str] = ..., declared_thresholds: _Optional[_Iterable[str]] = ..., supported_languages: _Optional[_Iterable[str]] = ...) -> None: ...
