"""``dataset_import@3`` — the runtime-neutral import step (docs/spec/08-resolutions.md R17, R18).

Reads a corpus in one of three formats — a NeMo manifest, a Hugging Face dataset (FLEURS, Common Voice) or a folder
with ``metadata.csv`` — and writes a ``dataset`` directory artifact (docs/spec/02-domain-projects-registry.md "The
dataset artifact"):

    dataset.json      header: format, name, source {name, licence, kind, languages, url, revision, subset},
                      splitRule, counts per split, hours, tags, evalOnly
    manifest.jsonl    one line per utterance: audio (relative path), duration, sampleRate, channels, language,
                      speaker?, text, origin, split
    audio/<h2>/<h>.wav  the audio, 16-bit PCM WAV mono at ``sample_rate`` (h = sha256 of the file)

The control plane's ``dataset`` output hook turns the artifact into a Source, Utterances (by the BLAKE3 hash of each
audio file), Transcripts and a frozen dataset version. The step itself never touches the database or the API.
Help: docs/help/steps/dataset-import.md.
"""

from __future__ import annotations

import csv
import hashlib
import importlib
import json
import unicodedata
from collections.abc import Iterable, Iterator, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel, model_validator

from cadence_worker import audio
from cadence_worker.steps.base import cadence_field
from cadence_worker.translit import transliterate

FORMAT = "cadence.dataset/1"
SPLITS = ("train", "validation", "test")
TEXT_FIELDS = ("raw_transcription", "sentence", "text", "transcription", "normalized_text")
SPEAKER_FIELDS = ("speaker_id", "client_id", "speaker")
AUDIO_FIELDS = ("audio", "path", "file", "audio_filepath")
SPLIT_RULES = ("speaker-disjoint", "source", "all-train", "all-validation", "all-test")
AUDIO_SUFFIXES = (".wav", ".flac", ".mp3", ".ogg")


