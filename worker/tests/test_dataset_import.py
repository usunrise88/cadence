from __future__ import annotations

import hashlib
import json
import struct
from collections.abc import Iterable, Mapping
from pathlib import Path
from typing import Any, cast

import pytest

from cadence_worker import audio, translit
from cadence_worker.__main__ import registry
from cadence_worker.steps import dataset_import as di
from cadence_worker.steps.base import missing_metadata

FIX = Path(__file__).parent / "fixtures" / "fleurs-sr-rs"
DEFAULTS = Path(__file__).resolve().parents[2] / "control-plane" / "defaults" / "defaults.yaml"


def params(**kw: Any) -> di.DatasetImportParams:
    base: dict[str, Any] = {"source_name": "fleurs", "licence": "CC-BY-4.0", "locale": "sr-RS"}
    base.update(kw)
    return di.DatasetImportParams(**base)


def run(tmp_path: Path, p: di.DatasetImportParams) -> tuple[dict[str, Any], list[dict[str, Any]], Path]:
    out = tmp_path / "dataset"
    di.DatasetImportStep().run(p, {}, {"dataset": out})
    header = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    lines = [json.loads(x) for x in (out / "manifest.jsonl").read_text(encoding="utf-8").splitlines()]
    return header, lines, out


def test_registry_publishes_the_new_contract_shape() -> None:
    reg = registry()["dataset_import"]
    assert reg["version"] == "3"
    assert reg["produces"] == {"dataset": "dataset"}
    assert di.DatasetImportStep.produces == {"dataset": "dataset"}
    assert di.DatasetImportStep.consumes == {}
    assert di.DatasetImportStep.neutral is True
    assert missing_metadata(cast(Any, di.DatasetImportStep)) == []


def test_default_refs_resolve_in_defaults_yaml() -> None:
    import yaml

    doc = yaml.safe_load(DEFAULTS.read_text(encoding="utf-8"))
    props = di.DatasetImportParams.model_json_schema()["properties"]
    refs = {k: v["x-cadence"]["defaultRef"] for k, v in props.items() if "defaultRef" in v["x-cadence"]}
    assert set(refs) == {
        "max_hours",
        "max_utterances",
        "validation_share",
        "min_validation_utterances",
        "sample_rate",
        "text_normalisation",
    }
    for field, ref in refs.items():
        section, key = ref.split(".")
        assert doc[section][key]["value"] == props[field]["default"], ref


def test_folder_csv_writes_the_dataset_artifact(tmp_path: Path) -> None:
    p = params(format="folder-csv", path=str(FIX), split_rule="source", name="fleurs-sr")
    header, lines, out = run(tmp_path, p)
    assert header["format"] == "cadence.dataset/1"
    assert header["name"] == "fleurs-sr"
    assert header["source"] == {"name": "fleurs", "licence": "CC-BY-4.0", "kind": "public", "languages": ["sr-RS"]}
    assert header["counts"] == {"train": 0, "validation": 0, "test": 3}
    assert header["splitRule"] == "source"
    assert abs(header["hours"] - 3 * 1.2 / 3600) < 1e-9
    assert len(lines) == 3
    for line in lines:
        assert set(line) == {"audio", "duration", "sampleRate", "channels", "language", "text", "origin", "split"}
        assert line["origin"] == "human"
        assert line["sampleRate"] == 16000
        assert line["duration"] == pytest.approx(1.2)
        wav = (out / line["audio"]).read_bytes()
        assert wav[:4] == b"RIFF"
        assert struct.unpack_from("<HHI", wav, 20) == (1, 1, 16000)
    first_csv_text = (FIX / "metadata.csv").read_text(encoding="utf-8").splitlines()[1].split(',"')[1].split('",')[0]
    assert lines[0]["text"] == first_csv_text


def test_float_wav_decodes_like_its_pcm_twin(tmp_path: Path) -> None:
    a = audio.read(FIX / "audio" / "clip3.wav")
    assert (a.sample_rate, a.channels, a.frames) == (16000, 1, 19200)
    again = audio.read(audio.wav_bytes(audio.canonical(a, 16000)))
    assert audio.wav_bytes(again) == audio.wav_bytes(audio.canonical(a, 16000))


def test_import_is_deterministic(tmp_path: Path) -> None:
    _, first, _ = run(tmp_path / "a", params(format="folder-csv", path=str(FIX)))
    _, second, _ = run(tmp_path / "b", params(format="folder-csv", path=str(FIX)))
    assert first == second


