"""GPU memory cap shared by every A3 script (import first, before any CUDA work).

The staging card is shared with a resident vLLM (~23.8 GB of 48 GB), so the process must stay under ~22 GB as seen by
nvidia-smi. `torch.cuda.set_per_process_memory_fraction` caps only PyTorch's caching allocator; the CUDA context,
cuBLAS/cuDNN workspaces and NCCL live outside it (~0.6-1 GB measured), so the allocator cap is set below the target.
A step kind would take the cap in GiB from the compute seed (e.g. `memoryCapGiB`) and derive the fraction per card.
"""

from __future__ import annotations

import os

import torch

CAP_GIB = float(os.environ.get("A3_CAP_GIB", "20.5"))


def apply(device: int = 0) -> float:
    total = torch.cuda.get_device_properties(device).total_memory
    frac = CAP_GIB * 2**30 / total
    torch.cuda.set_per_process_memory_fraction(frac, device)
    print(f"[a3.cap] device={torch.cuda.get_device_name(device)} total={total / 2**30:.2f} GiB "
          f"allocator cap={CAP_GIB} GiB fraction={frac:.4f}", flush=True)
    return frac


def peak() -> str:
    return (f"max_allocated={torch.cuda.max_memory_allocated() / 2**30:.2f} GiB "
            f"max_reserved={torch.cuda.max_memory_reserved() / 2**30:.2f} GiB")
