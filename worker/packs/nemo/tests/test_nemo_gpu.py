"""The NeMo pack on a card (``pytest -m gpu`` inside the nemo-speech image): materialize the base model, transcribe the
fixture clips with and without phrase boosting, score both with ``wer_score``. Needs the base model in the Hugging
Face cache (``HF_HOME`` or ``CADENCE_HF_READONLY_CACHES``) or network access."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest

from cadence_nemo.family import FAMILY, NAME
from cadence_nemo.steps.materialize import CheckpointFromBaseStep
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset
from cadence_worker.steps.wer_score import WerScoreParams, WerScoreStep

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"
NORMALIZER = {
    "locale": "he-IL",
    "unicode": "NFC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": True,
    "mappings": [],
    "numbers": "keep",
}

pytestmark = pytest.mark.gpu


def _ctx(tmp: Path) -> StepContext:
    return StepContext(lambda e: None, work_dir=tmp, card=Card(0, 8192))


TRANSCRIBE = """
import json, sys
from pathlib import Path
from cadence_nemo.steps.transcribe import TranscribeParams, TranscribeStep
from cadence_worker.steps.context import Card, StepContext
inputs, out, tmp = json.loads(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
ctx = StepContext(lambda e: None, work_dir=tmp, card=Card(0, 8192))
TranscribeStep().run(TranscribeParams(), {k: Path(v) for k, v in inputs.items()}, {"hypotheses": out}, ctx)
"""


def _transcribe(tmp: Path, inputs: dict[str, Path], out: Path) -> None:
    """Each decode in its own process, as a lease runs it (NeMo 3.0 does not restore a second .nemo in one process)."""
    args = [sys.executable, "-c", TRANSCRIBE, json.dumps({k: str(v) for k, v in inputs.items()}), str(out), str(tmp)]
    subprocess.run(args, check=True)


def _score(tmp: Path, hyps: Path, data: Path, tag: str) -> dict[str, Any]:
    (tmp / "norm.json").write_text(json.dumps(NORMALIZER), encoding="utf-8")
    out = tmp / f"scores-{tag}"
    inputs = {"hypotheses": hyps, "data": data, "normalizer": tmp / "norm.json"}
    WerScoreStep().run(WerScoreParams(), inputs, {"scores": out}, _ctx(tmp))
    return dict(json.loads((out / "summary.json").read_text(encoding="utf-8")))


def test_materialize_transcribe_boost_and_score(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("nemo")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
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
    base = {
        "format": "cadence.base_model/1",
        "versionId": "ver_base",
        "family": {"name": NAME},
        "model": FAMILY.conformance["base_model"],
    }
    (tmp_path / "base.json").write_text(json.dumps(base), encoding="utf-8")
    model = tmp_path / "model"
    CheckpointFromBaseStep().run(
        CheckpointFromBaseStep.Params(), {"base": tmp_path / "base.json"}, {"checkpoint": model}, _ctx(tmp_path)
    )
    plain = tmp_path / "plain.jsonl"
    _transcribe(tmp_path, {"model": model, "data": data}, plain)
    s0 = _score(tmp_path, plain, data, "plain")
    # Boost every reference word the plain decode missed.
    rows = [json.loads(x) for x in (tmp_path / "scores-plain" / "utterances.jsonl").read_text("utf-8").splitlines()]
    missed = sorted({ref for r in rows for op, ref, _ in r["ops"] if op in "SD" and ref and len(ref) >= 4})
    assert missed, "the base model decodes every fixture word; pick another check"
    (tmp_path / "boost.txt").write_text("# weight: 0.5\n" + "\n".join(missed) + "\n", encoding="utf-8")
    boosted = tmp_path / "boosted.jsonl"
    _transcribe(tmp_path, {"model": model, "data": data, "boost": tmp_path / "boost.txt"}, boosted)
    s1 = _score(tmp_path, boosted, data, "boosted")
    a = json.loads(plain.read_text("utf-8").splitlines()[0])
    b = json.loads(boosted.read_text("utf-8").splitlines()[0])
    assert "boost" not in a["decoding"]
    assert b["decoding"]["boost"]["weight"] == 0.5
    assert b["decoding"]["boost"]["terms"] == len(missed)
    assert a["decodingHash"] != b["decodingHash"]
    assert a["weightsHash"] == b["weightsHash"]

    def recalled(tag: str) -> int:
        lines = (tmp_path / f"scores-{tag}" / "utterances.jsonl").read_text("utf-8").splitlines()
        rows = [json.loads(x) for x in lines]
        return sum(1 for r in rows for w in r["hyp"].split() if w in missed and w in r["ref"].split())

    assert recalled("boosted") > recalled("plain"), (s0["wer"], s1["wer"])
