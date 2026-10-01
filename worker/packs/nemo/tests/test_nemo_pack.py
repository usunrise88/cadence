"""The NeMo pack without NeMo: schemas, the family descriptor, the pure parts of every step (mix reading, Noam
arithmetic, averaging on tiny tensors, augmentation, the OOMptimizer search, the training monitor, hypotheses) and the
schema half of the conformance suite. The flow itself runs in the NeMo image on a card (nightly)."""

from __future__ import annotations

import io
import json
import tarfile
from pathlib import Path
from typing import Any

import numpy as np
import pytest
import torch

from cadence_nemo import augment, lang, noam, oomptimizer, valwer
from cadence_nemo import checkpoint as ck
from cadence_nemo.family import FAMILY, NAME, PROFILES, att_context_size, profile
from cadence_nemo.mixdata import input_cfg, nemo_rows, read_training_data
from cadence_nemo.monitor import TrainingMonitor
from cadence_nemo.steps.average import AverageStep, checkpoint_inputs
from cadence_nemo.steps.calibrate import CalibrateParams, CalibrateStep, calibration_doc, lease_overhead
from cadence_nemo.steps.finetune import (
    FinetuneParams,
    FinetuneStep,
    fit_buckets,
    read_calibration,
    validation_clips,
)
from cadence_nemo.steps.transcribe import TranscribeParams, TranscribeStep, decoding_hash, hypothesis_row
from cadence_nemo.streaming import Stream, chunk_frames, record_partials, word_confidence, words_from_partials
from cadence_nemo.training import ModelFacts, optim_config, scaled_batches, train_ds_config, val_ds_config
from cadence_worker.cas import Store
from cadence_worker.conformance.suite import HYPOTHESIS_FIELDS, REPO_HELP, check_schemas
from cadence_worker.registry import load_families, load_kinds
from cadence_worker.steps.base import StepInputError, check_ranges, missing_metadata
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"
KINDS = (CalibrateStep, FinetuneStep, AverageStep, TranscribeStep)
PROMPTS = {"en-US": 0, "en": 0, "he-IL": 64, "fr-FR": 8, "fr-CA": 100, "auto": 101}


def ctx(tmp: Path, events: list[dict[str, Any]], store: Store | None = None) -> StepContext:
    return StepContext(events.append, work_dir=tmp, blob_path=store.path if store else None)


# ---------------------------------------------------------------- schemas and family


def test_every_kind_has_complete_x_cadence_and_valid_defaults() -> None:
    for kind in KINDS:
        assert missing_metadata(kind) == [], kind.__name__
        check_ranges(kind.Params())
    assert FinetuneParams().precision == "bf16"
    assert FinetuneParams().augmentation["profile"] == "telephony"
    assert TranscribeParams().profile == "160ms"
    assert CalibrateParams().bucket_bins[-1] == 20


def test_kinds_declare_their_role_runtime_and_card() -> None:
    assert {k.role for k in KINDS} == {"calibrate", "train", "average", "transcribe"}
    assert all(k.runtime == "nemo-speech" for k in KINDS)
    assert FinetuneStep.resources["gpu"]
    assert FinetuneStep.resources["gpus"] == 1
    assert not AverageStep.resources["gpu"]
    # The card kinds that size themselves to the lease's cap reserve the whole remaining cap (no memoryGb): a declared
    # reservation above a card's cap (the staging card's is 22 GB) would wait in the queue for ever.
    assert "memoryGb" not in FinetuneStep.resources
    assert "memoryGb" not in CalibrateStep.resources
    assert FinetuneStep.consumes == {"base": "base_model", "data": "mix", "calibration": "calibration"}
    assert set(FinetuneStep.produces.values()) == {"checkpoint", "training-state"}


def test_family_descriptor_profiles() -> None:
    d = FAMILY.descriptor
    assert d["name"] == NAME == "nemo.fastconformer-rnnt.cache-aware"
    assert [p["name"] for p in PROFILES] == ["80ms", "160ms", "320ms", "560ms", "1120ms"]
    p160 = profile("160ms")
    assert p160["label"] == "160 ms · [56,1]"
    assert p160["leftContextMs"] == 4480
    assert p160["latencyMs"] == 160 == p160.get("chunkMs")
    assert att_context_size(profile("1120ms")) == [56, 13]
    assert d["capabilities"]["trainModes"] == ["finetune"]
    assert d["capabilities"]["languagePrompt"] is True
    assert d["tokenizer"] == "sentencepiece"
    assert d["input"] == {"sampleRate": 16000, "channels": 1}


