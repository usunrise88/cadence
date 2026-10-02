"""``python -m cadence_worker serve``: register, then claim leases and run each in its own step process, with a
heartbeat per lease, logs and metrics forwarded in batches, and a release with the outputs hashed into the store.
"""

from __future__ import annotations

import json
import logging
import signal
import threading
import time
from collections.abc import Callable, Mapping
from typing import Any

from cadence_worker.cas import Store
from cadence_worker.client import ApiError, WorkerClient, with_retry
from cadence_worker.config import Config
from cadence_worker.executor import LeaseRunner
from cadence_worker.protocol_gen import (
    Lease,
    MetricPoint,
    RuntimeDescriptor,
    StepOutcome,
    WorkerLogLine,
    WorkerOutput,
    WorkerRegistration,
    WorkerReport,
)
from cadence_worker.registry import Family, KindEntry, step_kinds
from cadence_worker.telemetry import Telemetry

log = logging.getLogger("cadence_worker")

LOG_BATCH_LINES = 500
LOG_BATCH_BYTES = 512 << 10  # the endpoint takes at most 1 MiB per request
METRIC_BATCH_POINTS = 1000  # the contract allows 5000
FLUSH_SECONDS = 1.0
# How long shutdown waits for a stopping lease beyond the step stop grace: hashing its training state and the release.
# A worker container's stop_grace_period in docker-compose.yml must cover both (tests/test_compose.py).
RELEASE_MARGIN_SECONDS = 15.0


class Batcher[T]:
    """Collects items and sends them in batches: when a batch is full, every ``interval`` seconds, and on close."""

    def __init__(
        self,
        send: Callable[[list[T]], None],
        *,
        max_items: int,
        max_bytes: int = 0,
        size_of: Callable[[T], int] = lambda _: 0,
        interval: float = FLUSH_SECONDS,
        name: str = "batcher",
    ) -> None:
        self._send = send
        self._max_items = max_items
        self._max_bytes = max_bytes
        self._size_of = size_of
        self._interval = interval
        self._items: list[T] = []
        self._bytes = 0
        self._lock = threading.Lock()
        self._send_lock = threading.Lock()
        self._closed = threading.Event()
        self.sent_batches = 0
        self.dropped = 0
        self._thread = threading.Thread(target=self._loop, daemon=True, name=name)
        self._thread.start()

    def add(self, item: T) -> None:
        size = self._size_of(item)
        batches: list[list[T]] = []
        with self._lock:
            if self._items and self._max_bytes and self._bytes + size > self._max_bytes:
                batches.append(self._items)
                self._items, self._bytes = [], 0
            self._items.append(item)
            self._bytes += size
            if len(self._items) >= self._max_items:
                batches.append(self._items)
                self._items, self._bytes = [], 0
        for b in batches:
            self._deliver(b)

    def flush(self) -> None:
        with self._lock:
            batch, self._items, self._bytes = self._items, [], 0
        if batch:
            self._deliver(batch)

    def _deliver(self, batch: list[T]) -> None:
        with self._send_lock:
            try:
                self._send(batch)
                self.sent_batches += 1
            except Exception as e:
                self.dropped += len(batch)
                log.warning("dropped a batch of %d: %s", len(batch), e)

    def _loop(self) -> None:
        while not self._closed.wait(self._interval):
            self.flush()

    def close(self) -> None:
        self._closed.set()
        self._thread.join(timeout=5)
        self.flush()


def _line_size(line: WorkerLogLine) -> int:
    return len(json.dumps(line)) + 1