class DatasetImportParams(BaseModel):
    format: Literal["nemo-manifest", "hf-dataset", "folder-csv"] = cadence_field(
        "hf-dataset",
        description="Input format: a NeMo JSONL manifest, a Hugging Face dataset, or a folder with metadata.csv",
        source="Cadence recommendation",
        range={"values": ["nemo-manifest", "hf-dataset", "folder-csv"]},
    )
    name: str = cadence_field(
        "",
        description="Registry collection of the dataset version, without dataset/ (fleurs-he); empty = source name",
        source="Cadence recommendation",
        range={"pattern": "^([a-z0-9][a-z0-9._-]{0,98}[a-z0-9])?$"},
    )
    source_name: str = cadence_field(
        "",
        description="Registry source the utterances belong to (fleurs, common-voice-17); created on first import",
        source="docs/spec/08-resolutions.md R18",
        range={"pattern": "^[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$"},
    )
    source_kind: Literal["public", "production", "synthetic"] = cadence_field(
        "public",
        description="Kind of the source",
        source="docs/spec/02-domain-projects-registry.md, entity Source",
        range={"values": ["public", "production", "synthetic"]},
    )
    licence: str = cadence_field(
        "",
        description="Licence of the corpus as its card states it (CC-BY-4.0); required, recorded on the source",
        source="docs/spec/08-resolutions.md R18",
        range={"minLength": 1, "maxLength": 200},
    )
    source_url: str = cadence_field(
        "",
        description="Where the corpus comes from; empty derives hf://datasets/<repo> for Hugging Face imports",
        source="Cadence recommendation",
        range={"maxLength": 500},
    )
    locale: str = cadence_field(
        "",
        description="Language of every utterance (he-IL) unless the input or hf_configs names one",
        source="Cadence recommendation",
        range={"maxLength": 20},
    )
    path: str = cadence_field(
        "",
        description="nemo-manifest: the manifest file; folder-csv: the folder with metadata.csv (on the worker host)",
        source="Cadence recommendation",
        range={"maxLength": 1000},
    )
    hf_repo: str = cadence_field(
        "google/fleurs",
        description="Hugging Face dataset repository",
        source="docs/spec/08-resolutions.md R17 (FLEURS for replay and replay golden sets)",
        range={"maxLength": 200},
    )
    hf_config: str = cadence_field(
        "",
        description="Dataset configuration (he_il for FLEURS Hebrew); ignored when hf_configs is set",
        source="https://huggingface.co/datasets/google/fleurs",
        range={"maxLength": 100},
    )
    hf_configs: dict[str, str] = cadence_field(
        {},
        description="Several configurations imported into one dataset version, configuration → locale (de_de: de-DE)",
        source="docs/spec/08-resolutions.md R17 (one replay corpus across locales)",
        range="at most 100 entries",
    )
    hf_split: str = cadence_field(
        "train",
        description="Split of the Hugging Face dataset to read (train, validation, test)",
        source="Cadence recommendation",
        range={"maxLength": 40},
    )
    hf_revision: str = cadence_field(
        "",
        description="Pinned revision (commit) of the Hugging Face dataset; empty reads the default branch",
        source="Cadence recommendation (pin revisions for reproducible imports)",
        range={"maxLength": 100},
    )
    text_field: str = cadence_field(
        "",
        description="Column holding the transcript; empty picks raw_transcription, sentence, text or transcription",
        source="Cadence recommendation",
        range={"maxLength": 100},
    )
    max_hours: float = cadence_field(
        description="Cap on audio hours per language, in source order; 0 takes everything",
        default_ref="data.max_hours",
    )
    max_utterances: int = cadence_field(
        description="Cap on utterances per language, in source order; 0 takes everything",
        default_ref="data.max_utterances",
    )
    split_rule: Literal["speaker-disjoint", "source", "all-train", "all-validation", "all-test"] = cadence_field(
        "speaker-disjoint",
        description="How utterances get splits: speaker-disjoint hold-out, the source's own split, or all in one",
        source="docs/spec/03-pipelines-defaults.md starter pipeline (speaker_disjoint_split)",
        range={"values": list(SPLIT_RULES)},
    )
    validation_share: float = cadence_field(
        description="Share of speakers (or distinct transcripts without speakers) held out for validation",
        default_ref="data.validation_share",
    )
    min_validation_utterances: int = cadence_field(
        description="Fewest validation utterances of a speaker-disjoint split: when the share yields fewer, more whole "
        "speakers (or transcripts) move to validation, never past half the import",
        default_ref="data.min_validation_utterances",
    )
    sample_rate: int = cadence_field(
        description="Sample rate the audio is stored at (16-bit PCM WAV, mono)",
        default_ref="data.sample_rate",
    )
    text_normalisation: bool = cadence_field(
        description="Normalise transcripts (NFKC, case-folded, no punctuation); off keeps cased, punctuated text",
        default_ref="data.text_normalisation",
    )
    transliterate: Literal["", "sr-Cyrl-Latn"] = cadence_field(
        "",
        description="Convert transcripts to another script before anything else (sr-Cyrl-Latn: Serbian Cyrillic to "
        "Gaj Latin, for base models whose tokenizer lacks the Cyrillic letters); empty keeps the text's script",
        source="Cadence recommendation (the Serbian fine-tune on the test stand, 2026-10-01)",
        range={"values": ["", "sr-Cyrl-Latn"]},
    )
    eval_only: bool = cadence_field(
        False,
        description="Register for evaluation only (golden and replay test sets): never mixed or trained on",
        source="docs/spec/08-resolutions.md R17, R18",
        range={"values": [True, False]},
    )
    tags: list[str] = cadence_field(
        [],
        description="Tags for the dataset collection (golden, replay); eval-only is added when the version is",
        source="Cadence recommendation",
        range="at most 20 tags, each ≤ 40 characters",
    )
    purpose: Literal["speech", "noise"] = cadence_field(
        "speech",
        description="speech: utterances with transcripts; noise: background noise clips (no transcripts, all train) "
        "registered as a noise bank for augmentation",
        source="docs/spec/02-domain-projects-registry.md, entity Noise bank",
        range={"values": ["speech", "noise"]},
    )

    @model_validator(mode="after")
    def _check(self) -> DatasetImportParams:
        if not self.source_name:
            raise ValueError("source_name is required")
        if not self.licence.strip():
            raise ValueError("licence is required: an import carries the licence of its corpus")
        if self.format in ("nemo-manifest", "folder-csv") and not self.path:
            raise ValueError(f"format {self.format} needs path")
        if self.format == "hf-dataset" and not (self.hf_config or self.hf_configs):
            raise ValueError("format hf-dataset needs hf_config or hf_configs")
        if len(self.hf_configs) > 100 or len(self.tags) > 20 or any(len(t) > 40 for t in self.tags):
            raise ValueError("hf_configs takes at most 100 entries and tags at most 20 of ≤ 40 characters")
        return self