def test_base_model_fixture_names_this_family() -> None:
    fixture = Path(__file__).resolve().parents[4] / "control-plane/internal/registry/fixtures/base-models.yaml"
    assert f"familyId: {NAME}" in fixture.read_text(encoding="utf-8")


def test_conformance_schemas_pass_for_the_nemo_runtime() -> None:
    kinds = load_kinds("nemo-speech")
    families = load_families("nemo-speech")
    assert check_schemas("nemo-speech", kinds, families, REPO_HELP) == []


def test_fixtures_are_small_and_licensed() -> None:
    assert sum(p.stat().st_size for p in FIXTURES.iterdir()) < 2_000_000
    assert "CC-BY-4.0" in (FIXTURES / "SOURCE.md").read_text(encoding="utf-8")


# ---------------------------------------------------------------- language prompt and Noam


def test_prompt_keys() -> None:
    assert lang.resolve_prompt_key("he-IL", PROMPTS) == "he-IL"
    assert lang.resolve_prompt_key("he", PROMPTS) == "he-IL"
    assert lang.resolve_prompt_key("HE-il", PROMPTS) == "he-IL"
    assert lang.resolve_prompt_key("en", PROMPTS) == "en"
    assert lang.resolve_prompt_key("fr", PROMPTS) == "fr-CA"  # first by sort among the primary subtag's keys
    with pytest.raises(StepInputError):
        lang.resolve_prompt_key("und", PROMPTS)
    with pytest.raises(StepInputError):
        lang.resolve_prompt_key("xx-YY", PROMPTS)
    assert lang.prompt_keys(["he", "he-IL"], PROMPTS) == {"he": "he-IL", "he-IL": "he-IL"}
    assert lang.prompt_keys(["und"], PROMPTS, override="he-IL") == {"und": "he-IL"}


def test_language_tag() -> None:
    assert lang.with_tag("שלום.", "he-IL") == "שלום. <he-IL>"
    assert lang.with_tag("שלום. <he-IL>", "he-IL") == "שלום. <he-IL>"
    assert lang.strip_tags("hello <en-US> world <auto>") == "hello world"


def test_noam_scale_from_peak() -> None:
    assert noam.peak_for_scale(0.1, 1024, 100) == pytest.approx(3.125e-4)  # spike A3
    scale = noam.scale_for_peak(2e-4, 1024, 100)
    assert noam.lr_at(100, scale, 1024, 100) == pytest.approx(2e-4)
    assert noam.lr_at(50, scale, 1024, 100) < 2e-4
    assert noam.lr_at(400, scale, 1024, 100) == pytest.approx(1e-4)
    assert noam.lr_at(10**9, scale, 1024, 100, min_lr=1e-6) == 1e-6
    with pytest.raises(ValueError, match="positive"):
        noam.scale_for_peak(0, 1024, 100)


# ---------------------------------------------------------------- data


@pytest.fixture
def mix(tmp_path: Path) -> tuple[Path, Store]:
    """The fixtures imported by dataset_import, stored, and a two-group mix over them (as runs.RenderMix renders)."""
    params = DatasetImportParams(
        format="folder-csv",
        path=str(FIXTURES),
        source_name="fleurs-he-fixtures",
        licence="CC-BY-4.0",
        locale="he-IL",
        split_rule="source",
    )
    out = tmp_path / "dataset"
    write_dataset(params, records(params), out)
    store = Store(tmp_path / "cas")
    h = store.put_path(out).hash
    doc = {
        "format": "cadence.mix/1",
        "mix": {"id": "mix_1", "name": "m", "revision": 3},
        "input_cfg": [
            {
                "type": "group",
                "name": "target",
                "probability": 3,
                "input_cfg": [{"type": "dataset", "dataset": "dsv_a", "artifact": h, "hours": 0.5}],
            },
            {
                "type": "group",
                "name": "replay",
                "replay": True,
                "probability": 1,
                "input_cfg": [{"type": "dataset", "dataset": "dsv_b", "artifact": h, "hours": 0}],
            },
        ],
    }
    path = tmp_path / "mix.json"
    path.write_text(json.dumps(doc), encoding="utf-8")
    return path, store


