"""lid_classify@2 without a card: VoxLingua107's labels as BCP 47 subtags, the ranking, the step's glue with the
classifier replaced by a fake (rows in manifest order, batches that follow an OOM retry's scale, the engine check) and
the published schema. The real classifier is the GPU test in test_lid_gpu.py."""

from __future__ import annotations

import json
import sys
import types
from collections.abc import Sequence
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_omni import ecapa
from cadence_omni.steps import lid as lid_step
from cadence_worker.registry import registry
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[2] / "nemo" / "cadence_nemo" / "fixtures"
LID: dict[str, Any] = {
    "versionId": "ver_lid",
    "name": "auxiliary/lid-voxlingua107",
    "version": "2026-10-03.000000000000",
    "payload": {
        "roles": ["lid"],
        "licence": "Apache-2.0 (model); VoxLingua107 CC-BY-4.0",
        "outputsCommercialUse": True,
        "languages": ["*"],
        "hfRepo": "speechbrain/lang-id-voxlingua107-ecapa",
        "revision": "0253049ae131d6a4be1c4f0d8b0ff483a0f8c8e9",
        "engine": "speechbrain-ecapa",
    },
}


def test_labels_become_bcp47_subtags() -> None:
    assert ecapa.language_of("iw: Hebrew") == "he"
    assert ecapa.language_of("sr: Serbian") == "sr"
    assert ecapa.language_of("jw: Javanese") == "jv"
    assert ecapa.language_of("HR") == "hr"


def test_rank_is_a_softmax_most_probable_first() -> None:
    top = ecapa.rank([np.log(0.2), np.log(0.7), np.log(0.1)], ["hr", "sr", "bs"], 2)
    assert [c for c, _ in top] == ["sr", "hr"]
    assert top[0][1] == pytest.approx(0.7)
    assert sum(p for _, p in ecapa.rank([0.0, 0.0], ["b", "a"], 5)) == pytest.approx(1.0)
    assert [c for c, _ in ecapa.rank([0.0, 0.0], ["b", "a"], 5)] == ["a", "b"]  # ties by code


class FakeEcapa:
    def __init__(self, path: Path, work: Path, device: str) -> None:
        self.codes = ["he", "sr", "hr"]
        self.batches: list[int] = []

    def classify(self, clips: Sequence[np.ndarray[Any, np.dtype[np.float32]]], k: int) -> list[list[tuple[str, float]]]:
        self.batches.append(len(clips))
        return [[("he", 0.91), ("sr", 0.05), ("hr", 0.04)][:k] for _ in clips]


@pytest.fixture
def fake(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> list[FakeEcapa]:
    made: list[FakeEcapa] = []

    def make(path: Path, work: Path, device: str) -> FakeEcapa:
        made.append(FakeEcapa(path, work, device))
        return made[-1]

    monkeypatch.setattr(ecapa, "snapshot", lambda repo, revision: tmp_path)
    monkeypatch.setattr(ecapa, "Ecapa", make)
    monkeypatch.setattr(lid_step, "device", lambda: "cpu")
    return made


def _dataset(tmp_path: Path) -> Path:
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
    return data


def test_lid_writes_the_lid_artifact(tmp_path: Path, fake: list[FakeEcapa]) -> None:
    data = _dataset(tmp_path)
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": LID})
    out = tmp_path / "lid.jsonl"
    lid_step.LidClassifyStep().run(lid_step.LidParams(top_k=2), {"data": data}, {"lid": out}, ctx)
    rows = [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()]
    manifest = [json.loads(x) for x in (data / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert len(rows) == len(manifest) == 10
    assert rows[0]["language"] == "he"
    assert rows[0]["top"] == [["he", 0.91], ["sr", 0.05]]
    assert rows[0]["expected"] == "he-IL"
    assert rows[0]["model"]["engine"] == "speechbrain-ecapa"
    assert ctx.meta["lid"]["classifier"] == "lid-voxlingua107"
    assert ctx.meta["lid"]["utterances"] == 10


def test_a_retry_at_a_smaller_batch_scale_shrinks_the_batches(tmp_path: Path, fake: list[FakeEcapa]) -> None:
    data = _dataset(tmp_path)
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": LID}, batch_scale=0.75)
    lid_step.LidClassifyStep().run(lid_step.LidParams(batch_size=4), {"data": data}, {"lid": tmp_path / "l"}, ctx)
    assert fake[-1].batches == [3, 3, 3, 1]


def test_a_whisper_auxiliary_is_refused(tmp_path: Path, fake: list[FakeEcapa]) -> None:
    data = _dataset(tmp_path)
    aux = {**LID, "name": "auxiliary/whisper-large-v3"}
    aux["payload"] = {**LID["payload"], "roles": ["pseudolabel", "lid"], "engine": "transformers-whisper"}
    ctx = StepContext(lambda e: None, work_dir=tmp_path, auxiliaries={"auxiliary": aux})
    with pytest.raises(StepInputError, match="speechbrain-ecapa"):
        lid_step.LidClassifyStep().run(lid_step.LidParams(), {"data": data}, {"lid": tmp_path / "l"}, ctx)
    assert not fake  # refused before anything loads


def test_the_classifier_reads_speechbrain_labels(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    torch = pytest.importorskip("torch")

    class Encoder:
        def __len__(self) -> int:
            return 3

        def decode_ndim(self, i: int) -> str:
            return ["iw: Hebrew", "sr: Serbian", "hr: Croatian"][i]

    class Classifier:
        hparams = types.SimpleNamespace(label_encoder=Encoder())

        @classmethod
        def from_hparams(
            cls, source: str, savedir: str, run_opts: dict[str, str], overrides: dict[str, str]
        ) -> Classifier:
            assert overrides == {"pretrained_path": source}  # the pinned snapshot, never the Hub's newest
            return cls()

        def classify_batch(self, wavs: Any, lens: Any) -> tuple[Any, ...]:
            assert wavs.shape == (2, 1600)
            assert lens.tolist() == pytest.approx([1.0, 0.5])
            logp = torch.log(torch.tensor([[0.8, 0.1, 0.1], [0.1, 0.6, 0.3]]))
            return (logp, None, None, None)

    mod = types.ModuleType("speechbrain.inference.classifiers")
    mod.EncoderClassifier = Classifier  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "speechbrain.inference.classifiers", mod)
    clf = ecapa.Ecapa(tmp_path, tmp_path, "cpu")
    assert clf.codes == ["he", "sr", "hr"]
    clips = [np.zeros(1600, dtype=np.float32), np.zeros(800, dtype=np.float32)]
    top = clf.classify(clips, 2)
    assert [t[0][0] for t in top] == ["he", "sr"]
    assert top[1][1][0] == "hr"
    assert top[1][1][1] == pytest.approx(0.3)


def test_published_schema() -> None:
    d = registry()["lid_classify"]
    assert d["version"] == "2"
    assert lid_step.LidClassifyStep.runtime == "omni"
    assert d["consumes"] == {"data": "dataset"}
    assert d["produces"] == {"lid": "lid"}
    x = d["params"]["properties"]["auxiliary"]["x-cadence"]
    assert x["registryRef"] == {"kind": "auxiliary", "role": "lid"}
    assert x["default"] == "auxiliary/lid-voxlingua107"
