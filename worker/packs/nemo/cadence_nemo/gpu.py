"""The card under the lease's memory cap.

The harness applies ``CADENCE_MEMORY_CAP_MB`` as ``torch.cuda.set_per_process_memory_fraction(cap / total)``. That caps
PyTorch's caching allocator only; the CUDA context and the cuBLAS/cuDNN/cuFFT workspaces sit outside it (spike A3:
about 0.65 GB beyond a 20.5 GiB allocator on the staging card, 21.6 GB in nvidia-smi). The NeMo steps therefore re-apply
the fraction with ``packs.nemo.cuda_context_reserve_mb`` kept free, so the whole process stays under the cap that
nvidia-smi sees — the number that must fit beside the resident vLLM service.
"""

from __future__ import annotations

from typing import Any

MIB = 1024 * 1024


def allocator_fraction(cap_mb: int, reserve_mb: int, total_bytes: int) -> float:
    """The allocator's share of the card for a process capped at ``cap_mb`` with ``reserve_mb`` left for the context."""
    if cap_mb <= 0 or total_bytes <= 0:
        return 1.0
    usable = max(cap_mb - max(reserve_mb, 0), cap_mb // 4)
    return min(1.0, usable * MIB / total_bytes)


def apply_cap(cap_mb: int | None, reserve_mb: int) -> dict[str, Any]:
    """Cap device 0 (the lease's card); returns what was applied for logs and output meta."""
    import torch

    if not torch.cuda.is_available():
        return {"device": "cpu"}
    props = torch.cuda.get_device_properties(0)
    out: dict[str, Any] = {"device": props.name, "totalMb": int(props.total_memory // MIB)}
    if cap_mb:
        frac = allocator_fraction(cap_mb, reserve_mb, int(props.total_memory))
        torch.cuda.set_per_process_memory_fraction(frac, 0)
        out.update({"memoryCapMb": cap_mb, "allocatorCapMb": int(frac * props.total_memory // MIB), "fraction": frac})
    return out


def peak_mb() -> dict[str, int]:
    import torch

    if not torch.cuda.is_available():
        return {}
    return {
        "maxAllocatedMb": int(torch.cuda.max_memory_allocated() // MIB),
        "maxReservedMb": int(torch.cuda.max_memory_reserved() // MIB),
    }