def test_read_mix_from_the_store(mix: tuple[Path, Store], tmp_path: Path) -> None:
    path, store = mix
    data = read_training_data(path, store.path)
    assert data.mix["revision"] == 3
    assert [g.probability for g in data.groups] == [0.75, 0.25]
    assert data.dataset_ids() == ["dsv_a", "dsv_b"]
    assert len(data.train_clips()) == 16
    assert len(data.validation_clips()) == 4
    assert data.languages() == {"he-IL"}
    assert all(c.audio.is_file() and str(c.audio).startswith(str(store.root)) for c in data.clips())
    cfg = input_cfg(
        data, tmp_path / "m", lambda c: lang.with_tag(c.text, "he-IL"), lambda c: "he-IL", lambda c: c.split == "train"
    )
    assert [g["weight"] for g in cfg] == [0.75, 0.25]
    assert cfg[0]["input_cfg"][0]["weight"] == 0.5
    assert cfg[1]["input_cfg"][0]["weight"] == pytest.approx(data.groups[1].datasets[0].hours)  # the header's hours
    assert data.groups[1].datasets[0].hours > 0
    line = json.loads(Path(cfg[0]["input_cfg"][0]["manifest_filepath"]).read_text(encoding="utf-8").splitlines()[0])
    assert line["target_lang"] == "he-IL"
    assert line["text"].endswith(" <he-IL>")
    assert line["duration"] > 2


def test_bad_mix_inputs(tmp_path: Path) -> None:
    p = tmp_path / "x.json"
    p.write_text(json.dumps({"format": "other"}), encoding="utf-8")
    with pytest.raises(StepInputError, match=r"cadence\.mix/1"):
        read_training_data(p, lambda h: tmp_path / h)
    p.write_text(
        json.dumps(
            {"format": "cadence.mix/1", "input_cfg": [{"input_cfg": [{"dataset": "d", "artifact": "b3:" + "0" * 64}]}]}
        ),
        encoding="utf-8",
    )
    with pytest.raises(StepInputError, match="content store"):
        read_training_data(p, lambda h: tmp_path / h)


def test_validation_falls_back_to_training_clips(mix: tuple[Path, Store]) -> None:
    path, store = mix
    data = read_training_data(path, store.path)
    assert len(validation_clips(data, 3, 0.5, 20)) == 3
    for g in data.groups:
        for d in g.datasets:
            d.clips = [c for c in d.clips if c.split == "train"]
    assert validation_clips(data, 100, 0.5, 20)[0].split == "train"
    rows = nemo_rows(data.clips()[:1], lambda c: c.text, lambda c: "he-IL")
    assert set(rows[0]) == {"audio_filepath", "duration", "text", "lang", "target_lang"}


# ---------------------------------------------------------------- calibration and training configuration


def test_calibration_round_trip(tmp_path: Path) -> None:
    buckets = [oomptimizer.Bucket(4, 37), oomptimizer.Bucket(8, 11), oomptimizer.Bucket(20, 1)]
    doc = calibration_doc(
        buckets=buckets,
        requested=[4, 6, 8, 20],
        ratio=16,
        seconds=[0.6, 0.7, 0.8],
        audio=[50, 55, 60],
        sizes=[8, 9, 10],
        precision="bf16",
        applied={"memoryCapMb": 22017, "allocatorCapMb": 20993, "device": "card"},
        peak={"maxReservedMb": 20000},
        load_s=62.0,
        nemo_save_s=12.0,
    )
    assert doc["secondsPerStep"] == pytest.approx(0.7)
    # Load + the last .nemo save + a training state of about three .nemo saves.
    assert doc["leaseOverheadSeconds"] == pytest.approx(62 + 12 * 4)
    assert lease_overhead(-1, 0) == 0
    assert doc["plusMinus"] == pytest.approx(2 * doc["secondsPerStepStd"] / 0.7 / 3**0.5, rel=1e-2)
    assert doc["batchSizes"] == {"bucket_duration_bins": [4, 8, 20], "bucket_batch_size": [37, 11, 1]}
    assert doc["bucketConfig"]["tokensPerSecond"] == 16
    assert doc["batchSize"] == 9
    p = tmp_path / "cal.json"
    p.write_text(json.dumps(doc), encoding="utf-8")
    assert read_calibration(p) == ([4.0, 8.0, 20.0], [37, 11, 1])
    p.write_text("{}", encoding="utf-8")
    with pytest.raises(StepInputError):
        read_calibration(p)


def test_fit_buckets_and_scaling() -> None:
    bins, batches = [4.0, 8.0, 14.0, 20.0], [37, 11, 3, 1]
    assert fit_buckets(bins, batches, 20) == (bins, batches, 20.0)
    assert fit_buckets(bins, batches, 10) == ([4.0, 8.0, 10.0], [37, 11, 3], 10.0)
    assert fit_buckets(bins, batches, 8) == ([4.0, 8.0], [37, 11], 8.0)
    assert fit_buckets(bins, batches, 3) == ([3.0], [37], 3.0)
    assert scaled_batches([37, 11, 1], 0.75, 0.95) == [26, 7, 1]
    assert scaled_batches([37], 1.0, 1.05) == [37]


