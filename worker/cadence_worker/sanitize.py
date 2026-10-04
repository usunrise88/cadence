"""What a step reports is cleaned before it leaves the worker: every value of the lease's secret environment and every
credential-shaped token is redacted (agents read logs, errors and meta: non-negotiable 8), and non-finite numbers are
dropped or nulled (JSON has no NaN or ±inf, and the control plane refuses such a body without retry).
"""

from __future__ import annotations

import math
import re
from collections.abc import Iterable, Mapping
from typing import Any

REDACTED = "[redacted]"
MIN_SECRET_LEN = 4
MOUNT_CREDENTIALS = re.compile(r"^CADENCE_MOUNT_[A-Z0-9_]+_CREDENTIALS$")

# Credentials that may reach a step's output without being one of its injected secrets: Cadence tokens (cdk_ API key,
# cst_ agent session, cwk_ worker, cah_ agent host, cep_ egress proxy), Hugging Face, GitHub and Anthropic/OpenAI
# keys, and anything after "Bearer".
TOKEN_PATTERNS: tuple[tuple[re.Pattern[str], str], ...] = (
    (re.compile(r"\b(?:cdk|cst|cwk|cah|cep)_[A-Za-z0-9]{8,}"), REDACTED),
    (re.compile(r"\bhf_[A-Za-z0-9]{16,}"), REDACTED),
    (re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})"), REDACTED),
    (re.compile(r"\bsk-[A-Za-z0-9_-]{20,}"), REDACTED),
    (re.compile(r"\b(Bearer\s+)[A-Za-z0-9_.~+/=-]{8,}", re.IGNORECASE), r"\1" + REDACTED),
)


class Redactor:
    """Replaces each secret value (longest first, so one that contains another goes whole) and each token pattern."""

    def __init__(self, secrets: Iterable[str] = ()) -> None:
        self._secrets = sorted({s for s in secrets if len(s) >= MIN_SECRET_LEN}, key=len, reverse=True)

    @classmethod
    def for_env(cls, env: Mapping[str, str]) -> Redactor:
        """A redactor of a lease's secret environment. A mount's credentials (``CADENCE_MOUNT_<NAME>_CREDENTIALS``,
        ``<accessKeyId>:<secretAccessKey>`` for s3) are also redacted half by half: the S3 signer uses each alone."""
        values = list(env.values())
        for k, v in env.items():
            if MOUNT_CREDENTIALS.match(k) and ":" in v:
                values.extend(v.split(":", 1))
        return cls(values)

    def text(self, s: str) -> str:
        for v in self._secrets:
            s = s.replace(v, REDACTED)
        for pattern, repl in TOKEN_PATTERNS:
            s = pattern.sub(repl, s)
        return s

    def value(self, v: Any) -> Any:
        """A JSON-ready copy: strings (and keys) redacted, non-finite floats as null, anything else as its string."""
        if isinstance(v, str):
            return self.text(v)
        if v is None or isinstance(v, bool | int):
            return v
        if isinstance(v, float):
            return v if math.isfinite(v) else None
        if isinstance(v, Mapping):
            return {self.text(str(k)): self.value(x) for k, x in v.items()}
        if isinstance(v, list | tuple):
            return [self.value(x) for x in v]
        return self.text(str(v))


def finite(v: Any) -> float | None:
    """v as a float when it is a finite number, else None."""
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    return f if math.isfinite(f) else None


def finite_metrics(metrics: Any) -> dict[str, float]:
    """The finite numeric entries of a name → value mapping (anything else is dropped)."""
    if not isinstance(metrics, Mapping):
        return {}
    out: dict[str, float] = {}
    for k, v in metrics.items():
        f = finite(v)
        if f is not None:
            out[str(k)] = f
    return out
