"""Worker protocol types generated from api/openapi.yaml (tag `worker`) by worker/scripts/gen_protocol.py.

Do not edit: change the contract and run `make gen`.
"""

from __future__ import annotations

from typing import Any, Literal, NotRequired, TypedDict

OPERATIONS: dict[str, tuple[str, str]] = {
    "workerArtifacts.set": ("PUT", "/worker-artifacts/{hash}"),
    "workerLeases.claim": ("POST", "/worker-leases:claim"),
    "workerLeases.release": ("POST", "/worker-leases/{id}:release"),
    "workerLeases.report": ("POST", "/worker-leases/{id}:report"),
    "workerLive.connect": ("GET", "/worker-live/{jobId}"),
    "workerLogs.new": ("POST", "/worker-leases/{id}/worker-logs"),
    "workerMetrics.new": ("POST", "/worker-leases/{id}/worker-metrics"),
    "workerOutputs.new": ("POST", "/worker-leases/{id}/worker-outputs"),
    "workerRegistrations.new": ("POST", "/worker-registrations"),
}


class ArtifactRef(TypedDict):
    hash: str
    type: str
    size: NotRequired[int]
    meta: NotRequired[dict[str, Any]]


class AuxiliaryPayload(TypedDict):
    """The payload of an auxiliary version (auxiliary/<name>): a model a step loads per job ({hfRepo, revision}) or a
    running service ({service}), never both. Cadence never starts a service. Adoption (projects.adopt) is an approval
    for everyone and refuses outputsCommercialUse false (R26).
    """

    roles: list[Literal["lid", "pseudolabel", "align"]]
    licence: str
    outputsCommercialUse: bool
    conditions: NotRequired[list[str]]
    languages: list[str]
    hfRepo: NotRequired[str]
    revision: NotRequired[str]
    service: NotRequired[AuxiliaryService]
    engine: NotRequired[str]
    sources: NotRequired[list[str]]
    checkedAt: NotRequired[str]


class AuxiliaryService(TypedDict):
    kind: str
    endpoint: str
    protocol: str
    tokenSecret: NotRequired[str]


class CardTelemetry(TypedDict):
    index: int
    name: NotRequired[str]
    memoryTotalMb: NotRequired[int]
    memoryUsedMb: NotRequired[int]
    utilization: NotRequired[float]
    temperatureC: NotRequired[float]
    powerW: NotRequired[float]


class LatencyProfile(TypedDict):
    name: str
    latencyMs: int
    chunkMs: NotRequired[int]
    leftContextMs: NotRequired[int]
    params: NotRequired[dict[str, Any]]
    label: NotRequired[str]


class Lease(TypedDict):
    id: str
    jobId: str
    spec: StepSpec
    inputs: NotRequired[dict[str, str]]
    card: LeaseCard
    env: NotRequired[dict[str, str]]
    traceparent: NotRequired[str]
    heartbeatSeconds: int
    mounts: NotRequired[list[LeaseMount]]


class LeaseCard(TypedDict):
    index: int
    memoryCapMb: int


class LeaseMount(TypedDict):
    """A mount as a worker resolves mount://<name>/<path> URIs (cadence_worker.mounts)"""

    name: str
    kind: Literal["local", "nfs", "smb", "s3", "hf"]
    root: str
    readOnly: bool
    endpoint: NotRequired[str]
    region: NotRequired[str]
    revision: NotRequired[str]
    credentialsEnv: NotRequired[str]


class LiveEnd(TypedDict):
    """Flush every target, send summary, close with 1000"""

    type: Literal["end"]


class LiveError(TypedDict):
    type: Literal["error"]
    problem: Problem
    fatal: bool


class LiveFileEnd(TypedDict):
    """file: every byte was sent; the worker decodes the file (ffmpeg) and streams it at the session's pace"""

    type: Literal["fileEnd"]


class LiveFinal(TypedDict):
    type: Literal["final"]
    target: Literal["A", "B", "C"]
    segment: int
    seq: int
    text: str
    words: list[LiveWord]
    endpoint: Literal["eou", "finalize", "end"]
    audioEnd: float
    space: bool


class LiveFinalize(TypedDict):
    """A segment boundary: pad the right context with silence, force end of utterance, emit the finals; the next audio
    opens a new decoder stream (fresh encoder cache)
    """

    type: Literal["finalize"]


class LiveInput(TypedDict):
    kind: Literal["microphone", "file", "span"]
    sampleRate: NotRequired[int]
    frameMs: NotRequired[float]
    settings: NotRequired[dict[str, Any]]
    raw: NotRequired[bool]
    fileName: NotRequired[str]
    fileBytes: NotRequired[int]


