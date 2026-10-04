"""Typed step errors (StepError in api/openapi.yaml): ``oom`` gets one automatic retry at 0.75 of the batch,
``input`` will not get better by retrying, anything else is ``step``."""

from __future__ import annotations

from pydantic import ValidationError

from cadence_worker.protocol_gen import StepError
from cadence_worker.steps.base import AuxiliaryUnavailable, StepInputError

OOM_MARKERS = ("CUDA out of memory", "CUDA error: out of memory", "CUBLAS_STATUS_ALLOC_FAILED")


def is_oom(exc: BaseException) -> bool:
    """torch.OutOfMemoryError / torch.cuda.OutOfMemoryError (matched by name so the harness need not import torch), or
    a RuntimeError carrying CUDA's out-of-memory message, anywhere in the cause chain."""
    seen: set[int] = set()
    e: BaseException | None = exc
    while e is not None and id(e) not in seen:
        seen.add(id(e))
        if any(c.__name__ == "OutOfMemoryError" for c in type(e).__mro__):
            return True
        if isinstance(e, RuntimeError) and any(m in str(e) for m in OOM_MARKERS):
            return True
        e = e.__cause__ or e.__context__
    return False


def classify(exc: BaseException) -> StepError:
    msg = f"{type(exc).__name__}: {exc}"[:4000]
    if is_oom(exc):
        return {"type": "oom", "message": msg, "retryable": True}
    if isinstance(exc, AuxiliaryUnavailable):
        return {"type": "step", "message": f"auxiliary-unavailable: {exc}"[:4000], "retryable": True}
    if isinstance(exc, StepInputError | ValidationError):
        return {"type": "input", "message": msg, "retryable": False}
    return {"type": "step", "message": msg, "retryable": False}
