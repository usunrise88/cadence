from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

import pytest
from pydantic import BaseModel

from cadence_worker.defaults import DefaultsError, lookup
from cadence_worker.protocol_gen import StepResources
from cadence_worker.registry import registry
from cadence_worker.steps.base import (
    StepInputError,
    cadence_field,
    check_ranges,
    descriptor,
    input_hash,
    missing_metadata,
)
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.echo import EchoParams, EchoStep

HELP = Path(__file__).resolve().parents[2] / "docs" / "help" / "steps"


def ctx(tmp: Path, events: list[dict[str, Any]] | None = None) -> StepContext:
    sink = events if events is not None else []
    return StepContext(sink.append, work_dir=tmp)


def test_registry_lists_echo_with_its_contract() -> None:
    reg = registry()
    echo = reg["echo"]
    assert echo["version"] == "1"
    assert echo["consumes"] == {"text": "text"}
    assert echo["produces"] == {"text": "text"}
    assert echo["resources"] == {"gpu": False, "gpus": 0, "jobKind": "data"}
    assert echo.get("neutral") is True
    assert "role" not in echo
    assert echo["help"] == "steps.echo"
    assert echo["params"]["properties"]["prefix"]["x-cadence"]["source"] == "Cadence recommendation"


def test_every_step_kind_has_complete_metadata_and_a_help_article() -> None:
    for name, d in registry().items():
        slug = name.replace("_", "-")
        assert (HELP / f"{slug}.md").is_file(), f"docs/help/steps/{slug}.md is missing"
        assert d["help"] == f"steps.{slug}"


def test_missing_metadata_is_detected() -> None:
    class Bare(BaseModel):
        n: int = 1

    class BadStep:
        version: ClassVar[str] = "1"
        consumes: ClassVar[Mapping[str, str]] = {}
        produces: ClassVar[Mapping[str, str]] = {}
        resources: ClassVar[StepResources] = {}
        Params: ClassVar[type[BaseModel]] = Bare

        def run(
            self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], c: StepContext
        ) -> None:
            pass

    assert missing_metadata(BadStep) == ["n"]
    assert missing_metadata(EchoStep) == []


def test_echo_copies_with_prefix(tmp_path: Path) -> None:
    src = tmp_path / "in.txt"
    dst = tmp_path / "out.txt"
    src.write_text("shalom", encoding="utf-8")
    events: list[dict[str, Any]] = []
    EchoStep().run(EchoParams(prefix="> "), {"text": src}, {"text": dst}, ctx(tmp_path, events))
    assert dst.read_text(encoding="utf-8") == "> shalom"
    assert [e["e"] for e in events] == ["log", "progress"]


def test_echo_refuses_a_missing_input(tmp_path: Path) -> None:
    with pytest.raises(StepInputError):
        EchoStep().run(EchoParams(), {}, {"text": tmp_path / "o"}, ctx(tmp_path))


def test_ranges_are_enforced() -> None:
    check_ranges(EchoParams(prefix="x" * 64))
    with pytest.raises(StepInputError, match="longer"):
        check_ranges(EchoParams(prefix="x" * 65))

    class P(BaseModel):
        n: int = cadence_field(5, description="n", source="test", range={"min": 1, "max": 10})
        mode: str = cadence_field("a", description="m", source="test", range={"values": ["a", "b"]})

    check_ranges(P())
    with pytest.raises(StepInputError, match="maximum"):
        check_ranges(P(n=11))
    with pytest.raises(StepInputError, match="one of"):
        check_ranges(P(mode="c"))


def test_default_ref_reads_defaults_yaml_and_is_not_repeated() -> None:
    entry = lookup("training.steps")

    class P(BaseModel):
        steps: int = cadence_field(default_ref="training.steps")

    meta = P.model_json_schema()["properties"]["steps"]["x-cadence"]
    assert meta["defaultRef"] == "training.steps"
    assert meta["default"] == entry.value == P().steps
    assert meta["source"] == entry.source
    assert meta["range"] == entry.range
    with pytest.raises(TypeError, match=r"come from defaults\.yaml"):
        cadence_field(3000, default_ref="training.steps")
    with pytest.raises(DefaultsError):
        lookup("training.no_such_key")
    with pytest.raises(DefaultsError, match="section"):
        lookup("training")


def test_defaults_file_can_be_pointed_at(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    f = tmp_path / "defaults.yaml"
    f.write_text("version: 1\nx:\n  y: {value: 7, description: d, source: s}\n", encoding="utf-8")
    monkeypatch.setenv("CADENCE_DEFAULTS_FILE", str(f))
    assert lookup("x.y").value == 7
    assert lookup("x.y").range is None


def test_descriptor_carries_role_neutral_and_secrets() -> None:
    import fake_steps

    d = descriptor("probe", fake_steps.EnvProbe)
    assert d["secrets"] == ["HF_TOKEN"]
    assert d["resources"]["gpus"] == 1
    assert descriptor("slow", fake_steps.SlowTrain)["role"] == "train"


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


def test_input_hash_accepts_artifact_hashes_and_directories(tmp_path: Path) -> None:
    from cadence_worker.cas import Store

    d = tmp_path / "d"
    d.mkdir()
    (d / "f").write_text("x")
    h = Store(tmp_path / "cas").put_dir(d).hash
    assert input_hash("echo", EchoStep, EchoParams(), {"text": d}) == input_hash(
        "echo", EchoStep, EchoParams(), {"text": h}
    )


def test_context_validates_metric_names(tmp_path: Path) -> None:
    events: list[dict[str, Any]] = []
    c = ctx(tmp_path, events)
    c.metric("val_wer", 0.5, step=3, epoch=1.0)
    with pytest.raises(ValueError, match="metric name"):
        c.metric("Bad Name", 1.0)
    assert events[0]["name"] == "val_wer"
    assert events[0]["step"] == 3
    assert c.final_metrics == {"val_wer": 0.5}