class HttpSink:
    def __init__(self, client: WorkerClient, lease_id: str) -> None:
        self.client = client
        self.lease_id = lease_id
        self.logs: Batcher[WorkerLogLine] = Batcher(
            lambda b: with_retry(lambda: client.logs(lease_id, b)),
            max_items=LOG_BATCH_LINES,
            max_bytes=LOG_BATCH_BYTES,
            size_of=_line_size,
            name=f"logs-{lease_id}",
        )
        self.metrics: Batcher[MetricPoint] = Batcher(
            lambda b: with_retry(lambda: client.metrics(lease_id, b)),
            max_items=METRIC_BATCH_POINTS,
            name=f"metrics-{lease_id}",
        )
        self.last_progress: tuple[float, str] | None = None

    def log(self, line: WorkerLogLine) -> None:
        self.logs.add(line)

    def metric(self, point: MetricPoint) -> None:
        self.metrics.add(point)

    def progress(self, fraction: float, message: str) -> None:
        self.last_progress = (fraction, message)

    def publish(self, output: WorkerOutput) -> None:
        # Logs and metrics so far go first, so the job's log reads in order around the publication.
        self.logs.flush()
        self.metrics.flush()
        with_retry(lambda: self.client.publish(self.lease_id, output))

    def close(self) -> None:
        self.logs.close()
        self.metrics.close()


class LeaseSession:
    """One lease from claim to release."""

    def __init__(
        self,
        lease: Lease,
        *,
        client: WorkerClient,
        kinds: Mapping[str, KindEntry],
        store: Store,
        cfg: Config,
        telemetry: Telemetry,
    ) -> None:
        self.lease = lease
        self.client = client
        self.telemetry = telemetry
        self.sink = HttpSink(client, lease["id"])
        self.runner = LeaseRunner(
            lease, kinds=kinds, store=store, scratch=cfg.scratch, sink=self.sink, stop_grace=cfg.stop_grace
        )
        self.lost = False
        self._done = threading.Event()

    def heartbeat_once(self) -> None:
        report: WorkerReport = {"cards": self.telemetry.cards()}
        if self.sink.last_progress is not None:
            fraction, message = self.sink.last_progress
            report["progress"] = {"fraction": fraction, "message": message}
        try:
            ack = self.client.report(self.lease["id"], report)
        except ApiError as e:
            if e.permanent:
                # The lease is gone (reaped, or the job was deleted): stop and do not release.
                self.lost = True
                self.runner.stop("the control plane no longer knows this lease")
            return
        except Exception as e:
            log.warning("heartbeat for %s failed: %s", self.lease["id"], e)
            return
        if ack.get("stop"):
            self.runner.stop(str(ack.get("reason") or "cancelled"))

    def _heartbeats(self) -> None:
        every = max(1, int(self.lease.get("heartbeatSeconds") or 10))
        while not self._done.wait(every):
            self.heartbeat_once()

    def run(self) -> StepOutcome:
        hb = threading.Thread(target=self._heartbeats, daemon=True, name=f"heartbeat-{self.lease['id']}")
        hb.start()
        try:
            outcome = self.runner.run()
        except Exception as e:
            outcome = {"state": "failed", "error": {"type": "step", "message": f"harness: {e}"[:4000]}}
        finally:
            self._done.set()
            hb.join(timeout=5)
            self.sink.close()
        if not self.lost:
            try:
                # A finished step's outputs are worth waiting for: ride out a control-plane restart or outage of up
                # to ~13 minutes (the control plane does not reap leases right after its own start).
                with_retry(lambda: self.client.release(self.lease["id"], outcome), attempts=30)
            except Exception as e:
                log.error("release of %s failed: %s", self.lease["id"], e)
        return outcome