FACTS = ModelFacts(
    prompt_dictionary=PROMPTS, num_prompts=128, subsampling_factor=8, d_model=1024, sample_rate=16000, vocab_size=13088
)


def test_data_and_optimiser_configs(tmp_path: Path) -> None:
    base = {"is_tarred": True, "batch_duration": 200, "num_workers": 8, "prompt_dictionary": {}, "max_duration": 20}
    cfg = train_ds_config(
        base,
        input_cfg=[{"type": "group"}],
        bins=[4, 8],
        batches=[10, 3],
        max_duration=8,
        min_duration=0.5,
        num_workers=4,
        seed=7,
        prompt_mode="unified",
        auto_ratio=0.5,
        facts=FACTS,
    )
    assert cfg["is_tarred"] is False
    assert cfg["batch_duration"] is None
    assert cfg["use_bucketing"]
    assert cfg["bucket_duration_bins"] == [4.0, 8.0]
    assert cfg["bucket_batch_size"] == [10, 3]
    assert cfg["prompt_dictionary"]["he-IL"] == 64
    assert cfg["lang_field"] == "target_lang"
    assert cfg["seed"] == 7
    with pytest.raises(StepInputError):
        train_ds_config(
            base,
            input_cfg=[],
            bins=[4],
            batches=[],
            max_duration=8,
            min_duration=0.5,
            num_workers=0,
            seed=0,
            prompt_mode="unified",
            auto_ratio=0.5,
            facts=FACTS,
        )
    val = val_ds_config({}, manifest=tmp_path / "v.json", batch_size=8, num_workers=2, facts=FACTS)
    assert val["default_prompt_mode"] == "langID"
    assert not val["use_bucketing"]
    opt = optim_config(
        {"name": "adamw", "lr": 0.5, "betas": [0.9, 0.98]},
        scale=0.128,
        warmup_steps=100,
        min_lr=1e-6,
        weight_decay=1e-3,
        d_model=1024,
    )
    assert opt["lr"] == 0.128
    assert opt["sched"]["name"] == "NoamAnnealing"
    assert opt["sched"]["warmup_steps"] == 100


# ---------------------------------------------------------------- OOMptimizer search


def test_batch_search_converges_on_capacity() -> None:
    s = oomptimizer.BatchSearch(start=16, threshold=0.05)
    tried = []
    while True:
        tried.append(s.current)
        if s.advance(s.current > 13):
            break
    assert s.result == 13
    assert tried[:2] == [16, 8]


def test_batch_search_stops_when_nothing_fits() -> None:
    s = oomptimizer.BatchSearch(start=4)
    n = 0
    while not s.advance(True):
        n += 1
        assert n < 10
    assert s.result == 0


def test_search_buckets_and_merge() -> None:
    capacity = {4.0: 37, 8.0: 11, 12.0: 4, 16.0: 1, 20.0: 1, 24.0: 0}
    found = oomptimizer.search_buckets(list(capacity), 14, lambda b, sec, tok: b > capacity[sec], start=16)
    assert {b.max_duration: b.batch_size for b in found} == capacity
    merged = oomptimizer.merge_buckets(found)
    assert [(b.max_duration, b.batch_size) for b in merged] == [(4.0, 37), (8.0, 11), (12.0, 4), (20.0, 1)]


def test_tokens_per_second_quantile() -> None:
    assert oomptimizer.tokens_per_second([(10, 1.0)] * 99 + [(40, 1.0)]) == 10
    assert oomptimizer.tokens_per_second([(10, 1.0), (40, 1.0)], 0.99) == 40
    assert oomptimizer.tokens_per_second([]) is None
    assert oomptimizer.is_oom_error(RuntimeError("cuFFT error: CUFFT_INVALID_SIZE"))
    assert not oomptimizer.is_oom_error(RuntimeError("shape mismatch"))


# ---------------------------------------------------------------- averaging and checkpoints


def fake_nemo(path: Path, weights: dict[str, torch.Tensor], config: bytes = b"model: x\n") -> None:
    buf = io.BytesIO()
    torch.save(weights, buf)
    with tarfile.open(path, "w:") as tar:
        for name, data in (
            ("./model_config.yaml", config),
            ("./model_weights.ckpt", buf.getvalue()),
            ("./abc_tokenizer.model", b"spm"),
        ):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))


def read_weights(path: Path) -> dict[str, torch.Tensor]:
    with tarfile.open(path) as tar:
        f = tar.extractfile("./model_weights.ckpt")
        assert f is not None
        state: dict[str, torch.Tensor] = torch.load(io.BytesIO(f.read()), weights_only=True)
        return state


