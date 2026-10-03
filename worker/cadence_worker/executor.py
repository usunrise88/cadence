"""Runs one lease: materialise its inputs from the content store into a scratch directory, start the step in its own
process (cadence_worker.run_step) with the lease's secret environment, forward its logs, metrics and progress to a
sink, stop it on request, and hash its outputs into the store for the release.

The serve loop's sink posts to the control plane; the conformance suite's sink keeps everything in memory, so both run
the same path.
"""

from __future__ import annotations

import contextlib
import json
import os
import queue
import shutil
import signal
import subprocess
import sys
import threading
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import IO, Any, Protocol, cast

from cadence_worker.cas import CasError, Store, parse_uri
from cadence_worker.protocol_gen import (
    ArtifactRef,
    Lease,
    MetricPoint,
    StepError,
    StepOutcome,
    WorkerLogLine,
    WorkerOutput,
)
from cadence_worker.registry import KindEntry
from cadence_worker.run_step import EVENT_FD_ENV, MEMORY_CAP_ENV
from cadence_worker.sanitize import Redactor, finite, finite_metrics
from cadence_worker.steps.context import now_iso
from cadence_worker.tracing import Span

TRAINING_STATE = "training-state"
# Never handed to a step: the worker's own credential and where the control plane is.
WORKER_ONLY_ENV = ("CADENCE_WORKER_TOKEN_FILE", "CADENCE_URL", "CADENCE_WORKER_TOKEN")


class Sink(Protocol):
    def log(self, line: WorkerLogLine) -> None: ...
    def metric(self, point: MetricPoint) -> None: ...
    def progress(self, fraction: float, message: str) -> None: ...
    def publish(self, output: WorkerOutput) -> None: ...


@dataclass
class MemorySink:
    """Keeps what a step reported (tests, the conformance suite)."""

    logs: list[WorkerLogLine] = field(default_factory=list)
    metrics: list[MetricPoint] = field(default_factory=list)
    progresses: list[tuple[float, str]] = field(default_factory=list)
    published: list[WorkerOutput] = field(default_factory=list)

    def log(self, line: WorkerLogLine) -> None:
        self.logs.append(line)

    def metric(self, point: MetricPoint) -> None:
        self.metrics.append(point)

    def progress(self, fraction: float, message: str) -> None:
        self.progresses.append((fraction, message))

    def publish(self, output: WorkerOutput) -> None:
        self.published.append(output)


def failed(kind: str, message: str) -> StepOutcome:
    err: StepError = {"type": "input" if kind == "input" else "step", "message": message[:4000], "retryable": False}
    return {"state": "failed", "error": err}


def _layout(ref: ArtifactRef | None) -> bool | None:
    meta: dict[str, Any] = (ref.get("meta") if ref else None) or {}
    layout = meta.get("layout")
    return {"dir": True, "file": False}.get(str(layout)) if layout else None


