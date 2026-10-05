"""docker-compose.yml against the harness: a stopping worker container must outlast its steps' stop grace, or Docker
kills a training step while it writes its training state."""

from __future__ import annotations

import re
from pathlib import Path
from typing import Any

import pytest
import yaml

from cadence_worker.config import Config
from cadence_worker.serve import RELEASE_MARGIN_SECONDS

COMPOSE = Path(__file__).resolve().parents[2] / "docker-compose.yml"
DURATION = re.compile(r"(\d+(?:\.\d+)?)(h|ms|m|s|us)")
UNIT_S = {"h": 3600.0, "m": 60.0, "s": 1.0, "ms": 1e-3, "us": 1e-6}


def seconds(duration: str) -> float:
    """A compose duration (``90s``, ``1m30s``) in seconds."""
    parts = DURATION.findall(duration)
    if not parts or "".join(n + u for n, u in parts) != duration:
        raise ValueError(f"not a compose duration: {duration!r}")
    return sum(float(n) * UNIT_S[u] for n, u in parts)


def test_seconds() -> None:
    assert seconds("90s") == 90
    assert seconds("1m30s") == 90
    with pytest.raises(ValueError, match="duration"):
        seconds("90")


def worker_services() -> dict[str, dict[str, Any]]:
    if not COMPOSE.is_file():
        pytest.skip("docker-compose.yml is not beside the worker (a copy of worker/ alone)")
    doc = yaml.safe_load(COMPOSE.read_text(encoding="utf-8"))
    services: dict[str, dict[str, Any]] = doc["services"]
    return {
        name: s
        for name, s in services.items()
        if str((s.get("build") or {}).get("dockerfile", "")).startswith("worker/")
    }


def test_every_worker_container_outlasts_the_step_stop_grace() -> None:
    workers = worker_services()
    assert {"worker", "worker-toy"} <= workers.keys()
    for name, s in workers.items():
        env = {k: str(v) for k, v in (s.get("environment") or {}).items()}
        step_grace = Config.from_env(env).stop_grace
        # The step's grace, then the harness hashes the state and releases the lease; 10 s on top for slack.
        need = step_grace + RELEASE_MARGIN_SECONDS + 10
        assert "stop_grace_period" in s, f"{name} has no stop_grace_period (Docker's default is 10 s)"
        assert seconds(str(s["stop_grace_period"])) >= need, (
            f"{name}: stop_grace_period {s['stop_grace_period']} is shorter than the step stop grace "
            f"({step_grace:g} s) plus the release margin ({RELEASE_MARGIN_SECONDS:g} s) and 10 s"
        )


def test_the_staging_server_follows_defaults() -> None:
    """The compose profile serving runs the image and CUDA pool defaults.yaml names, on an internal network only."""
    if not COMPOSE.is_file():
        pytest.skip("docker-compose.yml is not beside the worker (a copy of worker/ alone)")
    from cadence_worker.defaults import lookup

    doc = yaml.safe_load(COMPOSE.read_text(encoding="utf-8"))
    triton = doc["services"]["triton"]
    assert triton["profiles"] == ["serving"]
    assert f":-{lookup('serving.image').value}}}" in triton["image"]
    command = " ".join(triton["command"])
    assert f"CADENCE_TRITON_CUDA_POOL_MB:-{lookup('deploy.triton_cuda_pool_mb').value}" in command
    assert "--model-control-mode=explicit" in command
    assert "growable" not in command
    assert "ports" not in triton
    assert triton["networks"] == ["serving"]
    assert doc["networks"]["serving"]["internal"] is True
    assert "serving:/models:ro" in triton["volumes"]
    worker = doc["services"]["worker"]
    assert "serving:/var/lib/cadence/serving" in worker["volumes"]
    assert "serving" in worker["networks"]
    assert "serving" in doc["services"]["control-plane"]["networks"]