def test_average_nemo_is_the_elementwise_mean(tmp_path: Path) -> None:
    a = {"w": torch.tensor([1.0, 2.0]), "h": torch.tensor([1.0], dtype=torch.bfloat16), "n": torch.tensor(3)}
    b = {"w": torch.tensor([3.0, 6.0]), "h": torch.tensor([3.0], dtype=torch.bfloat16), "n": torch.tensor(9)}
    fake_nemo(tmp_path / "a.nemo", a)
    fake_nemo(tmp_path / "b.nemo", b)
    ck.average_nemo([tmp_path / "a.nemo", tmp_path / "b.nemo"], tmp_path / "out.nemo")
    out = read_weights(tmp_path / "out.nemo")
    assert torch.equal(out["w"], torch.tensor([2.0, 4.0]))
    assert out["h"].dtype == torch.bfloat16
    assert float(out["h"]) == 2.0
    assert int(out["n"]) == 3  # integer buffers come from the first checkpoint
    with tarfile.open(tmp_path / "out.nemo") as tar:
        assert sorted(m.name for m in tar.getmembers()) == [
            "./abc_tokenizer.model",
            "./model_config.yaml",
            "./model_weights.ckpt",
        ]


def test_average_refuses_mismatched_checkpoints(tmp_path: Path) -> None:
    fake_nemo(tmp_path / "a.nemo", {"w": torch.zeros(2)})
    fake_nemo(tmp_path / "b.nemo", {"w": torch.zeros(2)}, config=b"model: y\n")
    fake_nemo(tmp_path / "c.nemo", {"v": torch.zeros(2)})
    with pytest.raises(StepInputError, match="configuration"):
        ck.average_nemo([tmp_path / "a.nemo", tmp_path / "b.nemo"], tmp_path / "o.nemo")
    with pytest.raises(StepInputError, match="tensors"):
        ck.average_nemo([tmp_path / "a.nemo", tmp_path / "c.nemo"], tmp_path / "o.nemo")
    with pytest.raises(StepInputError, match="two"):
        ck.average_nemo([tmp_path / "a.nemo"], tmp_path / "o.nemo")


def make_checkpoint(d: Path, weights: dict[str, torch.Tensor], step: int) -> None:
    d.mkdir(parents=True)
    fake_nemo(d / ck.NEMO_FILE, weights)
    ck.write_checkpoint(d, {"step": step, "valWer": 0.5, "base": {"hfRepo": "r", "revision": "v"}})


def test_average_step(tmp_path: Path) -> None:
    make_checkpoint(tmp_path / "c0", {"w": torch.ones(2)}, 6)
    make_checkpoint(tmp_path / "c1", {"w": torch.full((2,), 3.0)}, 9)
    events: list[dict[str, Any]] = []
    c = ctx(tmp_path, events)
    inputs = {"checkpoints.1": tmp_path / "c1", "checkpoints.0": tmp_path / "c0"}
    assert checkpoint_inputs(inputs) == [tmp_path / "c0", tmp_path / "c1"]
    AverageStep().run(AverageStep.Params(), inputs, {"checkpoint": tmp_path / "avg"}, c)
    meta = c.meta["checkpoint"]
    assert meta["family"] == NAME
    assert meta["step"] == 9
    assert meta["weightsHash"].startswith("b3:")
    assert len(meta["averagedFrom"]) == 2
    assert torch.equal(read_weights(tmp_path / "avg" / ck.NEMO_FILE)["w"], torch.full((2,), 2.0))


def test_read_base_model_and_checkpoint(tmp_path: Path) -> None:
    doc = {
        "format": "cadence.base_model/1",
        "versionId": "bmv_1",
        "family": {"name": NAME},
        "model": {"hfRepo": "nvidia/x", "revision": "abc", "checkpointFile": "x.nemo"},
    }
    p = tmp_path / "base.json"
    p.write_text(json.dumps(doc), encoding="utf-8")
    got: list[dict[str, Any]] = []

    def download(m: Any) -> Path:
        got.append(dict(m))
        return tmp_path / "x.nemo"

    base = ck.read_base(p, download)
    assert base.kind == "base_model"
    assert base.nemo == tmp_path / "x.nemo"
    assert got[0]["revision"] == "abc"
    assert base.reference["versionId"] == "bmv_1"
    assert base.tokenizer["kind"] == "sentencepiece"
    make_checkpoint(tmp_path / "ck", {"w": torch.ones(1)}, 5)
    b2 = ck.read_base(tmp_path / "ck", download)
    assert b2.kind == "checkpoint"
    assert b2.step == 5
    assert b2.reference["hfRepo"] == "r"
    p.write_text(json.dumps({**doc, "family": {"name": "toy-ctc"}}), encoding="utf-8")
    with pytest.raises(StepInputError, match="family"):
        ck.read_base(p, download)