@dataclass
class Record:
    """One utterance as read from the input, before conversion."""

    audio: bytes | Path
    text: str
    language: str
    speaker: str = ""
    split: str = ""  # the source's own split, when it has one


class DatasetImportStep:
    version: ClassVar[str] = "3"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"dataset": "dataset"}
    resources: ClassVar[Mapping[str, Any]] = {"gpu": False, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    role: ClassVar[str] = ""
    Params: ClassVar[type[BaseModel]] = DatasetImportParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = DatasetImportParams.model_validate(params.model_dump())
        write_dataset(p, records(p), outputs["dataset"], ctx)


# ---------------------------------------------------------------- readers


def records(p: DatasetImportParams) -> Iterator[Record]:
    if p.format == "nemo-manifest":
        return _nemo(p)
    if p.format == "folder-csv":
        return _folder(p)
    return _hf(p)


def _nemo(p: DatasetImportParams) -> Iterator[Record]:
    manifest = Path(p.path)
    base = manifest.parent
    with manifest.open(encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            if not line.strip():
                continue
            row = json.loads(line)
            if not isinstance(row, dict) or "audio_filepath" not in row:
                raise ValueError(f"{manifest}:{n}: a NeMo manifest line needs audio_filepath")
            path = Path(str(row["audio_filepath"]))
            yield Record(
                audio=path if path.is_absolute() else base / path,
                text=str(row.get(p.text_field or "text", "")),
                language=str(row.get("lang") or row.get("language") or p.locale),
                speaker=str(row.get("speaker") or row.get("speaker_id") or ""),
                split=str(row.get("split") or ""),
            )


def _folder(p: DatasetImportParams) -> Iterator[Record]:
    base = Path(p.path)
    if p.purpose == "noise" and not (base / "metadata.csv").is_file():
        # A noise folder needs no metadata: every audio file under it, in path order (MUSAN's noise/ as extracted).
        for clip in sorted(x for x in base.rglob("*") if x.suffix.lower() in AUDIO_SUFFIXES and x.is_file()):
            yield Record(audio=clip, text="", language=p.locale or "und")
        return
    with (base / "metadata.csv").open(encoding="utf-8", newline="") as f:
        for n, row in enumerate(csv.DictReader(f), 2):
            file = _first(row, AUDIO_FIELDS)
            if not file:
                raise ValueError(f"{base / 'metadata.csv'}:{n}: no file column ({', '.join(AUDIO_FIELDS)})")
            yield Record(
                audio=base / file,
                text=_first(row, (p.text_field,) if p.text_field else TEXT_FIELDS),
                language=row.get("language") or p.locale,
                speaker=_first(row, SPEAKER_FIELDS),
                split=row.get("split") or "",
            )


def _first(row: Mapping[str, Any], fields: Iterable[str]) -> str:
    for k in fields:
        v = row.get(k)
        if v not in (None, ""):
            return str(v)
    return ""


def load_hf(repo: str, config: str, split: str, revision: str) -> Iterable[Mapping[str, Any]]:
    """Rows of a Hugging Face dataset with undecoded audio ({"bytes", "path"}); tests replace it."""
    try:
        datasets: Any = importlib.import_module("datasets")
    except ImportError as e:
        raise RuntimeError("format hf-dataset needs the datasets library (the NeMo Speech container has it)") from e
    ds = datasets.load_dataset(repo, config, split=split, revision=revision or None)
    if "audio" in ds.column_names:
        ds = ds.cast_column("audio", datasets.Audio(decode=False))
    rows: Iterable[Mapping[str, Any]] = ds
    return rows


def _hf(p: DatasetImportParams) -> Iterator[Record]:
    configs = dict(p.hf_configs) if p.hf_configs else {p.hf_config: p.locale}
    for config, locale in configs.items():
        for row in load_hf(p.hf_repo, config, p.hf_split, p.hf_revision):
            a = row.get("audio")
            data: bytes | Path
            if isinstance(a, Mapping) and a.get("bytes"):
                data = bytes(a["bytes"])
            elif isinstance(a, Mapping) and a.get("path"):
                data = Path(str(a["path"]))
            else:
                raise ValueError(f"{p.hf_repo} {config}: a row without audio bytes or path")
            yield Record(
                audio=data,
                text=_first(row, (p.text_field,) if p.text_field else TEXT_FIELDS),
                language=locale or str(row.get("locale") or row.get("language") or ""),
                speaker=_first(row, SPEAKER_FIELDS),
                split=p.hf_split,
            )


# ---------------------------------------------------------------- writing


def normalise_text(text: str, on: bool) -> str:
    """Always NFC and single spaces; with normalisation on also NFKC, case-folded and without punctuation."""
    t = unicodedata.normalize("NFC", text)
    if on:
        t = unicodedata.normalize("NFKC", t).casefold()
        t = "".join(" " if unicodedata.category(c).startswith("P") else c for c in t)
    return " ".join(t.split())


def _fraction(key: str) -> float:
    """A stable number in [0, 1) for a split group."""
    return int(hashlib.sha256(key.encode("utf-8")).hexdigest()[:15], 16) / float(1 << 60)


def assign_split(p: DatasetImportParams, r: Record, text: str) -> str:
    rule = p.split_rule
    if rule.startswith("all-"):
        return rule.removeprefix("all-")
    if rule == "source":
        s = {"dev": "validation", "valid": "validation", "val": "validation"}.get(r.split, r.split)
        return s if s in SPLITS else "train"
    # speaker-disjoint: a speaker (or, without speakers, a transcript) is wholly in train or in validation, so the same
    # voice or sentence never lands on both sides.
    return "validation" if _fraction(split_group(r.speaker, text)) < p.validation_share else "train"


def split_group(speaker: str, text: str) -> str:
    """What a speaker-disjoint split keeps together: the speaker, or without one the transcript."""
    return f"speaker:{speaker}" if speaker else f"text:{text.casefold()}"


def top_up_validation(lines: list[dict[str, Any]], minimum: int) -> int:
    """Move whole groups from train to validation, next in line by their split fraction, until validation holds
    ``minimum`` utterances — never past half of all utterances. Returns how many utterances moved."""
    have = sum(1 for x in lines if x["split"] == "validation")
    if have >= minimum:
        return 0
    groups: dict[str, list[dict[str, Any]]] = {}
    for x in lines:
        if x["split"] == "train":
            groups.setdefault(split_group(str(x.get("speaker") or ""), str(x["text"])), []).append(x)
    moved = 0
    for key in sorted(groups, key=_fraction):
        members = groups[key]
        if have >= minimum:
            break
        if 2 * (have + len(members)) > len(lines):
            continue  # a group this large would pass half; a smaller one next in line may still fit
        for x in members:
            x["split"] = "validation"
        have += len(members)
        moved += len(members)
    return moved


def _report(ctx: Any, fraction: float, message: str) -> None:
    progress = getattr(ctx, "progress", None)
    if callable(progress):
        progress(fraction, message)


def write_dataset(p: DatasetImportParams, rows: Iterable[Record], out: Path, ctx: Any = None) -> dict[str, Any]:
    """Convert and write the dataset artifact into the directory ``out``; returns the header."""
    out.mkdir(parents=True, exist_ok=True)
    lines: list[dict[str, Any]] = []
    seen: set[str] = set()
    per_lang: dict[str, tuple[int, float]] = {}
    skipped = {"empty text": 0, "duplicate audio": 0, "no language": 0, "cap": 0}
    noise = p.purpose == "noise"
    for r in rows:
        text = normalise_text(transliterate(r.text, p.transliterate), p.text_normalisation)
        language = r.language.strip() or ("und" if noise else "")
        if not text and not noise:
            skipped["empty text"] += 1
            continue
        if not language:
            skipped["no language"] += 1
            continue
        n, hours = per_lang.get(language, (0, 0.0))
        if (p.max_utterances and n >= p.max_utterances) or (p.max_hours and hours >= p.max_hours):
            skipped["cap"] += 1
            continue
        a = audio.canonical(audio.read(r.audio), p.sample_rate)
        if a.frames == 0:
            skipped["empty text"] += 1
            continue
        body = audio.wav_bytes(a)
        h = hashlib.sha256(body).hexdigest()
        if h in seen:
            skipped["duplicate audio"] += 1
            continue
        seen.add(h)
        rel = f"audio/{h[:2]}/{h}.wav"
        dst = out / rel
        dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_bytes(body)
        line: dict[str, Any] = {
            "audio": rel,
            "duration": a.duration,
            "sampleRate": a.sample_rate,
            "channels": 1,
            "language": language,
            "text": text,
            "origin": "human",
            "split": "train" if noise else assign_split(p, r, text),
        }
        if r.speaker:
            line["speaker"] = r.speaker
        lines.append(line)
        per_lang[language] = (n + 1, hours + a.duration / 3600)
        if len(lines) % 100 == 0:
            _report(ctx, 0.0, f"{len(lines)} utterances imported")
    if not lines:
        raise ValueError(f"no utterances to import (skipped: {skipped})")
    top_up = not noise and p.split_rule == "speaker-disjoint" and p.validation_share > 0
    if top_up and (moved := top_up_validation(lines, p.min_validation_utterances)):
        _report(ctx, 0.0, f"validation topped up by {moved} utterances to reach {p.min_validation_utterances}")
    counts = {s: sum(1 for x in lines if x["split"] == s) for s in SPLITS}
    tags = list(dict.fromkeys(p.tags))
    if p.eval_only and "eval-only" not in tags:
        tags.append("eval-only")
    if noise and "noise-bank" not in tags:
        tags.append("noise-bank")
    url = p.source_url or (f"hf://datasets/{p.hf_repo}" if p.format == "hf-dataset" else "")
    subset = ",".join(p.hf_configs) if p.hf_configs else p.hf_config
    header: dict[str, Any] = {
        "format": FORMAT,
        "source": {
            "name": p.source_name,
            "licence": p.licence.strip(),
            "kind": p.source_kind,
            "languages": sorted(per_lang),
            **({"url": url} if url else {}),
            **({"revision": p.hf_revision} if p.format == "hf-dataset" and p.hf_revision else {}),
            **({"subset": subset} if p.format == "hf-dataset" and subset else {}),
        },
        "splitRule": "all-train" if noise else p.split_rule,
        "counts": counts,
        "hours": sum(x["duration"] for x in lines) / 3600,
    }
    if p.name:
        header["name"] = p.name
    if tags:
        header["tags"] = tags
    if p.eval_only:
        header["evalOnly"] = True
    if noise:
        header["purpose"] = "noise"
    with (out / "manifest.jsonl").open("w", encoding="utf-8") as f:
        for x in lines:
            f.write(json.dumps(x, ensure_ascii=False, sort_keys=True) + "\n")
    (out / "dataset.json").write_text(json.dumps(header, ensure_ascii=False, indent=2, sort_keys=True) + "\n", "utf-8")
    _report(ctx, 1.0, f"{len(lines)} utterances, {header['hours']:.3f} h; skipped {skipped}")
    return header
