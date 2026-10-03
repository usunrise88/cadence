"""What a running step may do besides reading inputs and writing outputs: report progress, metrics and logs, learn
that it must stop, and read the card, memory cap, batch scale and resume point of its lease.

In the worker the context writes events to the harness over a pipe (cadence_worker.run_step); tests build one with a
list as the sink.
"""

from __future__ import annotations

import re
import threading
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

METRIC_NAME = re.compile(r"^[a-z][a-z0-9_./]{0,99}$")
LEVELS = ("debug", "info", "warn", "error")

Event = dict[str, Any]


def now_iso() -> str:
    return datetime.now(UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z")


@dataclass(frozen=True)
class Card:
    """The card the lease runs on (index as the step sees it after CUDA_VISIBLE_DEVICES) and its memory cap."""

    index: int
    memory_cap_mb: int


class StepContext:
    def __init__(
        self,
        emit: Callable[[Event], None],
        *,
        work_dir: Path,
        card: Card | None = None,
        batch_scale: float = 1.0,
        resume_from: Path | None = None,
        blob_path: Callable[[str], Path] | None = None,
        stop: threading.Event | None = None,
        attempt: int = 1,
        auxiliaries: Mapping[str, Mapping[str, Any]] | None = None,
    ) -> None:
        self._emit = emit
        self.work_dir = work_dir
        self.card = card
        self.batch_scale = batch_scale
        self.resume_from = resume_from
        self.attempt = attempt
        self._blob_path = blob_path
        self._stop = stop or threading.Event()
        self.meta: dict[str, dict[str, Any]] = {}
        self.final_metrics: dict[str, float] = {}
        self._auxiliaries = {k: dict(v) for k, v in (auxiliaries or {}).items()}

    def auxiliary(self, param: str) -> dict[str, Any]:
        """The registry version a ``registry_ref`` parameter names, as the control plane resolved it for the project:
        ``{versionId, name, version, payload}`` (an auxiliary's payload: roles, licence, languages, hfRepo and revision
        or service, …). StepInputError when the spec carries none (a control plane before phase 4, or a local run that
        passed none)."""
        from cadence_worker.steps.base import StepInputError

        ref = self._auxiliaries.get(param)
        if not ref or not isinstance(ref.get("payload"), dict):
            raise StepInputError(
                f"parameter {param!r} names a registry version the step spec does not carry; plan the run through the "
                "control plane (it resolves the version the project adopted)"
            )
        return ref

    @property
    def memory_cap_mb(self) -> int | None:
        return self.card.memory_cap_mb if self.card else None

    def progress(self, fraction: float, message: str = "") -> None:
        self._emit({"e": "progress", "fraction": min(1.0, max(0.0, float(fraction))), "message": message[:500]})

    def metric(self, name: str, value: float, step: int | None = None, epoch: float | None = None) -> None:
        """A metric point (loss, val_wer, lr, …); the last value of each name is also the step's final metric."""
        if not METRIC_NAME.match(name):
            raise ValueError(f"metric name {name!r} does not match {METRIC_NAME.pattern}")
        ev: Event = {"e": "metric", "name": name, "value": float(value), "wallTime": now_iso()}
        if step is not None:
            ev["step"] = int(step)
        if epoch is not None:
            ev["epoch"] = float(epoch)
        self.final_metrics[name] = float(value)
        self._emit(ev)

    def final_metric(self, name: str, value: float) -> None:
        """A final value only (seconds_per_step, …), reported with the outcome rather than as a series point."""
        self.final_metrics[name] = float(value)

    def log(self, msg: str, level: str = "info", **fields: Any) -> None:
        if level not in LEVELS:
            level = "info"
        ev: Event = {"e": "log", "t": now_iso(), "level": level, "msg": msg[:16000]}
        if fields:
            ev["fields"] = fields
        self._emit(ev)

    def should_stop(self) -> bool:
        """True once the control plane asked the step to stop: a training step writes its training-state output and
        returns; the lease is then released as cancelled with that output."""
        return self._stop.is_set()

    def set_meta(self, output: str, meta: Mapping[str, Any]) -> None:
        """Neutral, self-describing metadata of an output artifact (R42), e.g. a checkpoint's family, step, valWer and
        weightsHash."""
        self.meta[output] = dict(meta)
        self._emit({"e": "meta", "output": output, "meta": dict(meta)})

    def publish(
        self,
        output: str,
        path: Path,
        meta: Mapping[str, Any] | None = None,
        metrics: Mapping[str, float] | None = None,
    ) -> None:
        """Publish an intermediate instance of output (a validation checkpoint) while the step runs: the harness hashes
        ``path`` into the content store, removes it from the scratch directory, and the control plane records it and
        runs its output hooks at once (workerOutputs.new), so it survives a pause, a window close or a failure. Write
        each publication to its own path (in ``work_dir``) and do not touch it afterwards. The step's final outputs
        are still what it writes to ``outputs``."""
        ev: Event = {"e": "publish", "output": output, "path": str(path), "meta": dict(meta or {})}
        if metrics:
            ev["metrics"] = {k: float(v) for k, v in metrics.items()}
        self._emit(ev)

    def blob(self, h: str) -> Path:
        """The read-only path of a blob an input references by hash (a dataset's audio); never write to it."""
        if self._blob_path is None:
            raise RuntimeError("this context has no content store")
        return self._blob_path(h)