class WorkerService:
    def __init__(
        self,
        cfg: Config,
        client: WorkerClient,
        *,
        runtime: RuntimeDescriptor,
        kinds: Mapping[str, KindEntry],
        families: list[Family],
        telemetry: Telemetry,
        store: Store,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self.cfg = cfg
        self.client = client
        self.runtime = runtime
        self.kinds = kinds
        self.families = families
        self.telemetry = telemetry
        self.store = store
        self.sleep = sleep
        self.worker_id = ""
        self.stopping = threading.Event()
        self.sessions: dict[str, LeaseSession] = {}
        self._lock = threading.Lock()
        self._threads: list[threading.Thread] = []

    def registration(self) -> WorkerRegistration:
        reg: WorkerRegistration = {
            "host": self.cfg.host,
            "instance": self.cfg.instance,
            "runtime": self.runtime,
            "stepKinds": step_kinds(self.kinds),
        }
        if self.families:
            reg["modelFamilies"] = [f.descriptor for f in self.families]
        return reg

    def register(self) -> None:
        delay = 1.0
        while not self.stopping.is_set():
            try:
                worker = self.client.register(self.registration())
                self.worker_id = worker["id"]
                log.info("registered as %s (%d step kinds)", self.worker_id, len(self.kinds))
                return
            except Exception as e:
                log.warning("registration failed, retrying in %.0fs: %s", delay, e)
                self.sleep(delay)
                delay = min(30.0, delay * 2)

    def claim_once(self) -> Lease | None:
        res = self.client.claim(
            {"workerId": self.worker_id, "wait": self.cfg.claim_wait, "cards": self.telemetry.cards()}
        )
        return res.get("lease")

    def start_lease(self, lease: Lease) -> threading.Thread:
        session = LeaseSession(
            lease, client=self.client, kinds=self.kinds, store=self.store, cfg=self.cfg, telemetry=self.telemetry
        )
        with self._lock:
            self.sessions[lease["id"]] = session

        def run() -> None:
            try:
                outcome = session.run()
                log.info("lease %s %s", lease["id"], outcome["state"])
            finally:
                with self._lock:
                    self.sessions.pop(lease["id"], None)

        t = threading.Thread(target=run, daemon=True, name=f"lease-{lease['id']}")
        t.start()
        self._threads.append(t)
        return t

    def active(self) -> int:
        with self._lock:
            return len(self.sessions)

    def loop(self) -> None:
        self.register()
        delay = 1.0
        while not self.stopping.is_set():
            if self.active() >= self.cfg.max_leases:
                self.sleep(0.5)
                continue
            try:
                lease = self.claim_once()
                delay = 1.0
            except ApiError as e:
                if e.status == 404:
                    self.register()  # the control plane no longer knows this worker
                    continue
                log.warning("claim failed, retrying in %.0fs: %s", delay, e)
                self.sleep(delay)
                delay = min(30.0, delay * 2)
                continue
            except Exception as e:
                log.warning("claim failed, retrying in %.0fs: %s", delay, e)
                self.sleep(delay)
                delay = min(30.0, delay * 2)
                continue
            if lease is None:
                continue
            if self.stopping.is_set():
                self.hand_back(lease)
            else:
                self.start_lease(lease)

    def hand_back(self, lease: Lease) -> None:
        """A lease claimed while stopping: give it back at once instead of letting it be reaped."""
        outcome: StepOutcome = {
            "state": "failed",
            "error": {"type": "lost", "message": "the worker stopped before starting the step", "retryable": True},
        }
        try:
            with_retry(lambda: self.client.release(lease["id"], outcome), attempts=3)
        except Exception as e:
            log.warning("could not hand back %s: %s", lease["id"], e)

    def shutdown(self, reason: str = "the worker is stopping") -> None:
        """Stop claiming and ask every running step to stop (a training step checkpoints); wait for their releases."""
        self.stopping.set()
        with self._lock:
            sessions = list(self.sessions.values())
        for s in sessions:
            s.runner.stop(reason)
        for t in self._threads:
            t.join(timeout=self.cfg.stop_grace + RELEASE_MARGIN_SECONDS)


def serve(cfg: Config | None = None) -> int:
    from cadence_worker.registry import load_families, load_kinds, runtime_descriptor

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    cfg = cfg or Config.from_env()
    runtime = runtime_descriptor()
    kinds = load_kinds(runtime["name"])
    families = load_families(runtime["name"])
    cfg.scratch.mkdir(parents=True, exist_ok=True)
    client = WorkerClient(cfg.url, WorkerClient.token_from_file(cfg.token_file))
    service = WorkerService(
        cfg,
        client,
        runtime=runtime,
        kinds=kinds,
        families=families,
        telemetry=Telemetry(enabled=cfg.gpu != "off"),
        store=Store(cfg.cas_dir),
    )

    def on_term(signum: int, frame: Any) -> None:
        threading.Thread(target=service.shutdown, daemon=True, name="shutdown").start()

    signal.signal(signal.SIGTERM, on_term)
    signal.signal(signal.SIGINT, on_term)
    loop = threading.Thread(target=service.loop, daemon=True, name="claim-loop")
    loop.start()
    while loop.is_alive() and not service.stopping.is_set():
        loop.join(timeout=1)
    service.shutdown()
    client.close()
    return 0
