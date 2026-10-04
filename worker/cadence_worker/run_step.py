"""One step in its own process: ``python -m cadence_worker.run_step <lease dir>``.

The harness (cadence_worker.executor) writes ``<lease dir>/step.json``, materialises the inputs, passes the secret
environment of the lease to this process only, and reads events from the pipe named by ``CADENCE_EVENT_FD`` (NDJSON:
log, progress, metric, meta). SIGTERM asks the step to stop (``ctx.should_stop()``); a step that returns after that is
reported ``cancelled`` with whatever outputs it wrote (the training-state of a training step). The result goes to
``<lease dir>/result.json``.
"""

from __future__ import annotations

import importlib
import importlib.util
import json
import os
import signal
import sys
import threading
import traceback
from pathlib import Path
from typing import IO, Any

from cadence_worker.cas import Store
from cadence_worker.errors import classify
from cadence_worker.mounts import CACHE_ENV, Mounts
from cadence_worker.steps.base import StepInputError, check_ranges, implements_step_kind, missing_metadata
from cadence_worker.steps.context import Card, Event, StepContext

EVENT_FD_ENV = "CADENCE_EVENT_FD"
MEMORY_CAP_ENV = "CADENCE_MEMORY_CAP_MB"


class EventWriter:
    def __init__(self, stream: IO[str]) -> None:
        self._stream = stream
        self._lock = threading.Lock()

    def __call__(self, ev: Event) -> None:
        line = json.dumps(ev, separators=(",", ":"), default=str)
        with self._lock:
            try:
                self._stream.write(line + "\n")
                self._stream.flush()
            except (BrokenPipeError, ValueError):
                pass  # the harness went away; the result file still records the outcome


def open_events(lease_dir: Path) -> IO[str]:
    fd = os.environ.get(EVENT_FD_ENV)
    if fd:
        return os.fdopen(int(fd), "w", buffering=1, encoding="utf-8")
    return (lease_dir / "events.ndjson").open("a", encoding="utf-8")


def load_kind(ref: str) -> type[Any]:
    module, _, attr = ref.partition(":")
    obj: Any = importlib.import_module(module)
    for part in attr.split("."):
        obj = getattr(obj, part)
    if not implements_step_kind(obj):
        raise TypeError(f"{ref} does not implement the StepKind contract")
    return obj  # type: ignore[no-any-return]


def apply_memory_cap(cap_mb: int, emit: EventWriter) -> None:
    """Cap this process's share of the card (torch.cuda.set_per_process_memory_fraction) when torch is present."""
    if cap_mb <= 0 or importlib.util.find_spec("torch") is None:
        return
    torch: Any = importlib.import_module("torch")
    if not torch.cuda.is_available():
        return
    total = int(torch.cuda.get_device_properties(0).total_memory)
    fraction = min(1.0, cap_mb * 1024 * 1024 / total)
    torch.cuda.set_per_process_memory_fraction(fraction, 0)
    emit({"e": "log", "level": "info", "msg": f"card memory capped at {cap_mb} MiB ({fraction:.3f} of the card)"})


def write_result(path: Path, result: dict[str, Any]) -> None:
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(result), encoding="utf-8")
    os.replace(tmp, path)


def execute(lease_dir: Path, stop: threading.Event, emit: EventWriter) -> dict[str, Any]:
    job = json.loads((lease_dir / "step.json").read_text(encoding="utf-8"))
    ctx: StepContext | None = None
    try:
        kind = load_kind(job["ref"])
        if bad := missing_metadata(kind):
            raise TypeError(f"parameters without complete x-cadence metadata: {bad}")
        params = kind.Params.model_validate(job.get("params") or {})
        check_ranges(params)
        inputs = {k: Path(v) for k, v in job["inputs"].items()}
        for name, p in inputs.items():
            if not p.exists():
                raise StepInputError(f"input {name!r} is missing at {p}")
        outputs = {k: Path(v) for k, v in job["outputs"].items()}
        card = Card(int(job["card"]["index"]), int(job["card"]["memoryCapMb"])) if job.get("card") else None
        if job.get("gpu") and card is not None:
            apply_memory_cap(card.memory_cap_mb, emit)
        cas_dir = job.get("casDir")
        store = Store(Path(cas_dir)) if cas_dir else None
        ctx = StepContext(
            emit,
            work_dir=Path(job["workDir"]),
            card=card,
            batch_scale=float(job.get("batchScale") or 1.0),
            resume_from=Path(job["resumeFrom"]) if job.get("resumeFrom") else None,
            blob_path=store.path if store else None,
            stop=stop,
            attempt=int(job.get("attempt") or 1),
            mounts=Mounts(
                job.get("mounts") or [],
                cache_dir=Path(os.environ.get(CACHE_ENV) or Path(job["workDir"]) / "mount-cache"),
            ),
            auxiliaries=job.get("auxiliaries") or {},
        )
        kind().run(params, inputs, outputs, ctx)
        if stop.is_set():
            return {"state": "cancelled", "metrics": ctx.final_metrics, "meta": ctx.meta}
        optional = set(getattr(kind, "optional_outputs", ()))  # e.g. a train step's final training state
        missing = [n for n, p in outputs.items() if not p.exists() and n not in optional]
        if missing:
            raise RuntimeError(f"the step did not write its outputs {missing}")
        return {"state": "done", "metrics": ctx.final_metrics, "meta": ctx.meta}
    except BaseException as exc:
        if isinstance(exc, KeyboardInterrupt | SystemExit) and stop.is_set():
            return {"state": "cancelled", "metrics": ctx.final_metrics if ctx else {}, "meta": ctx.meta if ctx else {}}
        emit({"e": "log", "level": "error", "msg": "".join(traceback.format_exception(exc))[-16000:]})
        return {
            "state": "failed",
            "error": classify(exc),
            "metrics": ctx.final_metrics if ctx else {},
            "meta": ctx.meta if ctx else {},
        }


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: python -m cadence_worker.run_step <lease dir>", file=sys.stderr)
        return 2
    lease_dir = Path(argv[1])
    stop = threading.Event()

    def on_term(signum: int, frame: object) -> None:
        stop.set()

    signal.signal(signal.SIGTERM, on_term)
    with open_events(lease_dir) as stream:
        emit = EventWriter(stream)
        result = execute(lease_dir, stop, emit)
        write_result(lease_dir / "result.json", result)
    return 0


if __name__ == "__main__":
    code = main(sys.argv)
    # The result is written: leave at once. A library may keep non-daemon threads alive (a streamed Hugging Face
    # dataset abandoned at its cap leaves one per configuration), and a normal exit would wait for them forever while
    # the lease waits for this process (the replay import on the stand, 2026-10-02).
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(code)
