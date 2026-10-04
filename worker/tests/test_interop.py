"""Phase 4 · stream I: imports (dataset_import@4: NeMo segments, Lhotse cuts, Lhotse Shar, Cadence bundles, mount
paths), exports (shar_export@1, dataset_export@1, hf_push@1) and the noise bank mined from calls (noise_mine@1), on
synthesized audio (tones in noise; no licence needed) and the FLEURS fixture (CC-BY-4.0, tests/fixtures)."""

from __future__ import annotations

import gzip
import io
import json
import struct
import tarfile
from pathlib import Path
from typing import Any, cast

import numpy as np
import pytest

from cadence_worker import audio
from cadence_worker.__main__ import registry
from cadence_worker.cas import ManifestFile, encode_manifest, hash_bytes, hash_file
from cadence_worker.mounts import Mounts
from cadence_worker.steps import dataset_import as di
from cadence_worker.steps import hf_push
from cadence_worker.steps.base import StepInputError, missing_metadata
from cadence_worker.steps.dataset_export import DatasetExportParams, DatasetExportStep
from cadence_worker.steps.hf_push import HfPushParams, HfPushStep
from cadence_worker.steps.noise_mine import NoiseMineParams, NoiseMineStep, clips, silences
from cadence_worker.steps.sdp_ingest import SdpIngestParams, SdpIngestStep
from cadence_worker.steps.shar_export import SharExportParams, SharExportStep

FIX = Path(__file__).parent / "fixtures" / "fleurs-sr-rs"
NEW_KINDS: dict[str, Any] = {
    "shar_export": SharExportStep,
    "dataset_export": DatasetExportStep,
    "hf_push": HfPushStep,
    "noise_mine": NoiseMineStep,
}


class Ctx:
    def __init__(self, **roots: tuple[Path, bool]) -> None:
        self.mounts = Mounts(
            [{"name": n, "kind": "local", "root": str(r), "readOnly": ro} for n, (r, ro) in roots.items()], env={}
        )
        self.messages: list[str] = []

    def progress(self, fraction: float, message: str = "") -> None:
        self.messages.append(message)

    def log(self, msg: str, level: str = "info", **fields: Any) -> None:
        self.messages.append(msg)


def tones(rate: int, spans: list[tuple[float, float]], total: float, freq: float = 440.0, noise: float = 0.003) -> Any:
    t = np.arange(int(total * rate)) / rate
    x = np.random.default_rng(11).normal(0, noise, t.size)
    for a, b in spans:
        m = (t >= a) & (t < b)
        x[m] += 0.3 * np.sin(2 * np.pi * freq * t[m])
    return x


