"""The worker side of the worker protocol (api/openapi.yaml, tag `worker`): Bearer cwk_ token, JSON under /api, the
operation table and types generated into protocol_gen."""

from __future__ import annotations

import json
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any, cast
from urllib.parse import quote

import httpx

from cadence_worker.protocol_gen import (
    OPERATIONS,
    MetricPoint,
    StepOutcome,
    Worker,
    WorkerClaim,
    WorkerClaimResult,
    WorkerLogLine,
    WorkerOutput,
    WorkerRegistration,
    WorkerReport,
    WorkerReportAck,
)


class ApiError(Exception):
    def __init__(self, status: int, problem: dict[str, Any]) -> None:
        self.status = status
        self.problem = problem
        super().__init__(
            f"control plane answered {status}: {problem.get('title', '')} {problem.get('detail', '')}".strip()
        )

    @property
    def permanent(self) -> bool:
        """A 4xx other than 408/429 will not get better by retrying."""
        return 400 <= self.status < 500 and self.status not in (408, 429)


class WorkerClient:
    def __init__(
        self,
        base_url: str,
        token: Callable[[], str],
        *,
        transport: httpx.BaseTransport | None = None,
        timeout: float = 45.0,
    ) -> None:
        self._token = token
        self._http = httpx.Client(base_url=base_url.rstrip("/") + "/api", transport=transport, timeout=timeout)

    @staticmethod
    def token_from_file(path: Path) -> Callable[[], str]:
        """Re-read on every call: a control-plane restart re-issues the token into the same file."""

        def read() -> str:
            return path.read_text(encoding="utf-8").strip()

        return read

    def close(self) -> None:
        self._http.close()

    def _call(
        self,
        op: str,
        body: Any = None,
        *,
        content: bytes | None = None,
        content_type: str = "application/json",
        **path: str,
    ) -> Any:
        method, template = OPERATIONS[op]
        url = template.format(**{k: quote(v, safe="") for k, v in path.items()})
        headers = {"Authorization": f"Bearer {self._token()}", "Accept": "application/json"}
        data = content
        if body is not None:
            data = json.dumps(body, separators=(",", ":"), allow_nan=False).encode()
        if data is not None:
            headers["Content-Type"] = content_type
        res = self._http.request(method, url, content=data, headers=headers)
        if res.status_code >= 400:
            try:
                problem = res.json()
            except ValueError:
                problem = {"detail": res.text[:500]}
            raise ApiError(res.status_code, problem if isinstance(problem, dict) else {"detail": str(problem)})
        if res.status_code == 204 or not res.content:
            return None
        return res.json()

    def register(self, reg: WorkerRegistration) -> Worker:
        return cast(Worker, self._call("workerRegistrations.new", reg))

    def claim(self, claim: WorkerClaim) -> WorkerClaimResult:
        return cast(WorkerClaimResult, self._call("workerLeases.claim", claim) or {})

    def report(self, lease_id: str, report: WorkerReport) -> WorkerReportAck:
        return cast(WorkerReportAck, self._call("workerLeases.report", report, id=lease_id))

    def logs(self, lease_id: str, lines: list[WorkerLogLine]) -> None:
        payload = "".join(json.dumps(line, separators=(",", ":"), allow_nan=False) + "\n" for line in lines).encode()
        self._call("workerLogs.new", content=payload, content_type="application/x-ndjson", id=lease_id)

    def metrics(self, lease_id: str, points: list[MetricPoint]) -> None:
        self._call("workerMetrics.new", {"points": points}, id=lease_id)

    def publish(self, lease_id: str, output: WorkerOutput) -> None:
        self._call("workerOutputs.new", output, id=lease_id)

    def release(self, lease_id: str, outcome: StepOutcome) -> None:
        self._call("workerLeases.release", outcome, id=lease_id)

    def upload(self, h: str, data: bytes) -> None:
        self._call("workerArtifacts.set", content=data, content_type="application/octet-stream", hash=h)


def with_retry[T](
    fn: Callable[[], T],
    *,
    attempts: int = 5,
    base: float = 0.5,
    cap: float = 30.0,
    sleep: Callable[[float], None] = time.sleep,
) -> T:
    """Retry transient failures (network, 5xx, 408, 429) with exponential backoff (each pause at most cap seconds);
    permanent 4xx raise at once."""
    for i in range(attempts):
        try:
            return fn()
        except ApiError as e:
            if e.permanent or i == attempts - 1:
                raise
        except httpx.TransportError:
            if i == attempts - 1:
                raise
        sleep(min(cap, base * (2**i)))
    raise AssertionError("unreachable")
