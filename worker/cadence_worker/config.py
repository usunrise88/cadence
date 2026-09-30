"""Worker configuration from the environment.

| Variable | Default | Meaning |
| --- | --- | --- |
| CADENCE_URL | http://control-plane:8080 | The control plane (the API lives under /api) |
| CADENCE_WORKER_TOKEN_FILE | /worker-credential/token | The cwk_ token the control plane writes at start |
| CADENCE_CAS_DIR | /var/lib/cadence/cas | The content store shared with the control plane |
| CADENCE_WORKER_HOST | staging | The compute host (compute entity name) this worker runs on |
| CADENCE_WORKER_SCRATCH | /var/lib/cadence/scratch | Per-lease scratch (on the store's file system) |
| CADENCE_RUNTIME / CADENCE_RUNTIME_FILE | /etc/cadence/runtime.json | The runtime descriptor baked into the image |
| CADENCE_WORKER_GPU | auto | on/off/auto: whether to read card telemetry (off for CPU runtimes) |
| CADENCE_WORKER_MAX_LEASES | 2 | Leases run at once; the control plane decides what fits on a card |
| CADENCE_STOP_GRACE_SECONDS | 60 | How long a stopping step may take to checkpoint before it is killed |
| CADENCE_CLAIM_WAIT_SECONDS | 20 | Long-poll wait of a claim (≤ 30) |
| CADENCE_WORKER_TRACE_FILE | (unset) | Append each finished step span here as a JSON line (cadence_worker.tracing) |
"""

from __future__ import annotations

import contextlib
import os
import socket
from dataclasses import dataclass
from pathlib import Path


def _instance() -> str:
    boot = ""
    with contextlib.suppress(OSError):
        boot = Path("/proc/sys/kernel/random/boot_id").read_text(encoding="utf-8").strip()[:8]
    return f"{socket.gethostname()}:{os.getpid()}" + (f":{boot}" if boot else "")


@dataclass(frozen=True)
class Config:
    url: str
    token_file: Path
    cas_dir: Path
    host: str
    scratch: Path
    gpu: str = "auto"
    max_leases: int = 2
    stop_grace: float = 60.0
    claim_wait: int = 20
    instance: str = ""

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> Config:
        e = dict(os.environ if env is None else env)
        return cls(
            url=e.get("CADENCE_URL", "http://control-plane:8080").rstrip("/"),
            token_file=Path(e.get("CADENCE_WORKER_TOKEN_FILE", "/worker-credential/token")),
            cas_dir=Path(e.get("CADENCE_CAS_DIR", "/var/lib/cadence/cas")),
            host=e.get("CADENCE_WORKER_HOST", "staging"),
            scratch=Path(e.get("CADENCE_WORKER_SCRATCH", "/var/lib/cadence/scratch")),
            gpu=e.get("CADENCE_WORKER_GPU", "auto"),
            max_leases=max(1, int(e.get("CADENCE_WORKER_MAX_LEASES", "2"))),
            stop_grace=float(e.get("CADENCE_STOP_GRACE_SECONDS", "60")),
            claim_wait=min(30, max(0, int(e.get("CADENCE_CLAIM_WAIT_SECONDS", "20")))),
            instance=e.get("CADENCE_WORKER_INSTANCE") or _instance(),
        )
