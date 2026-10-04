"""Phase-4 audit C1: a golden set imported from the Hub (dataset_import, hf-dataset) and the same FLEURS files indexed
on a mount (sdp_ingest) must meet in the leakage check. With ``segmentation: file`` a segment is the import's utterance
byte for byte; cut by VAD, its ``file-b3`` is. A corpus's ``test/`` split is left out of an ingest by default.

The fixture is FLEURS-like: three FLEURS sr_rs test clips (tests/fixtures/fleurs-sr-rs: two converted to 16-bit PCM,
one FLEURS's own 32-bit float WAV) laid out as the corpus script writes them, ``<split>/<id>.wav`` and ``<split>.tsv``.
"""

from __future__ import annotations

import json
import shutil
from collections.abc import Iterable, Mapping
from pathlib import Path
from typing import Any

import pytest

from cadence_worker import segments as seg
from cadence_worker.cas import hash_file
from cadence_worker.mounts import Mounts
from cadence_worker.steps import dataset_import as di
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.dataset_freeze import member_line
from cadence_worker.steps.sdp_ingest import SdpIngestParams, SdpIngestStep

FIX = Path(__file__).parent / "fixtures" / "fleurs-sr-rs" / "audio"
SPLITS = {"train": ["clip1"], "test": ["clip2", "clip3"]}


class Ctx:
    def __init__(self, root: Path) -> None:
        self.mounts = Mounts([{"name": "corpora", "kind": "local", "root": str(root), "readOnly": True}], env={})

    def progress(self, fraction: float, message: str = "") -> None:
        pass

    def log(self, msg: str, level: str = "info", **fields: Any) -> None:
        pass


def fleurs(root: Path) -> Path:
    """<root>/fleurs-sr/r1/{train,test}/<id>.wav and <split>.tsv, as scripts/corpora/fleurs.sh lays them out."""
    rev = root / "fleurs-sr" / "r1"
    for split, clips in SPLITS.items():
        (rev / split).mkdir(parents=True)
        rows = []
        for n, c in enumerate(clips):
            shutil.copyfile(FIX / f"{c}.wav", rev / split / f"{1000 + n}{c[-1]}.wav")
            rows.append(f"{n}\t{1000 + n}{c[-1]}.wav\tRečenica {c}.\trecenica {c}\tx\t19200\tMALE\n")
        (rev / f"{split}.tsv").write_text("".join(rows), encoding="utf-8")
    return rev


def ingest(root: Path, path: str, **kw: Any) -> list[dict[str, Any]]:
    out = root.parent / f"segments-{len(list(root.parent.glob('segments-*')))}"
    p = SdpIngestParams(source="fleurs-sr", path=path, language="sr-RS", **kw)
    SdpIngestStep().run(p, {}, {"segments": out}, Ctx(root))
    return seg.read(out)[1]


def hub_import(tmp_path: Path, files: list[Path], monkeypatch: pytest.MonkeyPatch) -> set[str]:
    """The golden side: the files imported as the Hub serves them (the original WAV bytes); returns the audio-b3 of
    every utterance, as the control plane's dataset hook computes it (the content hash of the stored WAV)."""

    def fake(repo: str, config: str, split: str, revision: str, streaming: bool = False) -> Iterable[Mapping[str, Any]]:
        return [{"audio": {"bytes": f.read_bytes(), "path": f.name}, "raw_transcription": f"t {f.stem}"} for f in files]

    monkeypatch.setattr(di, "load_hf", fake)
    p = di.DatasetImportParams(
        source_name="fleurs", licence="CC-BY-4.0", locale="sr-RS", format="hf-dataset", hf_config="sr_rs",
        hf_split="test", split_rule="source", eval_only=True,
    )  # fmt: skip
    out = tmp_path / "golden"
    di.DatasetImportStep().run(p, {}, {"dataset": out})
    lines = [json.loads(x) for x in (out / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert len(lines) == len(files)
    return {hash_file(out / x["audio"]) for x in lines}


def test_file_segmentation_hashes_as_the_hub_import(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    root = tmp_path / "mnt"
    rev = fleurs(root)
    golden = hub_import(tmp_path, sorted((rev / "test").glob("*.wav")), monkeypatch)
    rows = ingest(root, "mount://corpora/fleurs-sr/r1/test", segmentation="file")
    assert {r["hash"] for r in rows} == golden, "a pre-segmented file ingested whole is the imported utterance"
    assert all(r[seg.FILE_FINGERPRINT] == r["hash"] for r in rows)


def test_vad_segments_carry_the_whole_file_hash(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    root = tmp_path / "mnt"
    rev = fleurs(root)
    golden = hub_import(tmp_path, sorted((rev / "test").glob("*.wav")), monkeypatch)
    rows = ingest(root, "mount://corpora/fleurs-sr/r1/test", vad_min_speech_ms=50, min_segment_s=0.1)
    assert rows
    assert not {r["hash"] for r in rows} & golden, "re-cut by VAD, the segments' own hashes miss the golden set"
    # Every segment's file-b3 is the audio-b3 of the golden utterance it was cut from (a 1.2 s clip may hold no
    # segment long enough to keep).
    assert {r[seg.FILE_FINGERPRINT] for r in rows} <= golden, "their file-b3 finds it"
    # A draft carries file-b3 as a fingerprint the dataset hook stores beside audio-b3.
    line = member_line({**rows[0], "text": "t", "split": "train", "origin": "human"})
    assert line["fingerprints"] == {seg.FILE_FINGERPRINT: rows[0][seg.FILE_FINGERPRINT]}


def test_stereo_tracks_hash_per_channel(tmp_path: Path) -> None:
    from test_ingest import bursts, stereo_wav

    root = tmp_path / "mnt"
    stereo_wav(root / "calls" / "c.wav", bursts(8000, [(0.5, 2.0)], 3.0), bursts(8000, [(1.0, 2.5)], 3.0, 500), 8000)
    rows = ingest(root, "mount://corpora/calls", channels="split", exclude=[])
    by_channel = {r["channel"]: r[seg.FILE_FINGERPRINT] for r in rows}
    assert set(by_channel) == {0, 1}
    assert by_channel[0] != by_channel[1]


def test_the_test_split_is_left_out_by_default(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    fleurs(root)
    rows = ingest(root, "mount://corpora/fleurs-sr/r1", segmentation="file")
    assert {r["file"].split("/")[-2] for r in rows} == {"train"}
    every = ingest(root, "mount://corpora/fleurs-sr/r1", segmentation="file", exclude=[])
    assert {r["file"].split("/")[-2] for r in every} == {"train", "test"}
    # Pointing at the test split itself is explicit: nothing under it is excluded.
    assert len(ingest(root, "mount://corpora/fleurs-sr/r1/test", segmentation="file")) == 2


def test_a_path_not_on_the_mount_names_what_is_there(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    fleurs(root)
    with pytest.raises(StepInputError, match=r"fleurs-sr holds: r1\b"):
        ingest(root, "mount://corpora/fleurs-sr/2026-10-01")
    with pytest.raises(StepInputError, match=r"its root holds: fleurs-sr\b"):
        ingest(root, "mount://corpora/my-corpus/set-me-revision")