def wav(path: Path, x: Any, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(audio.wav_bytes(audio.Audio(samples=x, sample_rate=rate, channels=1)))


def stereo(path: Path, left: Any, right: Any, rate: int) -> None:
    pcm = np.round(np.clip(np.stack([left, right], axis=1), -1, 1) * 32767).astype("<i2").tobytes()
    head = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF",
        36 + len(pcm),
        b"WAVE",
        b"fmt ",
        16,
        1,
        2,
        rate,
        rate * 4,
        4,
        16,
        b"data",
        len(pcm),
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(head + pcm)


def imported(tmp_path: Path, ctx: Any = None, **kw: Any) -> tuple[dict[str, Any], list[dict[str, Any]], Path]:
    base: dict[str, Any] = {"source_name": "fleurs", "licence": "CC-BY-4.0", "locale": "sr-RS"}
    base.update(kw)
    out = tmp_path / "dataset"
    di.DatasetImportStep().run(di.DatasetImportParams(**base), {}, {"dataset": out}, ctx)
    header = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    lines = [json.loads(x) for x in (out / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    return header, lines, out


def fingerprint(out: Path, lines: list[dict[str, Any]]) -> list[tuple[str, str, str]]:
    """The content fingerprint's tuples: (audio hash, split, text)."""
    return sorted((hash_file(out / x["audio"]), x["split"], x["text"]) for x in lines)


def test_new_kinds_publish_complete_metadata() -> None:
    reg = registry()
    for name, cls in NEW_KINDS.items():
        assert reg[name]["version"] == "1", name
        assert cls.neutral is True
        assert missing_metadata(cast(Any, cls)) == [], name
    assert reg["dataset_import"]["version"] == "4"
    assert reg["shar_export"]["produces"] == {"export": "export"}
    assert reg["dataset_export"]["optionalInputs"] == ["record"]
    assert reg["hf_push"]["secrets"] == ["hf-token"]
    assert reg["noise_mine"]["produces"] == {"noise": "dataset"}
    assert reg["hf_push"]["resources"]["jobKind"] == "export"


# ---------------------------------------------------------------- imports


def test_nemo_manifest_segments_and_mount_paths(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    wav(root / "corpus" / "long.wav", tones(16000, [(0.0, 1.0), (1.5, 2.5)], 3.0), 16000)
    rows = [
        {"audio_filepath": "long.wav", "offset": 0.0, "duration": 1.0, "text": "Prvi deo.", "lang": "sr-RS"},
        {"audio_filepath": "long.wav", "offset": 1.5, "duration": 1.0, "text": "Drugi deo.", "lang": "sr-RS"},
    ]
    (root / "corpus" / "manifest.json").write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    ctx = Ctx(corpora=(root, True))
    header, lines, _ = imported(
        tmp_path, format="nemo-manifest", path="mount://corpora/corpus/manifest.json", split_rule="all-train", ctx=ctx
    )
    assert [x["duration"] for x in lines] == [pytest.approx(1.0), pytest.approx(1.0)]
    assert [x["text"] for x in lines] == ["Prvi deo.", "Drugi deo."]
    assert header["counts"]["train"] == 2
    with pytest.raises(StepInputError, match="fragment"):
        imported(tmp_path / "f", format="nemo-manifest", path="mount://corpora/corpus/manifest.json#t=0,1", ctx=ctx)


def test_lhotse_cuts_with_channels_and_supervisions(tmp_path: Path) -> None:
    rec = tmp_path / "cuts" / "call.wav"
    stereo(rec, tones(8000, [(0.2, 1.2)], 2.0), tones(8000, [(1.0, 1.8)], 2.0, 600), 8000)
    cuts = [
        {
            "id": "c1",
            "start": 0.2,
            "duration": 1.0,
            "channel": 0,
            "supervisions": [
                {"id": "s2", "start": 0.5, "duration": 0.5, "text": "svet", "language": "sr-RS", "speaker": "p1"},
                {"id": "s1", "start": 0.0, "duration": 0.5, "text": "Zdravo", "language": "sr-RS", "speaker": "p1"},
            ],
            "recording": {
                "id": "call",
                "sources": [{"type": "file", "channels": [0, 1], "source": "call.wav"}],
                "sampling_rate": 8000,
                "num_samples": 16000,
                "duration": 2.0,
                "channel_ids": [0, 1],
            },
            "custom": {"origin": "pseudo-label", "split": "validation"},
            "type": "MonoCut",
        }
    ]
    with gzip.open(tmp_path / "cuts" / "cuts.jsonl.gz", "wt", encoding="utf-8") as f:
        f.write("".join(json.dumps(c) + "\n" for c in cuts))
    header, lines, _ = imported(tmp_path, format="lhotse-cuts", path=str(tmp_path / "cuts"), split_rule="source")
    assert len(lines) == 1
    x = lines[0]
    assert x["text"] == "Zdravo svet", "supervisions joined in time order"
    assert x["speaker"] == "p1"
    assert x["origin"] == "pseudo-label"
    assert x["split"] == "validation"
    assert x["duration"] == pytest.approx(1.0)
    assert x["sampleRate"] == 16000
    assert header["counts"]["validation"] == 1


def dataset_dir(tmp_path: Path) -> tuple[Path, dict[str, Any], list[dict[str, Any]]]:
    header, lines, out = imported(tmp_path / "src", format="folder-csv", path=str(FIX), split_rule="source")
    # Spread the three test clips over the splits so per-split outputs are exercised.
    for x, s in zip(lines, ("train", "validation", "test"), strict=True):
        x["split"] = s
        x["speaker"] = "spk-" + s
    header["counts"] = {"train": 1, "validation": 1, "test": 1}
    (out / "manifest.jsonl").write_text("".join(json.dumps(x, sort_keys=True) + "\n" for x in lines), encoding="utf-8")
    (out / "card.md").write_text("# dataset/fleurs-sr\n\nThree clips.\n", encoding="utf-8")
    header["card"] = "card.md"
    (out / "dataset.json").write_text(json.dumps(header, sort_keys=True), encoding="utf-8")
    return out, header, lines


# ---------------------------------------------------------------- exports


def test_shar_export_round_trips_through_the_shar_import(tmp_path: Path) -> None:
    ds, _, lines = dataset_dir(tmp_path)
    exports = tmp_path / "exports"
    ctx = Ctx(exports=(exports, False))
    out = tmp_path / "export"
    p = SharExportParams(target="mount://exports/fleurs-sr/v1/lhotse-shar", version="ver_1", shard_utterances=1)
    SharExportStep().run(p, {"dataset": ds}, {"export": out}, ctx)
    doc = json.loads((out / "export.json").read_text(encoding="utf-8"))
    assert doc["format"] == "cadence.export/1"
    assert doc["exportFormat"] == "lhotse-shar"
    assert doc["target"] == "mount://exports/fleurs-sr/v1/lhotse-shar"
    assert doc["version"] == "ver_1"
    assert doc["utterances"] == 3
    names = sorted(f["path"] for f in doc["files"])
    assert names == sorted(
        f"{s}/{k}.000000.{e}"
        for s in ("train", "validation", "test")
        for k, e in (("cuts", "jsonl.gz"), ("recording", "tar"))
    )
    shar = exports / "fleurs-sr" / "v1" / "lhotse-shar"
    for f in doc["files"]:
        assert hash_file(shar / f["path"]) == f["hash"]
    assert not list(out.glob("files")), "a mount target keeps the files out of the content store"

    # The tar holds <id>.wav then <id>.json; the cuts point at it with a shar placeholder.
    with tarfile.open(shar / "train" / "recording.000000.tar") as tar:
        members = tar.getmembers()
        assert [m.name.rsplit(".", 1)[1] for m in members] == ["wav", "json"]
        assert all(m.mtime == 0 and m.uid == 0 for m in members)
        body = tar.extractfile(members[0])
        assert body is not None
        wav_bytes = body.read()
    with gzip.open(shar / "train" / "cuts.000000.jsonl.gz", "rt", encoding="utf-8") as f:
        cut = json.loads(f.readline())
    assert cut["recording"]["sources"] == [{"type": "shar", "channels": [0], "source": ""}]
    assert cut["id"] == hash_bytes(wav_bytes).removeprefix("b3:") == members[0].name.removesuffix(".wav")
    assert cut["supervisions"][0]["speaker"] == "spk-train"

    # Deterministic: the same dataset exports to the same bytes.
    again = tmp_path / "again"
    SharExportStep().run(p.model_copy(update={"target": "cas"}), {"dataset": ds}, {"export": again}, ctx)
    assert hash_file(again / "files" / "train" / "recording.000000.tar") == hash_file(
        shar / "train" / "recording.000000.tar"
    )

    # Imported back with split_rule source: the same audio bytes, splits and texts — the same content fingerprint.
    _, back, out_back = imported(
        tmp_path / "back",
        format="lhotse-shar",
        path="mount://exports/fleurs-sr/v1/lhotse-shar",
        split_rule="source",
        ctx=ctx,
    )
    assert fingerprint(out_back, back) == fingerprint(ds, lines)


def test_nemo_manifest_export_copies_audio_unchanged(tmp_path: Path) -> None:
    ds, _, lines = dataset_dir(tmp_path)
    out = tmp_path / "export"
    DatasetExportStep().run(DatasetExportParams(format="nemo-manifest", target="cas"), {"dataset": ds}, {"export": out})
    doc = json.loads((out / "export.json").read_text(encoding="utf-8"))
    assert doc["target"] == "cas"
    by_path = {f["path"]: f for f in doc["files"]}
    rows = [json.loads(x) for x in (out / "files" / "manifest.train.jsonl").read_text(encoding="utf-8").splitlines()]
    assert len(rows) == 1
    assert set(rows[0]) == {"audio_filepath", "duration", "text", "lang", "speaker"}
    # Each audio file is the dataset's blob, unchanged: the control plane records it as a copy.
    for x in lines:
        assert by_path[x["audio"]]["hash"] == hash_file(ds / x["audio"])
    # A read-only mount is refused.
    ro = Ctx(corpora=(tmp_path / "ro", True))
    with pytest.raises(StepInputError, match="read-only"):
        DatasetExportStep().run(
            DatasetExportParams(target="mount://corpora/x"), {"dataset": ds}, {"export": tmp_path / "x"}, ro
        )


def record_for(ds: Path) -> dict[str, Any]:
    files = [
        ManifestFile(f.relative_to(ds).as_posix(), hash_file(f), f.stat().st_size) for f in ds.rglob("*") if f.is_file()
    ]
    artifact = hash_bytes(encode_manifest(files))
    return {
        "format": "cadence.registry-record/1",
        "kind": "dataset_version",
        "collection": "dataset/fleurs-sr",
        "version": "2026-10-03.abcdef1",
        "versionId": "ver_src",
        "licence": "CC-BY-4.0",
        "tags": ["locale:sr-RS"],
        "payload": {"artifact": {"hash": artifact}},
        "sources": [
            {
                "name": "fleurs",
                "licence": "CC-BY-4.0",
                "kind": "public",
                "languages": ["sr-RS"],
                "trainingCleared": True,
            }
        ],
    }


def test_bundle_export_and_import_keep_the_identity(tmp_path: Path) -> None:
    ds, _, lines = dataset_dir(tmp_path)
    rec = tmp_path / "record.json"
    rec.write_text(json.dumps(record_for(ds)), encoding="utf-8")
    exports = tmp_path / "exports"
    ctx = Ctx(exports=(exports, False))
    out = tmp_path / "export"
    p = DatasetExportParams(format="cadence-bundle", target="mount://exports/b1")
    DatasetExportStep().run(p, {"dataset": ds, "record": rec}, {"export": out}, ctx)
    doc = json.loads((out / "export.json").read_text(encoding="utf-8"))
    bundle = json.loads((exports / "b1" / "bundle.json").read_text(encoding="utf-8"))
    assert bundle["format"] == "cadence.bundle/1"
    assert bundle["artifact"] == record_for(ds)["payload"]["artifact"]["hash"]
    blobs = [f for f in doc["files"] if f["path"].startswith("cas/b3/")]
    assert len(blobs) == len(bundle["files"]) + 1, "every file of the artifact and its manifest"
    for f in blobs:
        assert f["path"].endswith(f["hash"].removeprefix("b3:"))

    # No record input: refused.
    with pytest.raises(StepInputError, match="record"):
        DatasetExportStep().run(p, {"dataset": ds}, {"export": tmp_path / "x"}, ctx)

    header, back, out_back = imported(
        tmp_path / "in", format="cadence-bundle", path="mount://exports/b1", source_name="", licence="", ctx=ctx
    )
    assert header["source"] == {"name": "fleurs", "licence": "CC-BY-4.0", "kind": "public", "languages": ["sr-RS"]}
    assert header["name"] == "fleurs-sr"
    assert fingerprint(out_back, back) == fingerprint(ds, lines)
    for x in back:
        assert (out_back / x["audio"]).read_bytes() == (ds / x["audio"]).read_bytes()
    with pytest.raises(StepInputError, match="keeps its licence"):
        imported(tmp_path / "in2", format="cadence-bundle", path="mount://exports/b1", licence="MIT", ctx=ctx)


class FakeHub:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, Any]]] = []
        self.files: list[str] = []
        self.readme = ""

    def create_repo(self, **kw: Any) -> None:
        self.calls.append(("create_repo", kw))

    def upload_folder(self, **kw: Any) -> Any:
        self.calls.append(("upload_folder", kw))
        root = Path(kw["folder_path"])
        self.files = sorted(f.relative_to(root).as_posix() for f in root.rglob("*") if f.is_file())
        self.readme = (root / "README.md").read_text(encoding="utf-8")

        class Info:
            oid = "0123456789abcdef0123456789abcdef01234567"
            commit_url = "https://huggingface.co/datasets/acme/fleurs-sr/commit/0123456"

        return Info()


def test_hf_push_uploads_an_audiofolder(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    ds, _, _ = dataset_dir(tmp_path)
    hub = FakeHub()
    tokens: list[str] = []

    def fake_api(token: str) -> FakeHub:
        tokens.append(token)
        return hub

    monkeypatch.setattr(hf_push, "hub_api", fake_api)
    p = HfPushParams(repo="acme/fleurs-sr", private=True, licence="CC-BY-4.0", version="ver_1", name="fleurs-sr")
    monkeypatch.delenv("HF_TOKEN", raising=False)
    with pytest.raises(StepInputError, match="hf-token"):
        HfPushStep().run(p, {"dataset": ds}, {"export": tmp_path / "x"})
    monkeypatch.setenv("HF_TOKEN", "hf_secret")
    out = tmp_path / "export"
    HfPushStep().run(p, {"dataset": ds}, {"export": out})
    assert tokens == ["hf_secret"]
    assert hub.calls[0] == (
        "create_repo",
        {"repo_id": "acme/fleurs-sr", "repo_type": "dataset", "private": True, "exist_ok": True},
    )
    assert "data/train/metadata.jsonl" in hub.files
    assert "README.md" in hub.files
    assert hub.readme.startswith("---\n")
    assert "license: cc-by-4.0" in hub.readme
    assert "automatic-speech-recognition" in hub.readme
    assert "# dataset/fleurs-sr" in hub.readme, "the dataset card follows the header"
    doc = json.loads((out / "export.json").read_text(encoding="utf-8"))
    assert doc["target"] == "hf://datasets/acme/fleurs-sr"
    assert doc["hub"]["commit"].startswith("0123456789")
    assert "hf_secret" not in (out / "export.json").read_text(encoding="utf-8")
    assert hf_push.hub_licence("Apache-2.0") == "apache-2.0"
    assert hf_push.hub_licence("Proprietary licence v2") == "other"


# ---------------------------------------------------------------- the noise bank mined from calls


def test_silences_keep_their_distance_from_speech() -> None:
    gaps = silences([(1.0, 2.0), (1.8, 3.0), (6.0, 7.0)], 10.0, 0.2)
    assert gaps == [(0.0, 0.8), (3.2, 5.8), (7.2, 10.0)]
    got = clips(gaps, 1.0, 2.0)
    assert got == [(3.2, 5.2), (7.2, 9.2)], "a piece shorter than min_clip_s is dropped"


def test_noise_mine_takes_the_callers_silences(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    src = root / "calls" / "r1"
    # Caller line noise around two turns; the bot channel is digital silence between its turn.
    caller = tones(8000, [(0.5, 2.0), (6.0, 7.5)], 10.0, 300, noise=0.01)
    bot = np.zeros(80000)
    bot[int(2.5 * 8000) : int(4.5 * 8000)] = 0.3 * np.sin(2 * np.pi * 500 * np.arange(16000) / 8000)
    stereo(src / "call.wav", caller, bot, 8000)
    side = {"roles": ["caller", "bot"], "script": [{"channel": 1, "start": 2.5, "end": 4.5, "text": "Izvolite."}]}
    (src / "call.cadence.json").write_text(json.dumps(side), encoding="utf-8")
    ctx = Ctx(corpora=(root, True))
    segs = tmp_path / "segments"
    SdpIngestStep().run(SdpIngestParams(source="calls", path="mount://corpora/calls/r1"), {}, {"segments": segs}, ctx)
    out = tmp_path / "noise"
    p = NoiseMineParams(min_clip_s=0.5, max_clip_s=2.0)
    NoiseMineStep().run(p, {"segments": segs}, {"noise": out}, ctx)
    head = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    lines = [json.loads(x) for x in (out / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert head["purpose"] == "noise"
    assert head["source"] == {"name": "calls"}, "only the registered source's name: the control plane checks it"
    assert head["name"] == "calls-calls"
    assert head["mined"]["roles"] == ["caller", "bot"]
    assert head["mined"]["segments"].startswith("b3:")
    assert set(head["tags"]) >= {"noise-bank", "mined"}
    assert lines
    assert {x["role"] for x in lines} == {"caller"}, "the bot's digital silence drops out at min_rms_db"
    assert head["mined"]["dropped"]["quiet"] > 0
    assert head["counts"] == {"train": len(lines), "validation": 0, "test": 0}
    for x in lines:
        assert (x["split"], x["text"], x["language"]) == ("train", "", "und")
        assert 0.5 - 1e-6 <= x["duration"] <= 2.0 + 1e-6
        assert x["uri"].startswith("mount://corpora/calls/r1/call.wav#t=")
        assert x["uri"].endswith("&ch=0")
        a, b = (float(v) for v in x["uri"].split("#t=")[1].split("&")[0].split(","))
        for s, e in [(0.5, 2.0), (2.5, 4.5), (6.0, 7.5)]:
            assert b <= s or a >= e, f"{x['uri']} overlaps speech {s}-{e}"
        body = (out / x["audio"]).read_bytes()
        assert hash_bytes(body) == "b3:" + Path(x["audio"]).stem
        assert struct.unpack_from("<HHI", body, 20) == (1, 1, 16000)
    # Capped per recording.
    capped = tmp_path / "capped"
    NoiseMineStep().run(p.model_copy(update={"max_clips_per_file": 1}), {"segments": segs}, {"noise": capped}, ctx)
    assert len((capped / "manifest.jsonl").read_text(encoding="utf-8").splitlines()) == 1
    with pytest.raises(StepInputError, match="roles"):
        NoiseMineStep().run(
            p.model_copy(update={"roles": ["agent"]}), {"segments": segs}, {"noise": tmp_path / "x"}, ctx
        )


def test_shar_tar_pairs_reader() -> None:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w") as tar:
        for name, body in (("a.wav", b"RIFF"), ("a.json", b'{"id": "a"}')):
            info = tarfile.TarInfo(name)
            info.size = len(body)
            tar.addfile(info, io.BytesIO(body))
    buf.seek(0)
    with tarfile.open(fileobj=buf) as tar:
        pairs = list(di._tar_pairs(tar))
    assert pairs == [("a.wav", b"RIFF", {"id": "a"})]
