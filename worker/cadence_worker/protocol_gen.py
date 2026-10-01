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


class LeaseCard(TypedDict):
    index: int
    memoryCapMb: int


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
    produces: dict[str, str]
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


class StepResources(TypedDict):
    gpu: NotRequired[bool]
    gpus: NotRequired[int]
    memoryGb: NotRequired[float]
    diskGb: NotRequired[float]
    jobKind: NotRequired[Literal["training", "eval", "export", "data"]]


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