def test_training_state_json(tmp_path: Path) -> None:
    ck.write_state_json(tmp_path, {"step": 12, "seed": 0})
    (tmp_path / ck.STATE_CKPT).write_bytes(b"x")
    assert ck.read_state(tmp_path)["step"] == 12
    (tmp_path / ck.STATE_JSON).write_text(json.dumps({"family": "toy-ctc", "step": 1}), encoding="utf-8")
    with pytest.raises(StepInputError):
        ck.read_state(tmp_path)


def test_link_checkpoint_keeps_the_same_content(tmp_path: Path) -> None:
    make_checkpoint(tmp_path / "a", {"w": torch.ones(1)}, 1)
    ck.link_checkpoint(tmp_path / "a", tmp_path / "b")
    store = Store(tmp_path / "cas")
    assert store.put_path(tmp_path / "a").hash == store.put_path(tmp_path / "b").hash


class FakeHypothesis:
    def __init__(self, text: str) -> None:
        self.text = text


class FakeDecoding:
    """Characters as token ids; anything outside the vocabulary decodes as ⁇ (as a SentencePiece unk does)."""

    vocab = " abcdefghijklmnopqrstuvwxyz.,"

    def ids(self, text: str) -> list[int]:
        return [self.vocab.index(c) + 1 if c in self.vocab else 0 for c in text]

    def decode_ids_to_str(self, ids: list[int]) -> str:
        return "".join(self.vocab[i - 1] if i > 0 else "⁇" for i in ids)


class FakeWER:
    def __init__(self, hypotheses: list[str]) -> None:
        self.decoding = FakeDecoding()
        self.batch_dim_index = 0
        self.scores = torch.tensor(0)
        self.words = torch.tensor(0)
        self._hyps = hypotheses
        self.wrapped = 0
        self.hypotheses: list[FakeHypothesis] = []
        self.update: Any = None  # valwer.install sets it

    def decode(self, *args: Any) -> list[FakeHypothesis]:
        return [FakeHypothesis(h) for h in self._hyps]

    def _wrap_update(self, fn: Any) -> Any:
        def wrapped(*a: Any, **kw: Any) -> Any:
            self.wrapped += 1
            return fn(*a, **kw)

        return wrapped


def test_validation_wer_scores_raw_normalised_references() -> None:
    raw = ["Shalom (world).", "Good day, Ëve"]
    dec = FakeDecoding()
    refs = valwer.RawReferences(raw, lambda t: dec.decode_ids_to_str(dec.ids(t)))
    assert len(refs) == 2
    assert refs.reference(dec.decode_ids_to_str(dec.ids("Shalom (world)."))) == "Shalom (world)."
    assert refs.reference("not in the manifest") == "not in the manifest"
    assert valwer.counts("shalom world", "Shalom (world).") == (0, 2)
    assert valwer.counts("good day eve", "Good day, Ëve") == (1, 3)  # ë is not e
    assert valwer.counts("ab", "abc", use_cer=True) == (1, 3)

    metric = FakeWER(["shalom world", "good day eve"])
    valwer.install(metric, refs)
    targets = [dec.ids(t) for t in raw]
    width = max(len(t) for t in targets)
    padded = torch.tensor([t + [0] * (width - len(t)) for t in targets])
    metric.update(
        predictions=torch.zeros(2, 3),
        predictions_lengths=torch.tensor([3, 3]),
        targets=padded,
        targets_lengths=torch.tensor([len(t) for t in targets]),
    )
    # Tokenized, the references would read "shalom ⁇world⁇." and "good day, ⁇ve": two words wrong, not one.
    assert (int(metric.scores), int(metric.words)) == (1, 5)
    assert metric.wrapped == 1
    assert [h.text for h in metric.hypotheses] == ["shalom world", "good day eve"]


