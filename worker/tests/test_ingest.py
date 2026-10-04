"""Phase 4 · stream D: sdp_ingest → text_normalise → manifest_filter → speaker_disjoint_split → dataset_freeze on
synthesized audio (tone bursts in silence; no licence needed) on a temporary mount."""

from __future__ import annotations

import gzip
import json
import struct
from collections.abc import Mapping
from pathlib import Path
from typing import Any, cast

import numpy as np
import pytest

from cadence_worker import audio
from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.__main__ import registry
from cadence_worker.cas import hash_bytes
from cadence_worker.mounts import Mounts
from cadence_worker.steps import dataset_freeze as freeze_mod
from cadence_worker.steps.base import StepInputError, missing_metadata
from cadence_worker.steps.dataset_freeze import DatasetFreezeParams, DatasetFreezeStep, member_line
from cadence_worker.steps.manifest_filter import ManifestFilterParams, ManifestFilterStep
from cadence_worker.steps.sdp_ingest import SdpIngestParams, SdpIngestStep
from cadence_worker.steps.speaker_disjoint_split import SpeakerDisjointSplitParams, SpeakerDisjointSplitStep
from cadence_worker.steps.text_normalise import TextNormaliseParams, TextNormaliseStep

KINDS: dict[str, Any] = {
    "sdp_ingest": SdpIngestStep,
    "text_normalise": TextNormaliseStep,
    "manifest_filter": ManifestFilterStep,
    "speaker_disjoint_split": SpeakerDisjointSplitStep,
    "dataset_freeze": DatasetFreezeStep,
}


class Ctx:
    def __init__(self, root: Path) -> None:
        self.mounts = Mounts([{"name": "corpora", "kind": "local", "root": str(root), "readOnly": True}], env={})
        self.logs: list[str] = []

    def progress(self, fraction: float, message: str = "") -> None:
        pass

    def log(self, msg: str, level: str = "info", **fields: Any) -> None:
        self.logs.append(msg)


def bursts(rate: int, spans: list[tuple[float, float]], total: float, freq: float = 440.0) -> np.ndarray:
    t = np.arange(int(total * rate)) / rate
    x = np.random.default_rng(7).normal(0, 0.0005, t.size)
    for a, b in spans:
        m = (t >= a) & (t < b)
        x[m] += 0.3 * np.sin(2 * np.pi * freq * t[m])
    return x


