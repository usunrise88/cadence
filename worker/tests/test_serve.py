"""The serve loop against a fake control plane that implements the worker endpoints of api/openapi.yaml."""

from __future__ import annotations

import json
import threading
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx
import pytest
from helpers import TESTS, kinds, lease

from cadence_worker.cas import Store
from cadence_worker.client import ApiError, WorkerClient, with_retry
from cadence_worker.config import Config
from cadence_worker.protocol_gen import ArtifactRef, Lease
from cadence_worker.registry import load_families
from cadence_worker.serve import Batcher, WorkerService
from cadence_worker.telemetry import Telemetry, parse_smi

RUNTIME = {"name": "toy", "version": "dev", "plugin": "0.2.0"}


class FakeControlPlane:
    def __init__(self) -> None:
        self.leases: list[Lease] = []
        self.requests: list[tuple[str, str, dict[str, str], bytes]] = []
        self.registrations: list[dict[str, Any]] = []
        self.reports: list[dict[str, Any]] = []
        self.logs: list[dict[str, Any]] = []
        self.metrics: list[dict[str, Any]] = []
        self.outputs: list[dict[str, Any]] = []
        self.releases: dict[str, dict[str, Any]] = {}
        self.ack: Callable[[str], httpx.Response] = lambda _id: httpx.Response(200, json={"stop": False})
        self.claim_status = 200
        self.released = threading.Event()
        self.lock = threading.Lock()

    def handler(self, req: httpx.Request) -> httpx.Response:
        path = req.url.path
        body = req.read()
        with self.lock:
            self.requests.append((req.method, path, dict(req.headers), body))
        if req.headers.get("authorization") != "Bearer cwk_test":
            return httpx.Response(401, json={"type": "unauthenticated", "title": "Unauthenticated", "status": 401})
        if path == "/api/worker-registrations":
            self.registrations.append(json.loads(body))
            return httpx.Response(200, json={"id": "wrk_1", "host": "staging", "runtime": RUNTIME, "state": "online"})
        if path == "/api/worker-leases:claim":
            if self.claim_status != 200:
                status, self.claim_status = self.claim_status, 200
                return httpx.Response(status, json={"type": "not-found", "title": "Not found", "status": status})
            with self.lock:
                le = self.leases.pop(0) if self.leases else None
            if le is None:
                time.sleep(0.02)
                return httpx.Response(200, json={})
            return httpx.Response(200, json={"lease": le})
        lease_id = path.split("/")[3].split(":")[0]
        if path.endswith(":report"):
            self.reports.append(json.loads(body))
            return self.ack(lease_id)
        if path.endswith("/worker-logs"):
            assert req.headers["content-type"] == "application/x-ndjson"
            self.logs += [json.loads(line) for line in body.decode().splitlines()]
            return httpx.Response(204)
        if path.endswith("/worker-metrics"):
            self.metrics += json.loads(body)["points"]
            return httpx.Response(204)
        if path.endswith("/worker-outputs"):
            self.outputs.append(json.loads(body))
            return httpx.Response(204)
        if path.endswith(":release"):
            self.releases[lease_id] = json.loads(body)
            self.released.set()
            return httpx.Response(204)
        return httpx.Response(404, json={"type": "not-found", "title": "Not found", "status": 404})


@pytest.fixture
def cp() -> FakeControlPlane:
    return FakeControlPlane()


def service(tmp: Path, cp: FakeControlPlane, families: bool = False) -> WorkerService:
    token = tmp / "token"
    token.write_text("cwk_test\n")
    cfg = Config(
        url="http://cp",
        token_file=token,
        cas_dir=tmp / "cas",
        host="staging",
        scratch=tmp / "scratch",
        claim_wait=0,
        stop_grace=10,
        instance="test:1",
    )
    client = WorkerClient(cfg.url, WorkerClient.token_from_file(token), transport=httpx.MockTransport(cp.handler))
    return WorkerService(
        cfg,
        client,
        runtime=RUNTIME,  # type: ignore[arg-type]
        kinds=kinds(),
        families=load_families("toy") if families else [],
        telemetry=Telemetry(enabled=False),
        store=Store(cfg.cas_dir),
        sleep=lambda s: time.sleep(min(s, 0.01)),
    )


def run_until_released(svc: WorkerService, cp: FakeControlPlane, timeout: float = 60) -> None:
    t = threading.Thread(target=svc.loop, daemon=True)
    t.start()
    assert cp.released.wait(timeout), "no release"
    svc.shutdown()
    t.join(timeout=10)


def test_registration_publishes_runtime_kinds_and_families(tmp_path: Path, cp: FakeControlPlane) -> None:
    svc = service(tmp_path, cp, families=True)
    svc.register()
    reg = cp.registrations[0]
    assert reg["host"] == "staging"
    assert reg["instance"] == "test:1"
    assert reg["runtime"] == RUNTIME
    assert reg["stepKinds"]["echo"]["help"] == "steps.echo"
    assert reg["stepKinds"]["SlowTrain"]["role"] == "train"
    [family] = reg["modelFamilies"]
    assert family["name"] == "toy-ctc"
    assert {p["name"] for p in family["latencyProfiles"]} == {"offline", "320ms"}
    assert "fixtures" not in family
    assert "conformance" not in family
    assert svc.worker_id == "wrk_1"