class LeaseRunner:
    def __init__(
        self,
        lease: Lease,
        *,
        kinds: Mapping[str, KindEntry],
        store: Store,
        scratch: Path,
        sink: Sink,
        stop_grace: float = 60.0,
        python: str = sys.executable,
        extra_env: Mapping[str, str] | None = None,
        keep_scratch: bool = False,
    ) -> None:
        self.lease = lease
        self.kinds = kinds
        self.store = store
        self.dir = scratch / lease["id"]
        self.sink = sink
        self.stop_grace = stop_grace
        self.python = python
        self.extra_env = dict(extra_env or {})
        self.keep_scratch = keep_scratch
        self.proc: subprocess.Popen[bytes] | None = None
        self.stop_reason: str | None = None
        self._lock = threading.Lock()
        self._threads: list[threading.Thread] = []
        self._redactor = Redactor((lease.get("env") or {}).values())
        self._dropped_metrics: set[str] = set()
        self.last_progress: tuple[float, str] | None = None
        # Intermediate outputs (ctx.publish) are stored and sent in order on their own thread, so hashing a large
        # checkpoint never stalls the step's event pipe; the outcome waits for them.
        self._publications: queue.Queue[dict[str, Any] | None] = queue.Queue()
        self._publisher: threading.Thread | None = None
        self.published: list[WorkerOutput] = []
        spec = lease["spec"]
        self.span = Span.child_of(
            lease.get("traceparent"),
            f"step {spec['kind']}@{spec['kindVersion']}",
            **{
                "cadence.lease.id": lease["id"],
                "cadence.job.id": lease["jobId"],
                "cadence.step.attempt": spec.get("attempt", 1),
            },
        )

    # ---------------------------------------------------------------- lifecycle

    def run(self) -> StepOutcome:
        out: StepOutcome = failed("step", "the worker stopped before the step ended")
        try:
            prepared = self.prepare()
            if prepared is not None:
                out = prepared
            else:
                self.start()
                out = self.wait()
        finally:
            err = out.get("error")
            if err:
                # Error text often quotes a failing request or an environment dump, and agents read it.
                err["message"] = self.redact(str(err.get("message", "")))[:4000]
            self.span.end(out["state"], err["message"] if err else "")
            if not self.keep_scratch:
                shutil.rmtree(self.dir, ignore_errors=True)
        return out

    def prepare(self) -> StepOutcome | None:
        """Materialise inputs and write step.json; a failed outcome when the lease cannot run here."""
        spec = self.lease["spec"]
        entry = self.kinds.get(spec["kind"])
        if entry is None or entry.cls.version != spec["kindVersion"]:
            return failed("input", f"this worker has no step kind {spec['kind']}@{spec['kindVersion']}")
        if self.dir.exists():
            shutil.rmtree(self.dir)
        for sub in ("in", "out", "work"):
            (self.dir / sub).mkdir(parents=True)
        uris = self.lease.get("inputs") or {}
        inputs: dict[str, str] = {}
        try:
            for name, ref in sorted(spec["inputs"].items()):
                h = parse_uri(uris[name]) if name in uris else ref["hash"]
                inputs[name] = str(self.store.materialise(h, self.dir / "in" / name, directory=_layout(ref)))
            resume = (spec.get("overrides") or {}).get("resumeFrom")
            resume_path = str(self.store.materialise(resume, self.dir / "resume")) if resume else None
        except (CasError, OSError) as e:
            return failed("input", f"cannot materialise the inputs: {e}")
        gpu = bool(spec["resources"].get("gpu"))
        card = self.lease["card"]
        job: dict[str, Any] = {
            "kind": spec["kind"],
            "ref": entry.ref,
            "params": spec.get("params") or {},
            "inputs": inputs,
            "outputs": {n: str(self.dir / "out" / n) for n in spec["outputs"]},
            "workDir": str(self.dir / "work"),
            # The step sees its card as device 0 (CUDA_VISIBLE_DEVICES names the physical one).
            "card": {"index": 0, "memoryCapMb": card["memoryCapMb"]} if gpu else None,
            "gpu": gpu,
            "batchScale": (spec.get("overrides") or {}).get("batchScale") or 1.0,
            "resumeFrom": resume_path,
            "casDir": str(self.store.root),
            "attempt": spec.get("attempt", 1),
            "auxiliaries": spec.get("auxiliaries") or {},
        }
        (self.dir / "step.json").write_text(json.dumps(job, indent=2), encoding="utf-8")
        return None

    def child_env(self, event_fd: int) -> dict[str, str]:
        env = {k: v for k, v in os.environ.items() if k not in WORKER_ONLY_ENV}
        env.update(self.extra_env)
        env.update(self.lease.get("env") or {})  # secrets: this process only, never logged
        spec = self.lease["spec"]
        card = self.lease["card"]
        env[EVENT_FD_ENV] = str(event_fd)
        env["PYTHONUNBUFFERED"] = "1"
        if spec["resources"].get("gpu"):
            env["CUDA_VISIBLE_DEVICES"] = str(card["index"])
            env[MEMORY_CAP_ENV] = str(card["memoryCapMb"])
        else:
            env["CUDA_VISIBLE_DEVICES"] = ""  # CPU steps stay off the card
        env["TRACEPARENT"] = self.span.traceparent  # the step span, a child of the lease's (the job span)
        return env

    def start(self) -> None:
        r, w = os.pipe()
        try:
            self.proc = subprocess.Popen(
                [self.python, "-m", "cadence_worker.run_step", str(self.dir)],
                env=self.child_env(w),
                pass_fds=(w,),
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                stdin=subprocess.DEVNULL,
                start_new_session=True,
                cwd=self.dir / "work",
            )
        finally:
            os.close(w)
        assert self.proc.stdout is not None
        self._spawn(self._read_output, self.proc.stdout)
        self._spawn(self._read_events, os.fdopen(r, "rb"))
        with self._lock:
            pending = self.stop_reason
        if pending:
            self._signal_stop()

    def stop(self, reason: str) -> None:
        """Ask the step to stop (SIGTERM; it may checkpoint), then kill it after the grace period."""
        with self._lock:
            if self.stop_reason is not None:
                return
            self.stop_reason = reason
        if self.proc is not None:
            self._signal_stop()

    def _signal_stop(self) -> None:
        proc = self.proc
        if proc is None or proc.poll() is not None:
            return
        self._killpg(proc, signal.SIGTERM)

        def kill_later() -> None:
            deadline = time.monotonic() + self.stop_grace
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    return
                time.sleep(0.1)
            self._killpg(proc, signal.SIGKILL)

        threading.Thread(target=kill_later, daemon=True, name="stop-grace").start()

    @staticmethod
    def _killpg(proc: subprocess.Popen[bytes], sig: signal.Signals) -> None:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.killpg(proc.pid, sig)

    def wait(self) -> StepOutcome:
        assert self.proc is not None
        code = self.proc.wait()
        # The step may have left children in its process group; they must not outlive the lease.
        self._killpg(self.proc, signal.SIGKILL)
        for t in self._threads:
            t.join(timeout=10)
        if self._publisher is not None:
            self._publications.put(None)
            self._publisher.join()
        return self.outcome(code)

    # ---------------------------------------------------------------- streams

    def _spawn(self, fn: Callable[[IO[bytes]], None], stream: IO[bytes]) -> None:
        t = threading.Thread(target=fn, args=(stream,), daemon=True, name=fn.__name__)
        t.start()
        self._threads.append(t)

    def redact(self, s: str) -> str:
        """s without the lease's secret values or credential-shaped tokens (agents read what the step reports)."""
        return self._redactor.text(s)

    def clean(self, v: Any) -> Any:
        """A JSON-ready copy of meta or log fields: strings redacted, non-finite numbers as null."""
        return self._redactor.value(v)

    def _warn(self, msg: str) -> None:
        self.sink.log({"t": now_iso(), "level": "warn", "msg": self.redact(msg)[:16000], "fields": self.span.fields()})

    def finite_metrics(self, metrics: Any, where: str) -> dict[str, float]:
        """The finite entries of a step's metrics; a dropped one (NaN, ±inf: val_wer = 0/0) is a warning line."""
        kept = finite_metrics(metrics)
        if isinstance(metrics, Mapping):
            dropped = sorted(str(k) for k in metrics if str(k) not in kept)
            if dropped:
                self._warn(f"dropped non-finite metrics {', '.join(dropped)} from {where}")
        return kept

    def _read_output(self, stream: IO[bytes]) -> None:
        with stream:
            for raw in stream:
                text = raw.decode("utf-8", "replace").rstrip("\n")
                if text:
                    self.sink.log(
                        {
                            "t": now_iso(),
                            "level": "info",
                            "msg": self.redact(text)[:16000],
                            "fields": self.span.fields(),
                        }
                    )

    def _read_events(self, stream: IO[bytes]) -> None:
        with stream:
            for raw in stream:
                try:
                    ev = json.loads(raw)
                except ValueError:
                    continue
                if isinstance(ev, dict):
                    self._event(ev)

    def _event(self, ev: dict[str, Any]) -> None:
        kind = ev.get("e")
        if kind == "log":
            line: WorkerLogLine = {
                "t": str(ev.get("t") or now_iso()),
                "level": ev.get("level", "info") if ev.get("level") in ("debug", "info", "warn", "error") else "info",
                "msg": self.redact(str(ev.get("msg", "")))[:16000],
            }
            fields: dict[str, Any] = {}
            if isinstance(ev.get("fields"), dict):
                fields = self.clean(ev["fields"])
            line["fields"] = {**fields, **self.span.fields()}
            self.sink.log(line)
        elif kind == "metric":
            self._metric(ev)
        elif kind == "progress":
            fraction = min(1.0, max(0.0, finite(ev.get("fraction", 0.0)) or 0.0))
            self.last_progress = (fraction, self.redact(str(ev.get("message", "")))[:500])
            self.sink.progress(*self.last_progress)
        elif kind == "publish":
            if self._publisher is None:
                self._publisher = threading.Thread(target=self._publish_loop, daemon=True, name="publish")
                self._publisher.start()
            self._publications.put(ev)

    def _metric(self, ev: Mapping[str, Any]) -> None:
        """A metric point; one whose value is not a finite number is dropped (JSON has no NaN), with a warning line
        the first time per name."""
        name = str(ev.get("name", ""))
        value = finite(ev.get("value"))
        if value is None:
            if name not in self._dropped_metrics:
                self._dropped_metrics.add(name)
                self._warn(f"dropped metric {name!r}: {ev.get('value')!r} is not a finite number (reported once)")
            return
        point: MetricPoint = {"name": name, "value": value, "wallTime": str(ev.get("wallTime") or now_iso())}
        if isinstance(ev.get("step"), int):
            point["step"] = int(ev["step"])
        if (epoch := finite(ev.get("epoch"))) is not None:
            point["epoch"] = epoch
        self.sink.metric(point)

    # ---------------------------------------------------------------- intermediate outputs

    def _publish_loop(self) -> None:
        while (ev := self._publications.get()) is not None:
            try:
                self.publish(ev)
            except Exception as e:
                self._warn(f"could not publish output {ev.get('output')!r}: {e}")

    def publish(self, ev: Mapping[str, Any]) -> WorkerOutput:
        """Store one ctx.publish path (inside the lease's scratch directory) and send it as an instance of one of the
        step's outputs; the path is removed once stored."""
        name = str(ev.get("output", ""))
        typ = self.lease["spec"]["outputs"].get(name)
        if typ is None:
            raise ValueError(f"the step has no output {name!r}")
        p = Path(str(ev.get("path", ""))).resolve()
        if not p.is_relative_to(self.dir.resolve()):
            raise ValueError(f"{p} is outside the lease's scratch directory")
        if not p.exists():
            raise FileNotFoundError(f"{p} does not exist")
        stored = self.store.put_path(p)
        raw_meta = ev.get("meta")
        meta: dict[str, Any] = self.clean(raw_meta) if isinstance(raw_meta, Mapping) else {}
        meta.setdefault("layout", "dir" if stored.directory else "file")
        out: WorkerOutput = {
            "name": name,
            "artifact": {"hash": stored.hash, "type": typ, "size": stored.size, "meta": meta},
        }
        if metrics := self.finite_metrics(ev.get("metrics"), f"the published {name}"):
            out["metrics"] = metrics
        self.sink.publish(out)
        self.published.append(out)
        if p.is_dir():
            shutil.rmtree(p, ignore_errors=True)
        else:
            p.unlink(missing_ok=True)
        return out

    # ---------------------------------------------------------------- outcome

    def outcome(self, code: int) -> StepOutcome:
        result_path = self.dir / "result.json"
        result: dict[str, Any] | None = None
        if result_path.is_file():
            try:
                result = json.loads(result_path.read_text(encoding="utf-8"))
            except ValueError:
                result = None
        if result is None:
            if self.stop_reason:
                return {"state": "cancelled", "error": {"type": "cancelled", "message": self.stop_reason}}
            return failed("step", f"the step process exited with code {code} without a result")
        metrics = self.finite_metrics(result.get("metrics"), "the outcome")
        raw_meta = result.get("meta")
        meta: dict[str, Any] = self.clean(raw_meta) if isinstance(raw_meta, Mapping) else {}
        state = result.get("state")
        if state == "failed":
            out: StepOutcome = {"state": "failed", "error": cast(StepError, dict(result["error"]))}
            if metrics:
                out["metrics"] = metrics
            return out
        spec = self.lease["spec"]
        wanted = spec["outputs"]
        if state == "cancelled" or self.stop_reason:
            wanted = {n: t for n, t in wanted.items() if t == TRAINING_STATE}
        try:
            outputs = self.store_outputs(wanted, meta)
        except (CasError, OSError) as e:
            return failed("step", f"cannot store the outputs: {e}")
        if state == "cancelled" or self.stop_reason:
            cancelled: StepOutcome = {
                "state": "cancelled",
                "error": {"type": "cancelled", "message": self.stop_reason or "the step stopped"},
            }
            if outputs:
                cancelled["outputs"] = outputs
            if metrics:
                cancelled["metrics"] = metrics
            return cancelled
        done: StepOutcome = {"state": "done", "outputs": outputs}
        if metrics:
            done["metrics"] = metrics
        return done

    def store_outputs(self, wanted: Mapping[str, str], meta: Mapping[str, Any]) -> dict[str, ArtifactRef]:
        out: dict[str, ArtifactRef] = {}
        for name, typ in sorted(wanted.items()):
            p = self.dir / "out" / name
            if not p.exists():
                continue
            stored = self.store.put_path(p)
            m = dict(meta.get(name) or {})
            m.setdefault("layout", "dir" if stored.directory else "file")
            out[name] = {"hash": stored.hash, "type": typ, "size": stored.size, "meta": m}
        return out
