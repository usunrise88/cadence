from __future__ import annotations

import json
from dataclasses import replace
from pathlib import Path

import pytest

from cadence_worker import __version__
from cadence_worker.__main__ import main
from cadence_worker.conformance import check_schemas
from cadence_worker.conformance import run as run_conformance
from cadence_worker.registry import RegistryError, load_families, load_kinds, runtime_descriptor

HELP = Path(__file__).resolve().parents[2] / "docs" / "help"


def test_a_runtime_publishes_its_own_kinds_and_the_neutral_ones() -> None:
    toy = load_kinds("toy")
    assert {"echo", "toy_calibrate", "toy_train", "toy_average", "toy_transcribe"} <= set(toy)
    nemo = load_kinds("nemo-speech")
    assert "echo" in nemo
    assert not any(k.startswith("toy_") for k in nemo)
    assert [f.descriptor["name"] for f in load_families("toy")] == ["toy-ctc"]
    assert [f.descriptor["name"] for f in load_families("nemo-speech")] == ["nemo.fastconformer-rnnt.cache-aware"]
    assert {"oomptimizer_calibrate", "nemotron_finetune", "checkpoint_average", "nemotron_transcribe"} <= set(nemo)
    assert not any(k.startswith("nemotron_") for k in toy)


def test_runtime_descriptor_from_env_with_the_environment_lock(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CADENCE_RUNTIME", json.dumps({"name": "toy", "version": "1", "image": "cadence/worker-toy"}))
    monkeypatch.setenv("CADENCE_RUNTIME_DIGEST", "sha256:abc")
    d = runtime_descriptor()
    assert d["name"] == "toy"
    assert d["image"] == "cadence/worker-toy"
    assert d["digest"] == "sha256:abc"
    assert d["plugin"] == __version__
    assert "torch" in d.get("environment", {})


def test_runtime_descriptor_from_file(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    f = tmp_path / "runtime.json"
    f.write_text(json.dumps({"name": "nemo-speech", "version": "26.07", "environmentPackages": ["pydantic"]}))
    monkeypatch.delenv("CADENCE_RUNTIME", raising=False)
    monkeypatch.setenv("CADENCE_RUNTIME_FILE", str(f))
    d = runtime_descriptor()
    assert d["version"] == "26.07"
    assert list(d.get("environment", {})) == ["pydantic"]
    f.write_text(json.dumps({"name": "x"}))
    with pytest.raises(RegistryError):
        runtime_descriptor()


def test_cli_prints_the_registry(capsys: pytest.CaptureFixture[str]) -> None:
    assert main(["cadence_worker", "registry", "toy"]) == 0
    assert "toy_train" in json.loads(capsys.readouterr().out)
    assert main(["cadence_worker", "families", "toy"]) == 0
    assert json.loads(capsys.readouterr().out)[0]["name"] == "toy-ctc"


def test_the_toy_pack_passes_the_schema_checks() -> None:
    assert check_schemas("toy", load_kinds("toy"), load_families("toy"), HELP) == []


def test_schema_checks_catch_a_broken_pack(tmp_path: Path) -> None:
    kinds = load_kinds("toy")
    [fam] = load_families("toy")
    d = dict(fam.descriptor)
    d["roles"] = {"calibrate": "toy_train", "train": "toy_train", "transcribe": "nope"}
    d["latencyProfiles"] = [{"name": "offline", "latencyMs": 0}]
    broken = replace(fam, descriptor=d, fixtures=tmp_path)  # type: ignore[arg-type]
    problems = check_schemas("toy", kinds, [broken], tmp_path)
    text = "\n".join(problems)
    assert "role average is not mapped" in text
    assert "toy_train fills role 'calibrate' but declares 'train'" in text
    assert "maps to nope" in text
    assert "streaming capability without a streaming" in text
    assert "no conformance fixtures" in text
    assert "help article" in text
    assert check_schemas("empty", {}, [], None) == ["runtime 'empty' publishes no model family"]


@pytest.mark.conformance
def test_the_toy_pack_conforms(tmp_path: Path) -> None:
    report = run_conformance("toy", help_dir=HELP, work=tmp_path)
    assert report.ok, json.dumps(report.to_json(), indent=2)
    names = [s.name for s in report.stages]
    assert "toy-ctc/transcribe:320ms" in names
    assert "toy-ctc/stop" in names
