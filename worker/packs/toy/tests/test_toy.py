"""The toy pack's pieces in process (the conformance suite runs them through the harness)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
import torch

from cadence_toy.data import read_dataset
from cadence_toy.family import FAMILY, PROFILES
from cadence_toy.model import (
    FRAME_MS,
    CharTokenizer,
    TinyCTC,
    decode_offline,
    decode_streaming,
    features,
    load_checkpoint,
    read_wav,
    save_checkpoint,
)
from cadence_toy.steps.average import AverageStep, checkpoint_inputs
from cadence_toy.steps.train import TrainParams, TrainStep
from cadence_toy.steps.transcribe import TranscribeParams, TranscribeStep
from cadence_worker.cas import Store
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_toy" / "fixtures"


@pytest.fixture
def dataset(tmp_path: Path) -> tuple[Path, Store]:
    """The fixtures imported by dataset_import (folder-csv) into the directory artifact the steps read."""
    params = DatasetImportParams(
        format="folder-csv",
        path=str(FIXTURES),
        source_name="fixtures",
        licence="CC0-1.0",
        locale="und",
        split_rule="source",
    )
    out = tmp_path / "dataset"
    write_dataset(params, records(params), out)
    return out, Store(tmp_path / "cas")


def context(tmp: Path, store: Store, events: list[dict[str, Any]]) -> StepContext:
    return StepContext(events.append, work_dir=tmp, blob_path=store.path)


def test_fixtures_are_small_and_licensed() -> None:
    assert sum(p.stat().st_size for p in FIXTURES.iterdir()) < 1_000_000
    assert "CC0" in (FIXTURES / "SOURCE.md").read_text()


def test_tokenizer_round_trip() -> None:
    tok = CharTokenizer()
    assert tok.normalise("Dead,  BEEF!") == "dead beef"
    assert tok.decode(tok.encode("a bad cab")) == "a bad cab"


def test_features_are_frame_local() -> None:
    s = read_wav(FIXTURES / "clip01.wav")
    f = features(s)
    assert f.shape[1] == 80
    assert f.shape[0] == pytest.approx(s.numel() / 16000 * 1000 / FRAME_MS, abs=2)


def test_streaming_decode_equals_offline_and_reports_partials() -> None:
    torch.manual_seed(0)
    model, tok = TinyCTC().eval(), CharTokenizer()
    s = read_wav(FIXTURES / "clip03.wav")
    off = decode_offline(model, tok, s)
    dec, partials = decode_streaming(model, tok, s, 320)
    assert dec.text() == off.text()
    offsets = [p["audioOffsetMs"] for p in partials]
    assert offsets == sorted(offsets)
    assert offsets[0] == 320
    assert partials[-1]["final"] is True
    assert partials[-1]["text"] == dec.text()


def test_family_profiles_and_roles() -> None:
    d = FAMILY.descriptor
    assert FAMILY.runtime == "toy"
    assert d["defaultsSection"] == "packs.toy"
    assert [p["name"] for p in PROFILES] == ["offline", "320ms"]
    assert set(d["roles"]) == {"calibrate", "train", "average", "transcribe"}


def test_train_average_transcribe_in_process(tmp_path: Path, dataset: tuple[Path, Store]) -> None:
    data, store = dataset
    events: list[dict[str, Any]] = []
    ctx = context(tmp_path, store, events)
    outs = {"checkpoint": tmp_path / "ck", "state": tmp_path / "st"}
    TrainStep().run(TrainParams(steps=4, val_every=2), {"data": data}, outs, ctx)
    meta = ctx.meta["checkpoint"]
    assert meta["family"] == "toy-ctc"
    assert meta["step"] == 4
    assert 0 <= meta["valWer"] <= 1
    assert meta["weightsHash"].startswith("b3:")
    assert sorted(p.name for p in (tmp_path / "ck").iterdir()) == ["config.json", "model.pt", "tokenizer.json"]
    assert [e["step"] for e in events if e["e"] == "metric" and e["name"] == "val_wer"] == [2, 4]

    # Resume continues to the new total from the training state.
    ctx2 = StepContext(events.append, work_dir=tmp_path, blob_path=store.path, resume_from=tmp_path / "st")
    outs2 = {"checkpoint": tmp_path / "ck2", "state": tmp_path / "st2"}
    TrainStep().run(TrainParams(steps=6, val_every=100), {"data": data}, outs2, ctx2)
    assert ctx2.meta["checkpoint"]["step"] == 6

    actx = context(tmp_path, store, events)
    AverageStep().run(
        AverageStep.Params(),
        {"checkpoints.1": tmp_path / "ck2", "checkpoints.0": tmp_path / "ck"},
        {"checkpoint": tmp_path / "avg"},
        actx,
    )
    avg, _ = load_checkpoint(tmp_path / "avg")
    a, _ = load_checkpoint(tmp_path / "ck")
    b, _ = load_checkpoint(tmp_path / "ck2")
    for k, v in avg.state_dict().items():
        assert torch.allclose(v, (a.state_dict()[k] + b.state_dict()[k]) / 2, atol=1e-6)
    assert actx.meta["checkpoint"]["step"] == 6
    assert len(actx.meta["checkpoint"]["averagedFrom"]) == 2

    tctx = context(tmp_path, store, events)
    TranscribeStep().run(
        TranscribeParams(profile="320ms"),
        {"model": tmp_path / "avg", "data": data},
        {"hypotheses": tmp_path / "h.jsonl"},
        tctx,
    )
    rows = [json.loads(x) for x in (tmp_path / "h.jsonl").read_text().splitlines()]
    assert len(rows) == 8
    assert rows[0]["decoding"]["profile"] == "320ms"
    assert rows[0]["partials"]
    assert rows[0]["weightsHash"] == actx.meta["checkpoint"]["weightsHash"]
    with pytest.raises(StepInputError):
        TranscribeStep().run(
            TranscribeParams(profile="80ms"),
            {"model": tmp_path / "avg", "data": data},
            {"hypotheses": tmp_path / "x"},
            tctx,
        )


def test_the_training_state_of_another_family_is_refused(tmp_path: Path, dataset: tuple[Path, Store]) -> None:
    data, store = dataset
    st = tmp_path / "st"
    st.mkdir()
    (st / "state.json").write_text(json.dumps({"family": "other", "step": 1}))
    ctx = StepContext(lambda e: None, work_dir=tmp_path, blob_path=store.path, resume_from=st)
    with pytest.raises(StepInputError, match="belongs to"):
        TrainStep().run(
            TrainParams(steps=2), {"data": data}, {"checkpoint": tmp_path / "c", "state": tmp_path / "s"}, ctx
        )


def test_averaging_needs_two(tmp_path: Path) -> None:
    save_checkpoint(tmp_path / "one", TinyCTC(), CharTokenizer(), 1)
    assert checkpoint_inputs({"checkpoints.10": Path("b"), "checkpoints.2": Path("a"), "data": Path("x")}) == [
        Path("a"),
        Path("b"),
    ]
    with pytest.raises(StepInputError):
        AverageStep().run(
            AverageStep.Params(),
            {"checkpoints.0": tmp_path / "one"},
            {"checkpoint": tmp_path / "o"},
            StepContext(lambda e: None, work_dir=tmp_path),
        )


def test_the_dataset_is_read_from_the_import_directory(dataset: tuple[Path, Store]) -> None:
    data, _ = dataset
    utts = read_dataset(data)
    assert [u.text for u in utts][:2] == ["a bad cab", "dead beef"]
    assert all(u.audio.startswith("b3:") and u.split == "train" for u in utts)


def test_a_dataset_without_utterances_is_an_input_error(tmp_path: Path) -> None:
    (tmp_path / "dataset.json").write_text('{"format": "cadence.dataset/1"}')
    (tmp_path / "manifest.jsonl").write_text("")
    with pytest.raises(StepInputError, match="no utterances"):
        read_dataset(tmp_path)


def test_a_dataset_file_or_escaping_path_is_an_input_error(tmp_path: Path) -> None:
    f = tmp_path / "d.jsonl"
    f.write_text('{"audio": "b3:00", "text": "a"}\n')
    with pytest.raises(StepInputError, match="directory"):
        read_dataset(f)
    d = tmp_path / "d"
    d.mkdir()
    (d / "dataset.json").write_text('{"format": "cadence.dataset/1"}')
    (d / "manifest.jsonl").write_text('{"audio": "../x.wav", "text": "a"}\n')
    with pytest.raises(StepInputError, match="inside the artifact"):
        read_dataset(d)
