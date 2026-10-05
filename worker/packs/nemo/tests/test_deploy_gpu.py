"""nemotron_export and nemotron_parity on a card (``pytest -m gpu`` inside the nemo-speech image built with the
TensorRT builder, worker/Dockerfile): materialize the base model, export it at 80 ms (step graph, engine, model
directory, deployable.json) and decode the fixtures as the parity reference with token ids and smoke inputs. Serving
the engine needs Triton, which the image does not carry: the round trip through a server is the stand's check
(docs/help/steps/nemotron-export.md "Measured")."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest

from cadence_nemo import deploy, smoke_client
from cadence_nemo.family import FAMILY, NAME
from cadence_nemo.steps.materialize import CheckpointFromBaseStep
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"

pytestmark = pytest.mark.gpu

RUN = """
import json, sys
from pathlib import Path
from cadence_nemo.steps.export import ExportParams, ExportStep
from cadence_nemo.steps.parity import ParityParams, ParityStep
from cadence_worker.steps.context import Card, StepContext
which, inputs, outputs, tmp = sys.argv[1], json.loads(sys.argv[2]), json.loads(sys.argv[3]), Path(sys.argv[4])
ctx = StepContext(lambda e: None, work_dir=tmp, card=Card(0, 8192))
step, params = (ExportStep(), ExportParams(card_class="test")) if which == "export" else (ParityStep(), ParityParams())
step.run(params, {k: Path(v) for k, v in inputs.items()}, {k: Path(v) for k, v in outputs.items()}, ctx)
"""


def _run(which: str, tmp: Path, inputs: dict[str, Path], outputs: dict[str, Path]) -> None:
    """Each step in its own process, as a lease runs it (NeMo 3.0 does not restore a second .nemo in one process)."""
    args = [
        sys.executable,
        "-c",
        RUN,
        which,
        json.dumps({k: str(v) for k, v in inputs.items()}),
        json.dumps({k: str(v) for k, v in outputs.items()}),
        str(tmp),
    ]
    subprocess.run(args, check=True)


def test_export_and_parity_reference(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("nemo")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
    if "11.2.1" not in deploy.installed_tensorrt():
        pytest.skip("no TensorRT 11.2.1 builder in this image")
    base = {"format": "cadence.base_model/1", "versionId": "ver_base", "family": {"name": NAME}}
    base["model"] = FAMILY.conformance["base_model"]
    (tmp_path / "base.json").write_text(json.dumps(base), encoding="utf-8")
    model = tmp_path / "model"
    CheckpointFromBaseStep().run(
        CheckpointFromBaseStep.Params(),
        {"base": tmp_path / "base.json"},
        {"checkpoint": model},
        StepContext(lambda e: None, work_dir=tmp_path, card=Card(0, 8192)),
    )

    out = tmp_path / "deployable"
    _run("export", tmp_path, {"model": model}, {"deployable": out})
    doc = json.loads((out / deploy.DEPLOYABLE_JSON).read_text(encoding="utf-8"))
    assert doc["format"] == deploy.FORMAT
    assert doc["profile"] == "80ms"
    assert doc["serving"]["engine"]["version"] == "11.2.1"
    assert doc["serving"]["engine"]["tf32"] is False
    assert [f["path"] for f in doc["files"]] == ["1/model.plan", "config.pbtxt"]
    assert doc["manifestSha256"] == deploy.manifest_sha256(deploy.model_files(out / "model"))
    assert not any(ln.startswith("name:") for ln in (out / "model" / "config.pbtxt").read_text().splitlines())
    assert (out / "onnx" / "step.onnx").is_file()
    assert (out / deploy.CLIENT_FILE).is_file()

    params = DatasetImportParams(
        format="folder-csv",
        path=str(FIXTURES),
        source_name="fixtures",
        licence="CC-BY-4.0",
        locale="he-IL",
        split_rule="all-test",
    )
    data = tmp_path / "data"
    write_dataset(params, records(params), data)
    hyp, smoke = tmp_path / "hyp.jsonl", tmp_path / "smoke"
    _run("parity", tmp_path, {"model": model, "data": data}, {"hypotheses": hyp, "smoke": smoke})
    rows = [json.loads(x) for x in hyp.read_text("utf-8").splitlines()]
    assert rows
    assert all(isinstance(r["tokens"], list) for r in rows)
    # An empty transcript (2 of the he fixtures at 80 ms under the base model) has no tokens, every other one has.
    assert all(bool(r["tokens"]) == bool(r["text"]) for r in rows)
    index = json.loads((smoke / deploy.SMOKE_JSON).read_text("utf-8"))
    assert len(index["items"]) == min(20, len(rows))
    first = smoke_client.read_chunks(str(smoke / index["items"][0]["file"]))
    assert first["frames"] == 17
    assert first["chunks"][0]["start"] == 1
