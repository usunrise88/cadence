"""Step-kind deprecation (phase 4 · stream R): a pack deprecates a kind version in what it publishes; the control plane
warns on plans that pin it and refuses new pins from the cut-off day (docs/help/errors/step-kind-deprecated.md)."""

from __future__ import annotations

from typing import ClassVar

from cadence_worker.steps.base import descriptor
from cadence_worker.steps.echo import EchoStep


class _OldEcho(EchoStep):
    deprecated_after: ClassVar[str] = "2026-12-01"
    replaced_by: ClassVar[str] = "echo@2"
    deprecation_note: ClassVar[str] = "echo@2 keeps the prefix"


def test_a_deprecated_kind_publishes_its_deprecation() -> None:
    d = descriptor("echo", _OldEcho)
    assert d["deprecation"] == {"after": "2026-12-01", "replacedBy": "echo@2", "note": "echo@2 keeps the prefix"}
    # The schema is unchanged: deprecating a version is not a new version (step-kinds.lock.json stays).
    assert d["params"] == descriptor("echo", EchoStep)["params"]


def test_a_current_kind_publishes_none() -> None:
    assert "deprecation" not in descriptor("echo", EchoStep)