def test_validation_checkpoints_are_published_and_the_best_is_linked(tmp_path: Path) -> None:
    events: list[dict[str, Any]] = []
    c = ctx(tmp_path, events)
    best = tmp_path / "best"
    lineage = {"base": {"hfRepo": "r", "revision": "v"}, "tokenizer": {"kind": "spe"}}
    for step, wer, is_best in ((200, 0.40, True), (400, 0.42, False)):
        d = tmp_path / "published" / f"checkpoint-{step}"
        d.mkdir(parents=True)
        fake_nemo(d / ck.NEMO_FILE, {"w": torch.full((2,), float(step))})
        doc = ck.publish_validation(c, d, {"step": step, **lineage}, wer, best if is_best else None)
        assert (doc["step"], doc["valWer"], doc["family"]) == (step, wer, NAME)
    published = [e for e in events if e["e"] == "publish"]
    assert [e["output"] for e in published] == ["checkpoint", "checkpoint"]
    assert published[0]["meta"] == {
        "family": NAME,
        "step": 200,
        "valWer": 0.40,
        "weightsHash": ck.weights_hash(tmp_path / "published" / "checkpoint-200" / ck.NEMO_FILE),
        "base": lineage["base"],
        "tokenizer": lineage["tokenizer"],
    }
    assert published[1]["metrics"] == {"val_wer": 0.42}
    # The best link is the step-200 checkpoint: linked as checkpoint_best it is the same artifact.
    assert ck.read_checkpoint(best)["step"] == 200
    store = Store(tmp_path / "cas")
    ck.link_checkpoint(best, tmp_path / "checkpoint_best")
    assert store.put_path(tmp_path / "checkpoint_best").hash == store.put_path(Path(published[0]["path"])).hash


# ---------------------------------------------------------------- augmentation


def tone(seconds: float = 1.0, sr: int = 16000) -> np.ndarray:
    t = np.arange(int(seconds * sr)) / sr
    return (0.3 * np.sin(2 * np.pi * 440 * t)).astype(np.float32)


def test_profiles() -> None:
    p = augment.parse_profile(FinetuneParams().augmentation)
    assert p.enabled
    assert p.min_speed == 0.95
    assert not augment.parse_profile({"profile": "clean"}).enabled
    with pytest.raises(StepInputError, match="ffmpeg"):
        augment.parse_profile({"telephone": {"p": 1, "codecs": ["amr"]}})
    with pytest.raises(StepInputError):
        augment.parse_profile({"gain": {"p": 1, "min_db": 3, "max_db": -3}})
    with pytest.raises(StepInputError):
        augment.parse_profile({"echo": {}})


def test_transforms() -> None:
    x = tone()
    for f in (augment.mulaw, augment.alaw):
        y = f(x)
        assert y.shape == x.shape
        assert float(np.max(np.abs(y - x))) < 0.02
    low = augment.resample(x, 16000, 8000)
    assert low.size == 8000
    back = augment.resample(low, 8000, 16000)
    assert back.size == 16000
    assert float(np.corrcoef(back, x)[0, 1]) > 0.99
    noise = np.random.default_rng(0).standard_normal(16000).astype(np.float32) * 0.1
    limited = augment.resample(augment.resample(noise, 16000, 8000), 8000, 16000)
    spec = np.abs(np.fft.rfft(limited))
    assert spec[4100:].sum() < 1e-3 * spec.sum()  # nothing above 4 kHz after band-limiting
    assert augment.speed(x, 1.1).size == round(16000 / 1.1)


def test_apply_is_seeded_and_keeps_length_without_speed() -> None:
    prof = augment.parse_profile(
        {"telephone": {"p": 1, "codecs": ["ulaw", "alaw"]}, "gain": {"p": 1, "min_db": -6, "max_db": 6}}
    )
    x = tone()
    y1, a1 = augment.apply(x, 16000, prof, np.random.default_rng(3))
    y2, a2 = augment.apply(x, 16000, prof, np.random.default_rng(3))
    assert a1 == a2
    assert np.array_equal(y1, y2)
    assert y1.size == x.size
    assert any(s.startswith("codec:") for s in a1)
    assert "bandlimit:8000" in a1
    y3, a3 = augment.apply(x, 16000, augment.parse_profile({"profile": "clean"}), np.random.default_rng(3))
    assert a3 == []
    assert y3 is x


# ---------------------------------------------------------------- training monitor (stop, states, metrics)


class Clock:
    def __init__(self) -> None:
        self.t = 0.0

    def __call__(self) -> float:
        return self.t


def monitor(tmp: Path, events: list[dict[str, Any]], clock: Clock, **kw: Any) -> TrainingMonitor:
    return TrainingMonitor(ctx(tmp, events), total_steps=10, log_every=5, state_every_s=1200, clock=clock, **kw)


def test_monitor_metric_cadence_and_throughput(tmp_path: Path) -> None:
    events: list[dict[str, Any]] = []
    clock = Clock()
    m = monitor(tmp_path, events, clock)
    for step in range(1, 11):
        m.batch_audio(50.0)
        clock.t += 0.5
        m.step_end(step, 100.0 - step, 1e-4, 2.0, 20000.0)
    names = [(e["name"], e["step"]) for e in events if e["e"] == "metric"]
    assert {s for n, s in names if n == "loss"} == {1, 5, 10}
    tp = [e["value"] for e in events if e.get("name") == "throughput_audio_s_per_s"]
    assert tp == [pytest.approx(100.0)] * 3
    assert {n for n, _ in names} == {"loss", "lr", "grad_norm", "throughput_audio_s_per_s", "gpu_memory_mb"}
    assert [e for e in events if e["e"] == "progress"][-1]["fraction"] == 1.0