class LiveKeepalive(TypedDict):
    """Keeps the session open without audio; the worker answers pong (source worker), behind any audio already sent"""

    type: Literal["keepalive"]
    t: NotRequired[float]


class LivePartial(TypedDict):
    type: Literal["partial"]
    target: Literal["A", "B", "C"]
    segment: int
    seq: int
    text: str
    audioEnd: float


class LivePing(TypedDict):
    """Answered by the relay itself (pong, source relay): the control plane hop alone"""

    type: Literal["ping"]
    t: NotRequired[float]


class LivePong(TypedDict):
    type: Literal["pong"]
    source: Literal["relay", "worker"]
    t: NotRequired[float]


class LiveStart(TypedDict):
    """The first message: what the audio is. Telephony, pace and targets were fixed by transcriptions.new"""

    type: Literal["start"]
    input: LiveInput


class LiveStarted(TypedDict):
    """Every target is loaded and the decoder takes audio: the effective configuration per target"""

    type: Literal["started"]
    targets: list[LiveStartedTarget]
    captureRate: NotRequired[int]
    resampler: str
    telephony: NotRequired[LiveTelephony]
    pace: NotRequired[Literal["realtime", "fast"]]
    input: NotRequired[LiveInput]
    durationS: NotRequired[float]


class LiveStartedTarget(TypedDict):
    target: Literal["A", "B", "C"]
    profile: str
    chunkMs: int
    language: str
    loadS: float
    decoder: NotRequired[str]
    boost: NotRequired[LiveStartedTargetBoost]


class LiveStartedTargetBoost(TypedDict):
    terms: NotRequired[int]
    weight: NotRequired[float]


class LiveStats(TypedDict):
    type: Literal["stats"]
    source: Literal["worker", "relay"]
    rtf: NotRequired[float]
    audioS: NotRequired[float]
    queued: NotRequired[int]
    upP50Us: NotRequired[float]
    upP95Us: NotRequired[float]
    downP50Us: NotRequired[float]
    downP95Us: NotRequired[float]
    upN: NotRequired[int]
    downN: NotRequired[int]


class LiveSummary(TypedDict):
    """The session's totals, last; the socket then closes with 1000. Nothing of it is stored"""

    type: Literal["summary"]
    audioS: float
    rtf: float
    targets: dict[str, LiveTargetSummary]
    gpu: NotRequired[dict[str, Any]]
    load: NotRequired[dict[str, Any]]
    frameMsP50: NotRequired[float]
    frameMsP95: NotRequired[float]


class LiveTargetSummary(TypedDict):
    profile: NotRequired[str]
    steps: NotRequired[int]
    stepMsP50: NotRequired[float]
    stepMsP95: NotRequired[float]
    stepMsMax: NotRequired[float]
    finals: NotRequired[int]


class LiveTelephony(TypedDict):
    chain: str
    codec: Literal["ulaw", "alaw", "none"]
    sampleRate: int


class LiveWaiting(TypedDict):
    """From the relay until started: the job waits for a card (queued, with its place and why) or the worker loads the
    targets (loading)
    """

    type: Literal["waiting"]
    state: Literal["queued", "loading"]
    position: NotRequired[int]
    reason: NotRequired[str]
    reservationMb: NotRequired[int]
    waitedS: NotRequired[float]


class LiveWord(TypedDict):
    word: str
    start: NotRequired[float]
    end: NotRequired[float]
    confidence: NotRequired[float]


class MetricPoint(TypedDict):
    name: str
    step: NotRequired[int]
    epoch: NotRequired[float]
    value: float
    wallTime: str


class ModelFamilyDescriptor(TypedDict):
    name: str
    version: str
    title: NotRequired[str]
    framework: str
    architecture: str
    formats: NotRequired[list[str]]
    input: NotRequired[ModelFamilyDescriptorInput]
    features: NotRequired[dict[str, Any]]
    tokenizer: NotRequired[str]
    capabilities: NotRequired[ModelFamilyDescriptorCapabilities]
    latencyProfiles: list[LatencyProfile]
    roles: dict[str, str]
    interactive: NotRequired[ModelFamilyDescriptorInteractive]
    defaultsSection: NotRequired[str]
    help: NotRequired[str]
    skill: NotRequired[str]


class ModelFamilyDescriptorCapabilities(TypedDict):
    streaming: NotRequired[bool]
    wordTimestamps: NotRequired[bool]
    confidence: NotRequired[bool]
    boosting: NotRequired[str]
    languagePrompt: NotRequired[bool]
    trainModes: NotRequired[list[Literal["finetune", "adapter", "scratch"]]]