def test_speaker_disjoint_keeps_a_speaker_on_one_side(tmp_path: Path) -> None:
    p = params(format="nemo-manifest", path=str(FIX / "manifest.json"), validation_share=0.5)
    header, lines, _ = run(tmp_path, p)
    by_speaker: dict[str, set[str]] = {}
    for line in lines:
        by_speaker.setdefault(line["speaker"], set()).add(line["split"])
    assert all(len(s) == 1 for s in by_speaker.values())
    assert sum(header["counts"].values()) == 3


def test_without_speakers_the_same_sentence_stays_together(tmp_path: Path) -> None:
    for share in (0.0, 0.3, 0.5):
        _, lines, _ = run(tmp_path / str(share), params(format="folder-csv", path=str(FIX), validation_share=share))
        assert lines[0]["split"] == lines[1]["split"], "clips 1 and 2 read the same sentence"
    _, lines, _ = run(tmp_path / "zero", params(format="folder-csv", path=str(FIX), validation_share=0.0))
    assert {x["split"] for x in lines} == {"train"}


def test_validation_is_topped_up_with_whole_groups() -> None:
    def lines() -> list[dict[str, Any]]:
        # 40 speakers of 5 utterances, all in train (as a tiny share would leave them).
        return [{"split": "train", "speaker": f"s{i // 5}", "text": f"t{i}"} for i in range(200)]

    got = lines()
    assert di.top_up_validation(got, 23) == 25  # five whole speakers: the first that reaches 23
    val = [x for x in got if x["split"] == "validation"]
    assert len(val) == 25
    assert all(len({x["split"] for x in got if x["speaker"] == s}) == 1 for s in {x["speaker"] for x in got})
    # The next speakers in line are the ones with the lowest split fraction, as the share would have picked them.
    order = sorted({x["speaker"] for x in got}, key=lambda s: di._fraction(di.split_group(s, "")))
    assert {x["speaker"] for x in val} == set(order[:5])
    # Never past half of the import; enough already moves nothing.
    capped = lines()
    assert di.top_up_validation(capped, 1000) == 100
    assert di.top_up_validation(capped, 50) == 0
    # Without speakers a transcript is the group.
    plain = [{"split": "train", "text": t} for t in ("a", "A", "b", "c")]
    assert di.top_up_validation(plain, 1) in (1, 2)
    assert plain[0]["split"] == plain[1]["split"], "the same sentence (case-folded) stays together"


def test_import_tops_up_a_small_validation_split(tmp_path: Path) -> None:
    p = params(format="folder-csv", path=str(FIX), validation_share=0.0001, min_validation_utterances=1)
    header, lines, _ = run(tmp_path, p)
    assert header["counts"]["validation"] >= 1
    assert header["counts"]["validation"] * 2 <= len(lines)
    off = params(format="folder-csv", path=str(FIX), validation_share=0.0001, min_validation_utterances=0)
    header, _, _ = run(tmp_path / "off", off)
    assert header["counts"]["validation"] == 0


def test_caps_and_eval_only(tmp_path: Path) -> None:
    p = params(format="folder-csv", path=str(FIX), max_utterances=2, split_rule="all-test", eval_only=True)
    header, lines, _ = run(tmp_path, p.model_copy(update={"tags": ["golden"]}))
    assert len(lines) == 2
    assert header["counts"]["test"] == 2
    assert header["evalOnly"] is True
    assert header["tags"] == ["golden", "eval-only"]
    header, lines, _ = run(tmp_path / "h", params(format="folder-csv", path=str(FIX), max_hours=1.5 / 3600))
    assert len(lines) == 2, "the cap is checked before each utterance"


