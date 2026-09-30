"""Trace context for a lease (docs/spec/06-platform.md "Worker protocol"), without an OpenTelemetry dependency.

The lease's ``traceparent`` is the control plane's job span. The worker opens a step span under it: the step process
gets ``TRACEPARENT`` naming the step span (so a library that speaks W3C trace context continues it), every log line
the worker forwards carries ``trace_id`` and ``span_id`` in its fields, and the finished span is appended as one JSON
line to ``CADENCE_WORKER_TRACE_FILE`` when that is set — the same shape of file trace the control plane writes
(``traces.jsonl``).
"""

from __future__ import annotations

import json
import os
import re
import secrets
import threading
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

TRACE_FILE_ENV = "CADENCE_WORKER_TRACE_FILE"
_TRACEPARENT = re.compile(r"^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$")
_lock = threading.Lock()


def parse(tp: str | None) -> tuple[str, str, str] | None:
    """(trace id, parent span id, flags) of a W3C traceparent; None when it is missing or malformed."""
    m = _TRACEPARENT.match((tp or "").strip().lower())
    if not m or set(m.group(1)) == {"0"} or set(m.group(2)) == {"0"}:
        return None
    return m.group(1), m.group(2), m.group(3)


def _now() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


@dataclass
class Span:
    """One span: a child of the lease's traceparent, or the root of a new trace when the lease has none."""

    name: str
    trace_id: str
    span_id: str
    parent_id: str | None
    flags: str = "01"
    start: str = field(default_factory=_now)
    attributes: dict[str, Any] = field(default_factory=dict)

    @classmethod
    def child_of(cls, traceparent: str | None, name: str, **attributes: Any) -> Span:
        parent = parse(traceparent)
        if parent is None:
            return cls(name, secrets.token_hex(16), secrets.token_hex(8), None, attributes=attributes)
        return cls(name, parent[0], secrets.token_hex(8), parent[1], parent[2], attributes=attributes)

    @property
    def traceparent(self) -> str:
        return f"00-{self.trace_id}-{self.span_id}-{self.flags}"

    def fields(self) -> dict[str, str]:
        return {"trace_id": self.trace_id, "span_id": self.span_id}

    def end(self, status: str, message: str = "", trace_file: str | None = None) -> dict[str, Any]:
        """Close the span; appends it to the trace file (argument or CADENCE_WORKER_TRACE_FILE) when one is set."""
        record: dict[str, Any] = {
            "Name": self.name,
            "SpanContext": {"TraceID": self.trace_id, "SpanID": self.span_id, "TraceFlags": self.flags},
            "Parent": {"TraceID": self.trace_id, "SpanID": self.parent_id or "", "Remote": True},
            "SpanKind": "internal",
            "StartTime": self.start,
            "EndTime": _now(),
            "Status": {"Code": "Error" if status == "failed" else "Ok", "Description": message[:500]},
            "Attributes": [{"Key": k, "Value": v} for k, v in sorted(self.attributes.items())]
            + [{"Key": "cadence.step.state", "Value": status}],
            "Resource": [{"Key": "service.name", "Value": "cadence-worker"}],
        }
        path = trace_file if trace_file is not None else os.environ.get(TRACE_FILE_ENV)
        if path:
            p = Path(path)
            p.parent.mkdir(parents=True, exist_ok=True)
            with _lock, p.open("a", encoding="utf-8") as f:
                f.write(json.dumps(record, separators=(",", ":"), default=str) + "\n")
        return record