class ModelFamilyDescriptorInput(TypedDict):
    sampleRate: NotRequired[int]
    channels: NotRequired[int]


class ModelFamilyDescriptorInteractive(TypedDict):
    """The card memory a live transcription session of the family reserves (R49, phase 3 · stream T)"""

    memoryMb: int
    extraCheckpointMb: NotRequired[int]


class Problem(TypedDict):
    type: str
    title: str
    status: int
    detail: NotRequired[str]
    instance: NotRequired[str]
    currentRev: NotRequired[int]
    errors: NotRequired[list[ProblemFieldError]]


class ProblemFieldError(TypedDict):
    path: str
    message: str


class RuntimeDescriptor(TypedDict):
    name: str
    version: str
    image: NotRequired[str]
    digest: NotRequired[str]
    environment: NotRequired[dict[str, str]]
    plugin: NotRequired[str]


class StepError(TypedDict):
    type: Literal["oom", "step", "lost", "cancelled", "input"]
    message: str
    retryable: NotRequired[bool]


class StepKindDescriptor(TypedDict):
    version: str
    params: dict[str, Any]
    consumes: dict[str, str]
    optionalInputs: NotRequired[list[str]]
    produces: dict[str, str]
    optionalOutputs: NotRequired[list[str]]
    resources: StepResources
    role: NotRequired[str]
    neutral: NotRequired[bool]
    secrets: NotRequired[list[str]]
    help: str


class StepOutcome(TypedDict):
    state: Literal["done", "failed", "cancelled"]
    error: NotRequired[StepError]
    outputs: NotRequired[dict[str, ArtifactRef]]
    metrics: NotRequired[dict[str, float]]


class StepRegistryRef(TypedDict):
    """A registry version a step parameter names (x-cadence.registryRef), resolved for the project when the run was
    planned; in v1 only auxiliary versions resolve, so payload is an AuxiliaryPayload
    """

    versionId: str
    name: str
    version: str
    payload: AuxiliaryPayload


class StepResources(TypedDict):
    gpu: NotRequired[bool]
    gpus: NotRequired[int]
    memoryGb: NotRequired[float]
    diskGb: NotRequired[float]
    jobKind: NotRequired[Literal["training", "eval", "export", "data", "interactive"]]


class StepSpec(TypedDict):
    stepId: str
    pipelineRunId: str
    projectId: NotRequired[str]
    runId: NotRequired[str]
    kind: str
    kindVersion: str
    params: dict[str, Any]
    inputs: dict[str, ArtifactRef]
    outputs: dict[str, str]
    resources: StepResources
    priority: NotRequired[int]
    estimateSeconds: NotRequired[float]
    overrides: NotRequired[StepSpecOverrides]
    attempt: int
    auxiliaries: NotRequired[dict[str, StepRegistryRef]]


class StepSpecOverrides(TypedDict):
    batchScale: NotRequired[float]
    resumeFrom: NotRequired[str]


class Worker(TypedDict):
    id: str
    host: str
    hostId: NotRequired[str]
    instance: NotRequired[str]
    runtime: RuntimeDescriptor
    runtimeVersionId: NotRequired[str]
    stepKinds: NotRequired[list[str]]
    state: Literal["online", "offline"]
    lastSeenAt: NotRequired[str]


class WorkerClaim(TypedDict):
    workerId: str
    wait: NotRequired[int]
    cards: list[CardTelemetry]


class WorkerClaimResult(TypedDict):
    lease: NotRequired[Lease]


class WorkerLogLine(TypedDict):
    t: str
    level: NotRequired[Literal["debug", "info", "warn", "error"]]
    msg: str
    fields: NotRequired[dict[str, Any]]


class WorkerMetricBatch(TypedDict):
    points: list[MetricPoint]


class WorkerOutput(TypedDict):
    name: str
    artifact: ArtifactRef
    metrics: NotRequired[dict[str, float]]


class WorkerRegistration(TypedDict):
    host: str
    instance: NotRequired[str]
    runtime: RuntimeDescriptor
    stepKinds: dict[str, StepKindDescriptor]
    modelFamilies: NotRequired[list[ModelFamilyDescriptor]]


class WorkerReport(TypedDict):
    progress: NotRequired[WorkerReportProgress]
    cards: NotRequired[list[CardTelemetry]]


class WorkerReportAck(TypedDict):
    stop: bool
    reason: NotRequired[Literal["cancelled", "paused", "window-closed"]]


class WorkerReportProgress(TypedDict):
    fraction: NotRequired[float]
    message: NotRequired[str]