def test_hf_dataset_reads_fleurs_rows(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    clips = [(FIX / "audio" / f"clip{i}.wav").read_bytes() for i in (1, 2, 3)]
    calls: list[tuple[str, str, str, str]] = []

    def fake(repo: str, config: str, split: str, revision: str) -> Iterable[Mapping[str, Any]]:
        calls.append((repo, config, split, revision))
        return [
            {
                "id": i,
                "audio": {"bytes": b, "path": f"{i}.wav"},
                "raw_transcription": f"Rečenica {config} {i}.",
                "transcription": "ignored",
            }
            for i, b in enumerate(clips if config == "sr_rs" else clips[:1])
        ]

    monkeypatch.setattr(di, "load_hf", fake)
    p = params(
        format="hf-dataset",
        hf_configs={"sr_rs": "sr-RS", "hr_hr": "hr-HR"},
        hf_split="test",
        hf_revision="70bb2e84b976b7e960aa89f1c648e09c59f894dd",
        split_rule="source",
        max_utterances=2,
        locale="",
    )
    header, lines, _ = run(tmp_path, p)
    assert calls == [
        ("google/fleurs", "sr_rs", "test", "70bb2e84b976b7e960aa89f1c648e09c59f894dd"),
        ("google/fleurs", "hr_hr", "test", "70bb2e84b976b7e960aa89f1c648e09c59f894dd"),
    ]
    assert header["source"]["url"] == "hf://datasets/google/fleurs"
    assert header["source"]["subset"] == "sr_rs,hr_hr"
    assert header["source"]["languages"] == ["sr-RS"], "languages are the ones imported"
    # The hr_hr row repeats clip 1's audio, already imported for sr_rs: one utterance per audio.
    assert [x["language"] for x in lines] == ["sr-RS", "sr-RS"]
    assert lines[0]["text"] == "Rečenica sr_rs 0."
    assert {x["split"] for x in lines} == {"test"}


def test_text_normalisation() -> None:
    assert di.normalise_text("  Šengenska\nzona,  međutim. ", False) == "Šengenska zona, međutim."
    assert di.normalise_text("Šengenska zona, međutim.", True) == "šengenska zona međutim"


def test_noise_purpose_imports_a_folder_of_clips_as_a_noise_bank(tmp_path: Path) -> None:
    """purpose noise: every audio file of a folder without metadata.csv (MUSAN's noise/ as extracted), no transcripts,
    all train, language und, tagged noise-bank; the control plane's hook registers it as a noise-bank version."""
    folder = tmp_path / "musan" / "noise" / "free-sound"
    folder.mkdir(parents=True)
    for i, clip in enumerate(sorted((FIX / "audio").glob("*.wav"))[:2]):
        (folder / f"noise-free-sound-{i:04d}.wav").write_bytes(clip.read_bytes())
    (folder / "LICENSE").write_text("CC BY 4.0", encoding="utf-8")
    p = di.DatasetImportParams(
        format="folder-csv",
        path=str(tmp_path / "musan" / "noise"),
        purpose="noise",
        name="musan-noise",
        source_name="musan",
        licence="CC-BY-4.0",
        source_url="https://www.openslr.org/17/",
    )
    header, lines, _ = run(tmp_path, p)
    assert header["purpose"] == "noise"
    assert header["splitRule"] == "all-train"
    assert header["tags"] == ["noise-bank"]
    assert header["source"]["languages"] == ["und"]
    assert len(lines) == 2
    assert {(x["text"], x["split"], x["language"]) for x in lines} == {("", "train", "und")}
    # Speech imports still drop clips without a transcript.
    with pytest.raises(FileNotFoundError):
        run(tmp_path / "speech", p.model_copy(update={"purpose": "speech"}))


def test_params_need_a_source_and_its_licence() -> None:
    with pytest.raises(ValueError, match="licence"):
        di.DatasetImportParams(source_name="fleurs", format="folder-csv", path="x")
    with pytest.raises(ValueError, match="needs path"):
        di.DatasetImportParams(source_name="fleurs", licence="CC-BY-4.0", format="folder-csv")
    with pytest.raises(ValueError, match="hf_config"):
        di.DatasetImportParams(source_name="fleurs", licence="CC-BY-4.0")


def test_canonical_wav_bytes_are_pinned() -> None:
    # The canonical WAV is an utterance's identity: the same clip must give the same bytes on every host and version.
    a = audio.read(FIX / "audio" / "clip1.wav")
    assert hashlib.sha256(audio.wav_bytes(audio.canonical(a, 16000))).hexdigest().startswith("213f6c160894975e")


def test_transliterate_writes_serbian_latin(tmp_path: Path) -> None:
    _, cyrl, _ = run(tmp_path / "c", params(format="folder-csv", path=str(FIX), split_rule="source"))
    _, latn, _ = run(
        tmp_path / "l", params(format="folder-csv", path=str(FIX), split_rule="source", transliterate="sr-Cyrl-Latn")
    )
    assert any(any("Ѐ" <= ch <= "ӿ" for ch in x["text"]) for x in cyrl), "the fixture is Cyrillic"
    for src, out in zip(cyrl, latn, strict=True):
        assert not any("Ѐ" <= ch <= "ӿ" for ch in out["text"]), out["text"]
        assert out["text"] == translit.sr_cyrl_to_latn(src["text"])


@pytest.mark.parametrize(
    ("cyrl", "latn"),
    [
        ("Љубав и џеп", "Ljubav i džep"),
        ("ЊЕГОШ", "NJEGOŠ"),
        ("Ђорђе, ћуприја!", "Đorđe, ćuprija!"),
        ("abc 123", "abc 123"),
    ],
)
def test_sr_cyrl_to_latn(cyrl: str, latn: str) -> None:
    assert translit.sr_cyrl_to_latn(cyrl) == latn
    assert translit.transliterate(cyrl, "") == cyrl
