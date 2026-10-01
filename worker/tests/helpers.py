"""Shared helpers for harness tests: leases, kinds and a runner wired to a temporary store."""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from cadence_worker.cas import Store
from cadence_worker.executor import LeaseRunner, MemorySink
from cadence_worker.protocol_gen import ArtifactRef, Lease, StepSpec
from cadence_worker.registry import KindEntry
from cadence_worker.steps.echo import EchoStep

TESTS = Path(__file__).resolve().parent


def kinds() -> dict[str, KindEntry]:
    import fake_steps as fs

    out = {"echo": KindEntry("echo", EchoStep, "cadence_worker.steps.echo:EchoStep")}
    for name in (
        "SlowTrain",
        "Stubborn",
        "CardOom",
        "Forgetful",
        "SkipsOptional",
        "EnvProbe",
        "Publisher",
        "NonFinite",
        "Leaky",
    ):
        out[name] = KindEntry(name, getattr(fs, name), f"fake_steps:{name}")
    return out


def lease(
    kind: str,
    *,
    params: Mapping[str, Any] | None = None,
    inputs: Mapping[str, ArtifactRef] | None = None,
    env: Mapping[str, str] | None = None,
    lease_id: str = "lse_test",
    heartbeat: int = 10,
    card_index: int = 0,
    cap_mb: int = 24576,
    overrides: Mapping[str, Any] | None = None,
) -> Lease:
    cls = kinds()[kind].cls
    spec: StepSpec = {
        "stepId": "pls_1",
        "pipelineRunId": "plr_1",
        "kind": kind,
        "kindVersion": cls.version,
        "params": dict(params or {}),
        "inputs": dict(inputs or {}),
        "outputs": dict(cls.produces),
        "resources": dict(cls.resources),  # type: ignore[typeddict-item]
        "attempt": 1,
    }
    if overrides:
        spec["overrides"] = dict(overrides)  # type: ignore[typeddict-item]
    out: Lease = {
        "id": lease_id,
        "jobId": "job_1",
        "spec": spec,
        "inputs": {k: "cas://" + v["hash"] for k, v in (inputs or {}).items()},
        "card": {"index": card_index, "memoryCapMb": cap_mb},
        "heartbeatSeconds": heartbeat,
        "traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
    }
    if env:
        out["env"] = dict(env)
    return out


def runner(
    tmp: Path, lease_: Lease, sink: MemorySink | None = None, stop_grace: float = 10.0
) -> tuple[LeaseRunner, MemorySink, Store]:
    store = Store(tmp / "cas")
    sink = sink or MemorySink()
    r = LeaseRunner(
        lease_,
        kinds=kinds(),
        store=store,
        scratch=tmp / "scratch",
        sink=sink,
        stop_grace=stop_grace,
        extra_env={"PYTHONPATH": str(TESTS)},
    )
    return r, sink, store