def mono_wav(path: Path, x: np.ndarray, rate: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(audio.wav_bytes(audio.Audio(samples=x, sample_rate=rate, channels=1)))


def stereo_wav(path: Path, left: np.ndarray, right: np.ndarray, rate: int) -> None:
    pcm = np.round(np.clip(np.stack([left, right], axis=1), -1, 1) * 32767).astype("<i2").tobytes()
    header = struct.pack(
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
    path.write_bytes(header + pcm)


def corpus(root: Path) -> None:
    src = root / "toy" / "r1"
    # Two pre-segmented mono files with transcripts (one per speaker) and a long mono file cut by VAD.
    mono_wav(src / "a.wav", bursts(16000, [(0.2, 1.8)], 2.0), 16000)
    (src / "a.txt").write_text("Dobar dan.\n", encoding="utf-8")
    mono_wav(src / "b.wav", bursts(22050, [(0.1, 1.5)], 1.7, 330), 22050)
    (src / "b.txt").write_text("Хвала вам.", encoding="utf-8")
    mono_wav(src / "long.wav", bursts(16000, [(0.5, 2.0), (3.0, 4.2), (5.5, 7.0)], 8.0), 16000)
    # A stereo 8 kHz call: caller on 0 (VAD), bot on 1 (TTS script).
    stereo_wav(
        src / "call.wav",
        bursts(8000, [(0.5, 2.5), (5.0, 6.5)], 8.0, 300),
        bursts(8000, [(3.0, 4.5)], 8.0, 500),
        8000,
    )
    side = {
        "roles": ["caller", "bot"],
        "speakers": ["caller-1", ""],
        "script": [{"channel": 1, "start": 2.9, "end": 4.6, "text": "Kako vam mogu pomoći?"}],
    }
    (src / "call.cadence.json").write_text(json.dumps(side), encoding="utf-8")
    (src / "SOURCE.yaml").write_text("licence: CC-BY-4.0\nurl: https://example.org\nrevision: r1\n", encoding="utf-8")


def ingest(tmp_path: Path, **kw: Any) -> tuple[Path, Ctx]:
    root = tmp_path / "mnt"
    corpus(root)
    ctx = Ctx(root)
    out = tmp_path / "segments"
    p = SdpIngestParams(source="toy", path="mount://corpora/toy/r1", language="sr-RS", **kw)
    SdpIngestStep().run(p, {}, {"segments": out}, ctx)
    return out, ctx


def step(kind: Any, params: Any, src: Path, dst: Path, ctx: Any = None, out: str = "segments") -> Path:
    kind().run(params, {"segments": src}, {out: dst}, ctx)
    return dst


def test_kinds_publish_complete_metadata() -> None:
    reg = registry()
    versions = {"sdp_ingest": "2", "manifest_filter": "2"}
    for name, cls in KINDS.items():
        assert reg[name]["version"] == versions.get(name, "1")
        assert cls.neutral is True
        assert missing_metadata(cast(Any, cls)) == [], name
    props = reg["sdp_ingest"]["params"]["properties"]
    assert props["source"]["x-cadence"]["registry"] == "source"
    assert reg["dataset_freeze"]["produces"] == {"dataset": "dataset"}


def test_uri_round_trip_and_escape() -> None:
    uri = mounts.format_uri("corpora", "toy/r1/a.wav", 0.25, 1.5, 1)
    assert uri == "mount://corpora/toy/r1/a.wav#t=0.25,1.5&ch=1"
    ref = mounts.parse(uri)
    assert (ref.name, ref.path, ref.start, ref.end, ref.channel) == ("corpora", "toy/r1/a.wav", 0.25, 1.5, 1)
    assert ref.file_uri == "mount://corpora/toy/r1/a.wav"
    with pytest.raises(StepInputError):
        mounts.parse("mount://corpora/../etc/passwd")
    with pytest.raises(StepInputError):
        mounts.parse("file:///etc/passwd")


def test_resolver_refuses_links_out_of_the_root(tmp_path: Path) -> None:
    root = tmp_path / "root"
    root.mkdir()
    (tmp_path / "secret").write_text("x")
    (root / "link").symlink_to(tmp_path / "secret")
    ms = [{"name": "corpora", "root": str(root)}]
    with pytest.raises(StepInputError, match="leaves"):
        mounts.resolve("mount://corpora/link", ms)
    with pytest.raises(StepInputError, match="no mount"):
        mounts.resolve("mount://other/x", ms)


def test_env_mounts(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CADENCE_MOUNTS", json.dumps([{"name": "corpora", "kind": "local", "root": str(tmp_path)}]))
    assert mounts.resolve("mount://corpora/a/b.wav", mounts.mounts_of(None)) == (tmp_path / "a" / "b.wav").resolve()


def test_eou_of() -> None:
    own = [(0.5, 2.5), (5.0, 6.5)]
    others = [(3.0, 4.5), (6.0, 9.0)]
    assert seg.eou_of(own, 0.3, 2.8, others) == {"speechEnd": 2.2, "nextSpeech": 2.7, "gapS": 0.5}
    # A barge-in: the other party starts before the speech ends.
    assert seg.eou_of(own, 4.8, 6.7, others) == {"speechEnd": 1.7, "nextSpeech": 1.2, "gapS": -0.5}
    # Nobody speaks within 10 s.
    assert seg.eou_of([(0.0, 1.0)], 0.0, 1.0, [(20.0, 21.0)]) == {"speechEnd": 1.0}
    # No speech found in the segment: its end counts.
    assert seg.eou_of([], 1.0, 2.0, [(2.5, 3.0)]) == {"speechEnd": 1.0, "nextSpeech": 1.5, "gapS": 0.5}


def test_vad_finds_the_bursts() -> None:
    x = seg.to_rate(bursts(16000, [(0.5, 2.0), (3.0, 4.2)], 5.0), 16000)
    p = SdpIngestParams(source="toy", path="mount://corpora/x")
    from cadence_worker.steps.sdp_ingest import vad_params

    runs = seg.speech(x, vad_params(p))
    assert len(runs) == 2
    assert abs(runs[0][0] - 0.5) < 0.05
    assert abs(runs[0][1] - 2.0) < 0.05
    cuts = seg.segments_of(x, runs, vad_params(p))
    assert len(cuts) == 2
    for a, b in cuts:
        assert round(a * 16000) == a * 16000
        assert b > a


def test_long_speech_is_split_below_max_segment() -> None:
    x = seg.to_rate(bursts(16000, [(0.0, 12.0)], 12.0), 16000)
    p = SdpIngestParams(source="toy", path="mount://corpora/x", max_segment_s=5.0)
    from cadence_worker.steps.sdp_ingest import vad_params

    vp = vad_params(p)
    cuts = seg.segments_of(x, [(0.0, 12.0)], vp)
    assert len(cuts) >= 3
    assert all(b - a <= 5.0 + 1e-6 for a, b in cuts)


def test_ingest_writes_segments(tmp_path: Path) -> None:
    out, _ = ingest(tmp_path)
    header, lines = seg.read(out)
    assert header["format"] == "cadence.segments/1"
    assert header["source"] == {"name": "toy"}
    assert header["sourceInfo"] == {"licence": "CC-BY-4.0", "url": "https://example.org", "revision": "r1"}
    assert header["steps"] == ["sdp_ingest@2"]
    assert header["files"] == 4
    assert header["counts"]["segments"] == len(lines)
    by_file: dict[str, list[dict[str, Any]]] = {}
    for x in lines:
        by_file.setdefault(x["file"].rsplit("/", 1)[1], []).append(x)
        assert x["hash"].startswith("b3:")
        assert x["uri"].startswith(x["file"] + "#t=")
        assert set(x["vad"]) == {"speech", "ratio"}
        assert set(x["level"]) == {"rmsDb", "peakDb", "clipping"}
        assert x["language"] == "sr-RS"
    assert [x["text"] for x in by_file["a.wav"]] == ["Dobar dan."]
    assert by_file["a.wav"][0]["origin"] == "human"
    assert by_file["a.wav"][0]["role"] == "mono"
    assert by_file["b.wav"][0]["sourceRate"] == 22050
    assert len(by_file["long.wav"]) == 3
    assert all("text" not in x for x in by_file["long.wav"])
    call = by_file["call.wav"]
    bot = [x for x in call if x["role"] == "bot"]
    caller = [x for x in call if x["role"] == "caller"]
    assert len(bot) == 1
    assert bot[0]["origin"] == "model:tts-script"
    assert bot[0]["text"] == "Kako vam mogu pomoći?"
    assert bot[0]["channel"] == 1
    assert bot[0]["uri"].endswith("&ch=1")
    assert len(caller) == 2
    assert all(x["speaker"] == "caller-1" and x["channel"] == 0 for x in caller)
    assert all("crosstalk" in x for x in call)
    assert bot[0]["crosstalk"] < 0.2
    # End of utterance per channel: the caller's first turn ends ≈ 2.5 s, the bot answers ≈ 3.0 s; the bot ends ≈ 4.5 s,
    # the caller answers ≈ 5.0 s; nobody speaks after the caller's last turn. Single-track files have none.
    first, last = sorted(caller, key=lambda x: x["start"])
    assert 0.2 <= first["eou"]["gapS"] <= 0.7
    assert first["eou"]["nextSpeech"] == pytest.approx(first["eou"]["speechEnd"] + first["eou"]["gapS"], abs=0.002)
    assert 0.2 <= bot[0]["eou"]["gapS"] <= 0.7
    assert "gapS" not in last["eou"]
    assert last["eou"]["speechEnd"] > 0
    assert all("eou" not in x for f in ("a.wav", "b.wav", "long.wav") for x in by_file[f])
    files = [json.loads(r) for r in (out / seg.FILES).read_text(encoding="utf-8").splitlines()]
    assert {f["uri"].rsplit("/", 1)[1]: f["roles"] for f in files}["call.wav"] == ["caller", "bot"]
    # The canonical hash is the hash of the 16 kHz WAV of exactly that range.
    a = by_file["a.wav"][0]
    x16 = seg.to_rate(seg.decode(tmp_path / "mnt" / "toy" / "r1" / "a.wav", tmp_path).channels[0], 16000)
    i0, i1 = seg.bounds(a["start"], a["end"])
    wav = audio.wav_bytes(audio.Audio(samples=x16[i0:i1], sample_rate=16000, channels=1))
    assert hash_bytes(wav) == a["hash"]
    assert len(wav) == a["bytes"]


def test_stereo_without_roles_is_mixed_down(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    stereo_wav(root / "s" / "c.wav", bursts(8000, [(0.5, 2.0)], 3.0), bursts(8000, [(0.5, 2.0)], 3.0), 8000)
    out = tmp_path / "seg"
    SdpIngestStep().run(SdpIngestParams(source="s1", path="mount://corpora/s"), {}, {"segments": out}, Ctx(root))
    _, lines = seg.read(out)
    assert lines
    assert all(x["channel"] == seg.MIXED and "&ch=" not in x["uri"] for x in lines)


def test_ingest_refuses_missing_source_and_empty_path(tmp_path: Path) -> None:
    root = tmp_path / "mnt"
    (root / "empty").mkdir(parents=True)
    with pytest.raises(StepInputError, match="source is required"):
        SdpIngestStep().run(SdpIngestParams(path="mount://corpora/empty"), {}, {"segments": tmp_path / "o"}, Ctx(root))
    with pytest.raises(StepInputError, match="no audio files"):
        SdpIngestStep().run(
            SdpIngestParams(source="x1", path="mount://corpora/empty"), {}, {"segments": tmp_path / "o"}, Ctx(root)
        )


def test_normalise_filter_split(tmp_path: Path) -> None:
    out, _ = ingest(tmp_path)
    norm = step(
        TextNormaliseStep,
        TextNormaliseParams(transliterate="sr-Cyrl-Latn", itn=[{"spoken": "dan", "written": "DAN"}]),
        out,
        tmp_path / "norm",
    )
    _, lines = seg.read(norm)
    texts = {x["text"] for x in lines if "text" in x}
    assert "Hvala vam." in texts
    assert "Dobar DAN." in texts
    assert any(x.get("textOriginal") == "Хвала вам." for x in lines)
    assert (norm / seg.FILES).is_file()

    filt = step(ManifestFilterStep, ManifestFilterParams(min_duration=0.5), norm, tmp_path / "filt")
    header, kept = seg.read(filt)
    assert header["filtered"]["role"] == 1  # the bot's turn
    assert header["filtered"]["empty_text"] >= 3  # untranscribed VAD segments
    assert all(x.get("text") for x in kept)
    assert header["steps"] == ["sdp_ingest@2", "text_normalise@1", "manifest_filter@2"]

    split = step(
        SpeakerDisjointSplitStep,
        SpeakerDisjointSplitParams(validation_share=0.5, min_validation_utterances=1),
        filt,
        tmp_path / "split",
    )
    h, lines = seg.read(split)
    assert h["splitRule"] == "speaker-disjoint"
    assert {x["split"] for x in lines} <= {"train", "validation"}
    assert any(x["split"] == "validation" for x in lines)


def test_filter_reasons() -> None:
    from cadence_worker.steps.manifest_filter import reason

    p = ManifestFilterParams()
    ok = {"duration": 2.0, "text": "dobar dan svima", "role": "caller", "language": "sr-RS", "vad": {"ratio": 0.8}}
    assert reason(p, ok) is None
    cases: list[tuple[Mapping[str, Any], str]] = [
        ({**ok, "duration": 0.1}, "duration"),
        ({**ok, "role": "bot"}, "role"),
        ({**ok, "origin": "pseudo-label:disputed"}, "origin"),
        ({**ok, "text": " "}, "empty_text"),
        ({**ok, "text": "x" * 200}, "chars_per_second"),
        ({**ok, "lid": {"language": "he"}}, "lid_mismatch"),
        ({**ok, "vad": {"ratio": 0.05}}, "speech_ratio"),
        ({**ok, "crosstalk": 0.9}, "crosstalk"),
    ]
    for x, why in cases:
        assert reason(p, x) == why
    # Serbian heard as Croatian is one language (pseudolabel.lid_equivalents), Hebrew is not.
    assert reason(p, {**ok, "lid": {"language": "hr"}}) is None
    assert reason(ManifestFilterParams(lid_equivalents=[]), {**ok, "lid": {"language": "hr"}}) == "lid_mismatch"
    assert reason(ManifestFilterParams(languages=["he"]), ok) == "language"
    assert reason(ManifestFilterParams(languages=["sr"]), ok) is None


def test_split_is_disjoint_and_stable() -> None:
    from cadence_worker.steps.speaker_disjoint_split import assign

    lines = [{"speaker": f"s{i % 7}", "file": f"f{i}", "text": str(i)} for i in range(70)]
    p = SpeakerDisjointSplitParams(validation_share=0.3, min_validation_utterances=0, test_share=0.2)
    assign(lines, p)
    sides: dict[str, set[str]] = {}
    for x in lines:
        sides.setdefault(str(x["speaker"]), set()).add(str(x["split"]))
    assert all(len(v) == 1 for v in sides.values())
    again = [{k: v for k, v in x.items() if k != "split"} for x in lines]
    assign(again, p)
    assert [x["split"] for x in again] == [x["split"] for x in lines]
    # Without speakers, a file is one group.
    nospk = [{"file": f"f{i // 2}", "text": str(i)} for i in range(20)]
    assign(nospk, SpeakerDisjointSplitParams(validation_share=0.3, min_validation_utterances=0))
    for i in range(0, 20, 2):
        assert nospk[i]["split"] == nospk[i + 1]["split"]


def prepared(tmp_path: Path) -> tuple[Path, Ctx]:
    out, ctx = ingest(tmp_path)
    filt = step(ManifestFilterStep, ManifestFilterParams(min_duration=0.5), out, tmp_path / "filt")
    split = step(
        SpeakerDisjointSplitStep,
        SpeakerDisjointSplitParams(validation_share=0.5, min_validation_utterances=1),
        filt,
        tmp_path / "split",
    )
    return split, ctx


def test_draft_then_cut(tmp_path: Path) -> None:
    split, ctx = prepared(tmp_path)
    _, segs = seg.read(split)
    draft = tmp_path / "draft"
    DatasetFreezeStep().run(DatasetFreezeParams(name="toy-sr"), {"segments": split}, {"dataset": draft}, ctx)
    h = json.loads((draft / "dataset.json").read_text(encoding="utf-8"))
    assert h["format"] == "cadence.dataset-draft/1"
    assert h["name"] == "toy-sr"
    assert h["source"] == {"name": "toy"}
    assert h["card"] == "card.md"
    assert (draft / "card.md").read_text(encoding="utf-8").startswith("# dataset/toy-sr")
    assert {c["name"] for c in h["quality"]["checks"]} == {"silence_share", "clipping_share", "length_outliers"}
    st = h["stats"]
    for k in ("durationHistogram", "charsPerSecondHistogram", "levelHistogram"):
        assert len(st[k]["edges"]) == len(st[k]["counts"])
    assert sum(st["durationHistogram"]["counts"]) == len(segs)
    assert set(st["durationPercentiles"]) == {"p5", "p50", "p95"}
    dlines = [json.loads(x) for x in (draft / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert [x["hash"] for x in dlines] == [x["hash"] for x in segs]
    assert all("audio" not in x and x["sampleRate"] == 16000 for x in dlines)
    assert sum(h["counts"].values()) == len(dlines)
    assert not list(draft.rglob("*.wav"))

    def cut(dst: Path) -> dict[str, Any]:
        p = DatasetFreezeParams(name="toy-sr", mode="cut", draft_version="ver_draft", shard_utterances=2)
        DatasetFreezeStep().run(p, {"segments": split}, {"dataset": dst}, ctx)
        return cast(dict[str, Any], json.loads((dst / "dataset.json").read_text(encoding="utf-8")))

    c = cut(tmp_path / "cut")
    assert c["format"] == "cadence.dataset/1"
    assert c["draftVersionId"] == "ver_draft"
    clines = [json.loads(x) for x in (tmp_path / "cut" / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    for line, s in zip(clines, segs, strict=True):
        wav = (tmp_path / "cut" / line["audio"]).read_bytes()
        assert hash_bytes(wav) == s["hash"]  # the cut is the indexed segment
        assert line["uri"] == s["uri"]
        assert line["channels"] == 1
    assert len(c["shards"]) == (len(clines) + 1) // 2
    first = gzip.decompress((tmp_path / "cut" / c["shards"][0]["cuts"]).read_bytes()).decode("utf-8")
    cut0 = json.loads(first.splitlines()[0])
    assert cut0["type"] == "MonoCut"
    assert cut0["recording"]["sources"][0]["source"] == clines[0]["audio"]
    # Deterministic: a second cut is byte-identical.
    cut(tmp_path / "cut2")
    for f in sorted((tmp_path / "cut").rglob("*")):
        if f.is_file():
            assert f.read_bytes() == (tmp_path / "cut2" / f.relative_to(tmp_path / "cut")).read_bytes(), f


def test_freeze_carries_end_of_utterance(tmp_path: Path) -> None:
    # Keep the call's bot turn (its text is the TTS script): a segment of a two-channel recording, with an end of
    # utterance.
    out, ctx = ingest(tmp_path)
    filt = step(
        ManifestFilterStep, ManifestFilterParams(min_duration=0.5, roles=["mono", "caller", "bot"]), out, tmp_path / "f"
    )
    split = step(
        SpeakerDisjointSplitStep,
        SpeakerDisjointSplitParams(validation_share=0.5, min_validation_utterances=1),
        filt,
        tmp_path / "s",
    )
    _, segs = seg.read(split)
    with_eou = [x for x in segs if "eou" in x]
    gaps = [x["eou"]["gapS"] for x in with_eou if "gapS" in x["eou"]]
    assert with_eou
    assert gaps
    assert all(x["role"] != "mono" for x in with_eou)
    draft = tmp_path / "draft"
    DatasetFreezeStep().run(DatasetFreezeParams(name="toy-eou"), {"segments": split}, {"dataset": draft}, ctx)
    h = json.loads((draft / "dataset.json").read_text(encoding="utf-8"))
    eou = h["stats"]["eou"]
    assert (eou["utterances"], eou["withGap"], eou["overlapping"]) == (len(with_eou), len(gaps), 0)
    assert eou["p50GapS"] == pytest.approx(float(np.percentile(gaps, 50)), abs=0.001)
    assert sum(eou["gapHistogram"]["counts"]) == len(gaps)
    assert "End of utterance" in (draft / "card.md").read_text(encoding="utf-8")
    dlines = [json.loads(x) for x in (draft / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert [x.get("eou") for x in dlines] == [x.get("eou") for x in segs]
    # The frozen cut (cadence.dataset/1) carries the same records and statistics.
    cut = tmp_path / "cut"
    p = DatasetFreezeParams(name="toy-eou", mode="cut", draft_version="ver_draft")
    DatasetFreezeStep().run(p, {"segments": split}, {"dataset": cut}, ctx)
    c = json.loads((cut / "dataset.json").read_text(encoding="utf-8"))
    clines = [json.loads(x) for x in (cut / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    assert [x.get("eou") for x in clines] == [x.get("eou") for x in segs]
    assert c["stats"]["eou"] == eou


def test_freeze_eou_stats_without_records() -> None:
    assert freeze_mod.eou_stats([{"duration": 1.0}]) is None
    assert freeze_mod.eou_of({"eou": {"speechEnd": 1.0, "nextSpeech": 0.5}}) == {"speechEnd": 1.0}  # no gap: no next
    st = freeze_mod.eou_stats(
        [{"eou": {"speechEnd": 1.0}}, {"eou": {"speechEnd": 2.0, "nextSpeech": 1.5, "gapS": -0.5}}]
    )
    assert st is not None
    assert (st["utterances"], st["withGap"], st["overlapping"]) == (2, 1, 1)


def test_cut_refuses_changed_audio(tmp_path: Path) -> None:
    split, ctx = prepared(tmp_path)
    mono_wav(tmp_path / "mnt" / "toy" / "r1" / "a.wav", bursts(16000, [(0.2, 1.8)], 2.0, 880), 16000)
    p = DatasetFreezeParams(mode="cut", draft_version="ver_x")
    with pytest.raises(StepInputError, match="no longer matches"):
        DatasetFreezeStep().run(p, {"segments": split}, {"dataset": tmp_path / "cut"}, ctx)


def test_freeze_refuses_unready_segments(tmp_path: Path) -> None:
    out, ctx = ingest(tmp_path)
    with pytest.raises(StepInputError, match="has no split"):
        DatasetFreezeStep().run(DatasetFreezeParams(), {"segments": out}, {"dataset": tmp_path / "d"}, ctx)
    split, _ = prepared(tmp_path / "again")
    with pytest.raises(StepInputError, match="draft_version"):
        DatasetFreezeStep().run(DatasetFreezeParams(mode="cut"), {"segments": split}, {"dataset": tmp_path / "e"}, ctx)


def test_member_line_keeps_annotated_entity_spans() -> None:
    # An annotation batch's rows carry entity spans; the dataset's manifest keeps them for entity_score.
    x: dict[str, Any] = {
        "duration": 2.0,
        "language": "sr-RS",
        "text": "Zovem se Ana.",
        "origin": "human",
        "split": "test",
        "uri": "mount://corpora/c.wav#t=0,2&ch=0",
        "role": "caller",
        "entities": [{"start": 9, "end": 12, "class": "name", "text": "Ana"}, "junk"],
    }
    assert member_line(x)["entities"] == [{"start": 9, "end": 12, "class": "name", "text": "Ana"}]
    assert "entities" not in member_line({**x, "entities": []})