def test_monitor_best_validation(tmp_path: Path) -> None:
    m = monitor(tmp_path, [], Clock())
    assert m.validation(3, 0.6)
    assert not m.validation(6, 0.7)
    assert m.validation(9, 0.5)
    assert (m.best_step, m.best_wer, m.last_val_step) == (9, 0.5, 9)


def test_monitor_periodic_states_and_stop_decision(tmp_path: Path) -> None:
    clock = Clock()
    m = monitor(tmp_path, [], clock, stop_grace_s=60)
    assert not m.state_due()
    clock.t = 1200
    assert m.state_due()
    m.state_saved(100, 10.0)
    assert not m.state_due()
    assert m.save_fresh_on_stop()  # a 10 s save fits the 60 s grace
    m.state_saved(200, 45.0)
    assert not m.save_fresh_on_stop()  # a 45 s save does not: release the periodic state
    fresh = monitor(tmp_path, [], Clock())
    assert fresh.save_fresh_on_stop()  # no state yet: try a fresh one


def test_monitor_stop_follows_the_context(tmp_path: Path) -> None:
    import threading

    stop = threading.Event()
    c = StepContext(lambda e: None, work_dir=tmp_path, stop=stop)
    m = TrainingMonitor(c, total_steps=5, log_every=1, state_every_s=60, clock=Clock())
    assert not m.stop_requested()
    stop.set()
    assert m.stop_requested()
    assert m.stop_seen_at == 0.0


# ---------------------------------------------------------------- streaming hypotheses


def test_partials_and_words() -> None:
    streams = [Stream(frames=35), Stream(frames=12)]
    assert chunk_frames([9, 16], True) == 9
    assert chunk_frames([9, 16], False) == 16
    assert chunk_frames(8, True) == 8
    record_partials(streams, ["", "a"], 0, 9, 10.0, 5.0)
    record_partials(streams, ["hello", "a b"], 9, 16, 10.0, 9.0)
    record_partials(streams, ["hello world", "a b"], 25, 16, 10.0, 12.0)
    assert [p["audioOffsetMs"] for p in streams[0].partials] == [90, 250, 350]
    assert [p["audioOffsetMs"] for p in streams[1].partials] == [90, 120]  # the short stream stopped at its end
    words = words_from_partials("hello world", streams[0].partials, [0.9, 0.8])
    assert words == [
        {"word": "hello", "start": 0.0, "end": 0.25, "confidence": 0.9},
        {"word": "world", "start": 0.25, "end": 0.35, "confidence": 0.8},
    ]
    assert words_from_partials("x y", [], None)[1]["confidence"] is None


def test_hypothesis_rows_carry_every_field() -> None:
    s = Stream(frames=10, partials=[{"audioOffsetMs": 100, "emitMs": 3.0, "text": "שלום", "final": True}], text="שלום")
    decoding = {"profile": "160ms", "attContextSize": [56, 1]}
    row = hypothesis_row("b3:" + "1" * 64, s, decoding, decoding_hash(decoding), "b3:" + "2" * 64)
    assert all(k in row for k in HYPOTHESIS_FIELDS)
    assert row["family"] == NAME
    assert row["words"][0]["word"] == "שלום"
    assert decoding_hash(decoding) == decoding_hash(dict(reversed(list(decoding.items()))))


def test_word_confidence_from_tokens_skips_the_locale_tag() -> None:
    pieces = ["▁sh", "a", "l", ".", "▁wo", "rld", "▁", "<he-IL>"]
    conf = [0.9, 0.8, 0.95, 0.99, 0.5, 0.7, 0.99, 0.98]
    assert word_confidence(pieces, conf) == [0.8, 0.5]
    assert word_confidence(["▁", "a", "b", "▁c"], [0.1, 0.6, 0.7, 0.3]) == [0.6, 0.3]


def test_finetune_final_state_is_optional() -> None:
    from cadence_worker.steps.base import descriptor

    d = descriptor("nemotron_finetune", FinetuneStep)
    assert d.get("optionalOutputs") == ["state"]
    assert "state" in d["produces"], "the state is still an output: a stop writes it"
    assert "optionalOutputs" not in descriptor("nemotron_calibrate", CalibrateStep)
