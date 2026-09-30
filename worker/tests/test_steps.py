from __future__ import annotations

from pathlib import Path

import pytest

from cadence_worker.__main__ import registry
from cadence_worker.steps.base import input_hash, missing_metadata
from cadence_worker.steps.echo import EchoParams, EchoStep

HELP = Path(__file__).resolve().parents[2] / "docs" / "help" / "steps"


def test_registry_lists_echo_with_its_contract() -> None:
    reg = registry()
    assert reg["echo"]["version"] == "1"
    assert reg["echo"]["consumes"] == ["text"]
    assert reg["echo"]["params"]["properties"]["prefix"]["x-cadence"]["source"] == "Cadence recommendation"


def test_every_step_kind_has_complete_metadata_and_a_help_article() -> None:
    for name, _ in registry().items():
        slug = name.replace("_", "-")
        assert (HELP / f"{slug}.md").is_file(), f"docs/help/steps/{slug}.md is missing"


def test_missing_metadata_is_detected() -> None:
    from collections.abc import Mapping
    from typing import Any, ClassVar

    from pydantic import BaseModel

    class Bare(BaseModel):
        n: int = 1

    class BadStep:
        version: ClassVar[str] = "1"
        consumes: ClassVar[tuple[str, ...]] = ()
        produces: ClassVar[tuple[str, ...]] = ()
        resources: ClassVar[Mapping[str, Any]] = {}
        Params: ClassVar[type[BaseModel]] = Bare

        def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path]) -> None:
            pass

    assert missing_metadata(BadStep) == ["n"]
    assert missing_metadata(EchoStep) == []


def test_echo_copies_with_prefix(tmp_path: Path) -> None:
    src = tmp_path / "in.txt"
    dst = tmp_path / "out.txt"
    src.write_text("shalom", encoding="utf-8")
    EchoStep().run(EchoParams(prefix="> "), {"text": src}, {"text": dst})
    assert dst.read_text(encoding="utf-8") == "> shalom"


@pytest.mark.parametrize(("change", "same"), [("nothing", True), ("params", False), ("input", False)])
def test_input_hash_is_stable_and_sensitive(tmp_path: Path, change: str, same: bool) -> None:
    src = tmp_path / "in.txt"
    src.write_text("a", encoding="utf-8")
    before = input_hash("echo", EchoStep, EchoParams(), {"text": src})
    params = EchoParams()
    if change == "params":
        params = EchoParams(prefix="x")
    if change == "input":
        src.write_text("b", encoding="utf-8")
    after = input_hash("echo", EchoStep, params, {"text": src})
    assert (before == after) is same