def test_a_lease_runs_and_is_released_with_its_outputs(tmp_path: Path, cp: FakeControlPlane) -> None:
    svc = service(tmp_path, cp)
    ref: ArtifactRef = {"hash": svc.store.put_bytes(b"shalom"), "type": "text"}
    cp.leases.append(lease("echo", params={"prefix": "> "}, inputs={"text": ref}, lease_id="lse_a"))
    run_until_released(svc, cp)
    rel = cp.releases["lse_a"]
    assert rel["state"] == "done"
    out = rel["outputs"]["text"]
    assert svc.store.path(out["hash"]).read_text() == "> shalom"
    assert any(line["msg"] == "echoed" for line in cp.logs)
    claim = next(json.loads(b) for m, p, _, b in cp.requests if p.endswith(":claim"))
    assert claim == {"workerId": "wrk_1", "wait": 0, "cards": []}


def test_published_outputs_reach_the_control_plane_before_the_release(
    tmp_path: Path, cp: FakeControlPlane, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("PYTHONPATH", str(TESTS))
    svc = service(tmp_path, cp)
    cp.leases.append(lease("Publisher", lease_id="lse_p"))
    run_until_released(svc, cp)
    assert [o["artifact"]["meta"]["step"] for o in cp.outputs] == [10, 20]
    assert all(o["name"] == "checkpoint" and o["artifact"]["type"] == "checkpoint" for o in cp.outputs)
    order = [p for _, p, _, _ in cp.requests if p.endswith(("/worker-outputs", ":release"))]
    assert len(order) == 3
    assert order[-1].endswith(":release")
    assert cp.releases["lse_p"]["state"] == "done"


def _strict(raw: bytes) -> Any:
    def refuse(name: str) -> Any:
        raise ValueError(f"{name} is not JSON")

    return [json.loads(line, parse_constant=refuse) for line in raw.decode().splitlines() if line]


def test_non_finite_metrics_do_not_cost_the_release(
    tmp_path: Path, cp: FakeControlPlane, monkeypatch: pytest.MonkeyPatch
) -> None:
    """val_wer = 0/0 must not turn a request into invalid JSON (a 400 without retry loses the release)."""
    monkeypatch.setenv("PYTHONPATH", str(TESTS))
    svc = service(tmp_path, cp)
    cp.leases.append(lease("NonFinite", lease_id="lse_n"))
    run_until_released(svc, cp)
    for _, _, headers, body in cp.requests:
        if "json" in headers.get("content-type", ""):
            _strict(body)
    rel = cp.releases["lse_n"]
    assert rel["state"] == "done"
    assert rel["metrics"] == {"loss": 0.5}
    assert rel["outputs"]["checkpoint"]["meta"]["valWer"] is None
    assert [o["artifact"]["meta"]["valWer"] for o in cp.outputs] == [None]
    assert [p["name"] for p in cp.metrics] == ["loss"]


def test_the_client_refuses_to_send_non_finite_numbers(cp: FakeControlPlane) -> None:
    client = WorkerClient("http://cp", lambda: "cwk_test", transport=httpx.MockTransport(cp.handler))
    with pytest.raises(ValueError, match="not JSON compliant"):
        client.metrics("lse_x", [{"name": "val_wer", "value": float("nan"), "wallTime": "2026-10-01T00:00:00Z"}])
    with pytest.raises(ValueError, match="not JSON compliant"):
        client.logs(
            "lse_x", [{"t": "2026-10-01T00:00:00Z", "level": "info", "msg": "x", "fields": {"v": float("inf")}}]
        )
    assert cp.requests == []


def test_a_stop_ack_cancels_with_the_training_state(
    tmp_path: Path, cp: FakeControlPlane, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("PYTHONPATH", str(TESTS))
    svc = service(tmp_path, cp)
    cp.ack = lambda _id: httpx.Response(200, json={"stop": True, "reason": "paused"})
    cp.leases.append(lease("SlowTrain", params={"seconds": 30}, lease_id="lse_s", heartbeat=1))
    run_until_released(svc, cp)
    rel = cp.releases["lse_s"]
    assert rel["state"] == "cancelled"
    assert rel["error"] == {"type": "cancelled", "message": "paused"}
    assert set(rel["outputs"]) == {"state"}
    assert rel["outputs"]["state"]["type"] == "training-state"
    assert cp.reports[0]["cards"] == []
    assert [p["name"] for p in cp.metrics] == ["loss"]
    assert "wallTime" in cp.metrics[0]


def test_a_lost_lease_is_stopped_and_not_released(
    tmp_path: Path, cp: FakeControlPlane, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("PYTHONPATH", str(TESTS))
    svc = service(tmp_path, cp)
    gone = threading.Event()

    def ack(_id: str) -> httpx.Response:
        gone.set()
        return httpx.Response(404, json={"type": "not-found", "title": "Not found", "status": 404})

    cp.ack = ack
    le = lease("SlowTrain", params={"seconds": 30}, lease_id="lse_l", heartbeat=1)
    svc.register()
    t = svc.start_lease(le)
    t.join(timeout=30)
    assert gone.is_set()
    assert not t.is_alive()
    assert cp.releases == {}


def test_an_unknown_worker_registers_again(tmp_path: Path, cp: FakeControlPlane) -> None:
    svc = service(tmp_path, cp)
    cp.claim_status = 404
    ref: ArtifactRef = {"hash": svc.store.put_bytes(b"x"), "type": "text"}
    cp.leases.append(lease("echo", inputs={"text": ref}, lease_id="lse_r"))
    run_until_released(svc, cp)
    assert len(cp.registrations) == 2


def test_a_lease_claimed_while_stopping_is_handed_back(tmp_path: Path, cp: FakeControlPlane) -> None:
    svc = service(tmp_path, cp)
    svc.hand_back(lease("echo", lease_id="lse_h"))
    rel = cp.releases["lse_h"]
    assert rel["state"] == "failed"
    assert rel["error"]["type"] == "lost"
    assert rel["error"]["retryable"] is True


def test_batcher_splits_by_count_and_bytes_and_flushes_on_interval() -> None:
    sent: list[list[str]] = []
    b: Batcher[str] = Batcher(sent.append, max_items=10, max_bytes=10, size_of=len, interval=3600)
    for s in ["aaaa", "bbbb", "cc", "dddd", "e"]:
        b.add(s)
    assert sent == [["aaaa", "bbbb", "cc"]]  # "dddd" would take the batch past 10 bytes
    b.close()
    assert sent == [["aaaa", "bbbb", "cc"], ["dddd", "e"]]

    counted: list[list[int]] = []
    c: Batcher[int] = Batcher(counted.append, max_items=2, interval=3600)
    for i in range(5):
        c.add(i)
    assert counted == [[0, 1], [2, 3]]
    c.close()
    assert counted[-1] == [4]

    timed: list[list[int]] = []
    t: Batcher[int] = Batcher(timed.append, max_items=100, interval=0.05)
    t.add(1)
    deadline = time.monotonic() + 5
    while not timed and time.monotonic() < deadline:
        time.sleep(0.01)
    assert timed == [[1]]
    t.close()


def test_batcher_counts_what_it_could_not_send() -> None:
    def fail(batch: list[int]) -> None:
        raise RuntimeError("down")

    b: Batcher[int] = Batcher(fail, max_items=2, interval=3600)
    b.add(1)
    b.add(2)
    b.close()
    assert b.dropped == 2


def test_retry_backs_off_on_transient_errors_only() -> None:
    calls: list[int] = []

    def flaky() -> str:
        calls.append(1)
        if len(calls) < 3:
            raise ApiError(503, {"title": "unavailable"})
        return "ok"

    assert with_retry(flaky, sleep=lambda _: None) == "ok"
    assert len(calls) == 3

    def bad() -> str:
        calls.append(1)
        raise ApiError(422, {"title": "validation"})

    calls.clear()
    with pytest.raises(ApiError):
        with_retry(bad, sleep=lambda _: None)
    assert len(calls) == 1


def test_the_token_file_is_reread(tmp_path: Path, cp: FakeControlPlane) -> None:
    token = tmp_path / "token"
    token.write_text("cwk_old")
    client = WorkerClient("http://cp", WorkerClient.token_from_file(token), transport=httpx.MockTransport(cp.handler))
    with pytest.raises(ApiError) as e:
        client.claim({"workerId": "wrk_1", "cards": []})
    assert e.value.status == 401
    token.write_text("cwk_test")
    assert client.claim({"workerId": "wrk_1", "cards": []}) == {}


def test_nvidia_smi_output_is_parsed() -> None:
    out = "0, NVIDIA RTX PRO 5000 Blackwell, 48935, 23821, 37, 51, 88.20\n1, x, [N/A], 1, 0, 30, [N/A]\n"
    cards = parse_smi(out)
    assert cards[0] == {
        "index": 0,
        "name": "NVIDIA RTX PRO 5000 Blackwell",
        "memoryTotalMb": 48935,
        "memoryUsedMb": 23821,
        "utilization": 0.37,
        "temperatureC": 51.0,
        "powerW": 88.2,
    }
    assert "memoryTotalMb" not in cards[1]
    assert "powerW" not in cards[1]
    assert Telemetry(enabled=False).cards() == []


def test_with_retry_caps_each_pause() -> None:
    pauses: list[float] = []

    def down() -> str:
        raise httpx.ConnectError("control plane down")

    with pytest.raises(httpx.ConnectError):
        with_retry(down, attempts=10, cap=30.0, sleep=pauses.append)
    assert len(pauses) == 9
    assert max(pauses) == 30.0
    assert pauses[:3] == [0.5, 1.0, 2.0]
