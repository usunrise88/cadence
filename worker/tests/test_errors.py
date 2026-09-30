from __future__ import annotations

import pytest
import torch
from pydantic import BaseModel, ValidationError

from cadence_worker.errors import classify, is_oom
from cadence_worker.steps.base import StepInputError


class OutOfMemoryError(RuntimeError):
    """Stands in for a framework's own OOM class (matched by name)."""


def validation_error() -> ValidationError:
    class P(BaseModel):
        n: int

    try:
        P.model_validate({"n": "x"})
    except ValidationError as e:
        return e
    raise AssertionError


def chained() -> Exception:
    try:
        try:
            raise RuntimeError("CUDA out of memory. Tried to allocate 20.00 MiB")
        except RuntimeError as inner:
            raise ValueError("training failed") from inner
    except ValueError as outer:
        return outer


@pytest.mark.parametrize(
    ("exc", "kind", "retryable"),
    [
        (torch.cuda.OutOfMemoryError("CUDA out of memory"), "oom", True),
        (OutOfMemoryError("anything"), "oom", True),
        (RuntimeError("CUDA error: out of memory"), "oom", True),
        (chained(), "oom", True),
        (StepInputError("no such profile"), "input", False),
        (validation_error(), "input", False),
        (ValueError("bug"), "step", False),
        (RuntimeError("shape mismatch"), "step", False),
    ],
)
def test_classify(exc: Exception, kind: str, retryable: bool) -> None:
    err = classify(exc)
    assert err["type"] == kind
    assert err.get("retryable") is retryable
    assert len(err["message"]) <= 4000


def test_host_memory_error_is_not_card_oom() -> None:
    assert not is_oom(MemoryError())
