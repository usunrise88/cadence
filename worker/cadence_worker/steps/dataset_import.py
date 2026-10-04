"""``dataset_import@4`` — the runtime-neutral import step (docs/spec/08-resolutions.md R17, R18; docs/spec/03
"Interoperability").

Reads a corpus in one of six formats — a NeMo manifest (with ``offset``/``duration`` segments), a Hugging Face dataset
(FLEURS, Common Voice), a folder with ``metadata.csv``, a Lhotse CutSet (``cuts.jsonl[.gz]``), a Lhotse Shar
directory, or a Cadence bundle another instance exported (``datasets.export`` format ``cadence-bundle``) — from a path
on the worker host or a ``mount://`` URI on a path mount (version 4), and writes a ``dataset`` directory artifact
(docs/spec/02-domain-projects-registry.md "The dataset artifact"):

    dataset.json      header: format, name, source {name, licence, kind, languages, url, revision, subset},
                      splitRule, counts per split, hours, tags, evalOnly
    manifest.jsonl    one line per utterance: audio (relative path), duration, sampleRate, channels, language,
                      speaker?, text, origin, split
    audio/<h2>/<h>.wav  the audio, 16-bit PCM WAV mono at ``sample_rate`` (h = sha256 of the file)

The control plane's ``dataset`` output hook turns the artifact into a Source, Utterances (by the BLAKE3 hash of each
audio file), Transcripts and a frozen dataset version. The step itself never touches the database or the API.

A Cadence bundle keeps its identity: its audio is copied byte for byte, its splits, texts and origins are kept and its
source comes from the bundle's record, so the version registers with the same content fingerprint as on the instance
it came from. A Shar shard whose audio is already the canonical WAV (Cadence's own ``shar_export``) is copied byte for
byte too. Help: docs/help/steps/dataset-import.md.
"""

from __future__ import annotations

import csv
import gzip
import hashlib
import importlib
import io
import json
import re
import struct
import tarfile
import tempfile
import unicodedata
from collections.abc import Iterable, Iterator, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import IO, Any, ClassVar, Literal

from pydantic import BaseModel, model_validator

from cadence_worker import audio
from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.cas import decode_manifest
from cadence_worker.mounts import PATH_KINDS
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.translit import transliterate

FORMAT = "cadence.dataset/1"
SPLITS = ("train", "validation", "test")
TEXT_FIELDS = ("raw_transcription", "sentence", "text", "transcription", "normalized_text")
SPEAKER_FIELDS = ("speaker_id", "client_id", "speaker")
AUDIO_FIELDS = ("audio", "path", "file", "audio_filepath")
SPLIT_RULES = ("speaker-disjoint", "source", "all-train", "all-validation", "all-test")
AUDIO_SUFFIXES = (".wav", ".flac", ".mp3", ".ogg")
FORMATS = ("nemo-manifest", "hf-dataset", "folder-csv", "lhotse-cuts", "lhotse-shar", "cadence-bundle")
PATH_FORMATS = ("nemo-manifest", "folder-csv", "lhotse-cuts", "lhotse-shar", "cadence-bundle")
BUNDLE_FORMAT = "cadence.bundle/1"
ORIGIN = re.compile(r"^(human|pseudo-label|model:.+)$")


class DatasetImportParams(BaseModel):
    format: Literal["nemo-manifest", "hf-dataset", "folder-csv", "lhotse-cuts", "lhotse-shar", "cadence-bundle"] = (
        cadence_field(
            "hf-dataset",
            description="Input format: a NeMo JSONL manifest, a Hugging Face dataset, a folder with metadata.csv, a "
            "Lhotse CutSet, a Lhotse Shar directory or a Cadence bundle",
            source="docs/spec/03-pipelines-defaults.md Interoperability",
            range={"values": list(FORMATS)},
        )
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
        description="Licence of the corpus as its card states it (CC-BY-4.0); required (a bundle carries its own), "
        "recorded on the source",
        source="docs/spec/08-resolutions.md R18",
        range={"maxLength": 200},
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
        description="nemo-manifest: the manifest file; folder-csv: the folder with metadata.csv; lhotse-cuts: the cuts "
        "file or its folder; lhotse-shar and cadence-bundle: the folder — on the worker host, or "
        "mount://<mount>/<path> on a path mount",
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
        # A bundle carries its source, licence and kind in its record; the other formats name them.
        if not self.source_name and self.format != "cadence-bundle":
            raise ValueError("source_name is required")
        if not self.licence.strip() and self.format != "cadence-bundle":
            raise ValueError("licence is required: an import carries the licence of its corpus")
        if self.format in PATH_FORMATS and not self.path:
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
    offset: float = 0.0  # a segment of the file: its start in seconds
    duration: float | None = None  # and its length (None: to the end)
    channel: int | None = None  # one channel of a multi-channel file (None: all, mixed down)
    origin: str = "human"
    verbatim: bool = False  # keep canonical WAV bytes as they are (Shar audio Cadence exported)


class DatasetImportStep:
    version: ClassVar[str] = "4"
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
        if p.format == "cadence-bundle":
            import_bundle(p, local_path(p.path, ctx), outputs["dataset"], ctx)
            return
        full: set[str] = set()
        write_dataset(p, records(p, full, ctx), outputs["dataset"], ctx, full)


# ---------------------------------------------------------------- readers


def local_path(path: str, ctx: Any = None) -> Path:
    """A path on the worker host, or a mount URI on a path mount of the lease (local, nfs, smb)."""
    if not path.startswith(mounts.SCHEME):
        return Path(path)
    ms = mounts.mounts_of(ctx)
    ref = mounts.parse(path)
    kind = next((str(m.get("kind")) for m in ms if m.get("name") == ref.name), "")
    if kind and kind not in PATH_KINDS:
        raise StepInputError(
            f"{path}: mount {ref.name} is {kind}; imports read path mounts (local, nfs, smb) — ingest it with "
            "sdp_ingest or copy it to a path mount"
        )
    if ref.start is not None or ref.channel is not None:
        raise StepInputError(f"{path}: an import path takes no #t or ch fragment")
    return mounts.resolve(path, ms)


def records(p: DatasetImportParams, full: set[str] | None = None, ctx: Any = None) -> Iterator[Record]:
    """The input's records in source order. full is the set of languages write_dataset has capped (max_hours,
    max_utterances): a reader that knows a row's language before reading its audio stops reading that language."""
    if p.format == "nemo-manifest":
        return _nemo(p, local_path(p.path, ctx))
    if p.format == "folder-csv":
        return _folder(p, local_path(p.path, ctx))
    if p.format == "lhotse-cuts":
        return _lhotse_cuts(p, local_path(p.path, ctx))
    if p.format == "lhotse-shar":
        return _lhotse_shar(p, local_path(p.path, ctx))
    return _hf(p, full if full is not None else set())


def _nemo(p: DatasetImportParams, manifest: Path) -> Iterator[Record]:
    base = manifest.parent
    with manifest.open(encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            if not line.strip():
                continue
            row = json.loads(line)
            if not isinstance(row, dict) or "audio_filepath" not in row:
                raise ValueError(f"{manifest}:{n}: a NeMo manifest line needs audio_filepath")
            path = Path(str(row["audio_filepath"]))
            offset = row.get("offset")
            yield Record(
                audio=path if path.is_absolute() else base / path,
                text=str(row.get(p.text_field or "text", "")),
                language=str(row.get("lang") or row.get("language") or p.locale),
                speaker=str(row.get("speaker") or row.get("speaker_id") or ""),
                split=str(row.get("split") or ""),
                # A line with an offset is a segment of its file: offset and duration in seconds (NeMo's convention).
                offset=float(offset) if offset is not None else 0.0,
                duration=float(row["duration"]) if offset is not None and row.get("duration") is not None else None,
            )


def _open_text(path: Path) -> IO[str]:
    if path.suffix == ".gz":
        return io.TextIOWrapper(gzip.open(path, "rb"), encoding="utf-8")
    return path.open(encoding="utf-8")


def _cut_record(p: DatasetImportParams, cut: Mapping[str, Any], audio_of: Any, where: str) -> Record:
    """A Lhotse cut (MonoCut or MixedCut-free CutSet line) as a record: its supervisions' texts joined in time order,
    the first supervision's language and speaker, the cut's span of its recording and its channel."""
    sups = sorted(
        (s for s in cut.get("supervisions") or [] if isinstance(s, Mapping)), key=lambda s: float(s.get("start", 0))
    )
    text = " ".join(str(s.get("text") or "") for s in sups).strip()
    first = sups[0] if sups else {}
    custom = cut.get("custom") if isinstance(cut.get("custom"), Mapping) else {}
    origin = str((custom or {}).get("origin") or "human")
    channel = cut.get("channel")
    return Record(
        audio=audio_of(cut),
        text=str(first.get(p.text_field) or text) if p.text_field else text,
        language=str(first.get("language") or p.locale),
        speaker=str(first.get("speaker") or ""),
        split=str((custom or {}).get("split") or ""),
        origin=origin if ORIGIN.match(origin) else "human",
        **_span(cut, where),
        channel=int(channel) if isinstance(channel, int) else None,
    )


def _span(cut: Mapping[str, Any], where: str) -> dict[str, Any]:
    try:
        return {"offset": float(cut.get("start") or 0.0), "duration": float(cut["duration"])}
    except (KeyError, TypeError, ValueError) as e:
        raise StepInputError(f"{where}: a cut needs start and duration") from e


def _cuts_files(path: Path) -> list[Path]:
    if path.is_file():
        return [path]
    found = sorted(path.glob("cuts*.jsonl.gz")) or sorted(path.glob("cuts*.jsonl"))
    if not found:
        raise StepInputError(f"{path}: no cuts*.jsonl[.gz] (a Lhotse CutSet)")
    return found


def _lhotse_cuts(p: DatasetImportParams, path: Path) -> Iterator[Record]:
    """A Lhotse CutSet: each cut's recording source of type file (relative to the cuts file), its start and
    duration within the recording and its channel."""
    for cuts in _cuts_files(path):
        with _open_text(cuts) as f:
            for n, line in enumerate(f, 1):
                if not line.strip():
                    continue
                cut = json.loads(line)
                where = f"{cuts.name}:{n}"
                if not isinstance(cut, dict) or not isinstance(cut.get("recording"), Mapping):
                    raise StepInputError(f"{where}: a cut needs its recording (MixedCuts are not imported)")

                def audio_of(c: Mapping[str, Any], base: Path = cuts.parent, at: str = where) -> Path:
                    srcs = [s for s in c["recording"].get("sources") or [] if isinstance(s, Mapping)]
                    if not srcs or srcs[0].get("type") != "file":
                        raise StepInputError(
                            f"{at}: only recordings with a file source are imported (Shar: lhotse-shar)"
                        )
                    src = Path(str(srcs[0]["source"]))
                    return src if src.is_absolute() else base / src

                yield _cut_record(p, cut, audio_of, where)


def _tar_pairs(tar: tarfile.TarFile) -> Iterator[tuple[str, bytes, dict[str, Any] | None]]:
    """A Shar tar's members in pairs: the data file (<cut id>.<ext>) then its manifest (<cut id>.json)."""
    members = [m for m in tar.getmembers() if m.isfile()]
    for i in range(0, len(members) - 1, 2):
        data, meta = members[i], members[i + 1]
        fd, fm = tar.extractfile(data), tar.extractfile(meta)
        if fd is None or fm is None:
            continue
        manifest = json.loads(fm.read()) if meta.name.endswith(".json") else None
        yield data.name, fd.read(), manifest if isinstance(manifest, dict) else None


def _lhotse_shar(p: DatasetImportParams, path: Path) -> Iterator[Record]:
    """A Lhotse Shar directory (or a parent of one per split: train/, validation/, test/): cuts.NNNNNN.jsonl.gz beside
    recording.NNNNNN.tar, the audio of each cut stored in the tar under its id."""
    dirs = [path] if sorted(path.glob("cuts.*.jsonl.gz")) else [path / s for s in SPLITS if (path / s).is_dir()]
    if not dirs:
        raise StepInputError(f"{path}: no cuts.NNNNNN.jsonl.gz (a Lhotse Shar directory)")
    for d in dirs:
        split = d.name if d != path and d.name in SPLITS else ""
        for cuts in sorted(d.glob("cuts.*.jsonl.gz")):
            tar_path = d / cuts.name.replace("cuts.", "recording.").replace(".jsonl.gz", ".tar")
            if not tar_path.is_file():
                raise StepInputError(f"{d.name}/{cuts.name}: no {tar_path.name} beside it")
            with tarfile.open(tar_path) as tar:
                audio_by_id = {Path(name).stem: body for name, body, _ in _tar_pairs(tar)}
            with _open_text(cuts) as f:
                for n, line in enumerate(f, 1):
                    if not line.strip():
                        continue
                    cut = json.loads(line)
                    where = f"{d.name}/{cuts.name}:{n}"
                    cid = str(cut.get("id") or "")
                    if cid not in audio_by_id:
                        raise StepInputError(f"{where}: cut {cid!r} has no audio in {tar_path.name}")
                    # Shar stores each cut's own audio: the cut starts at 0 of it.
                    r = _cut_record(p, {**cut, "start": 0.0}, lambda _c, b=audio_by_id[cid]: b, where)
                    r.split = r.split or split
                    r.verbatim = True
                    yield r


def _folder(p: DatasetImportParams, base: Path) -> Iterator[Record]:
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


def load_hf(repo: str, config: str, split: str, revision: str, streaming: bool = False) -> Iterable[Mapping[str, Any]]:
    """Rows of a Hugging Face dataset with undecoded audio ({"bytes", "path"}); tests replace it. Streaming reads rows
    as they are needed instead of downloading the whole split first (a FLEURS locale is ≈ 5 GB; a capped replay import
    needs an hour of it)."""
    try:
        datasets: Any = importlib.import_module("datasets")
    except ImportError as e:
        raise RuntimeError("format hf-dataset needs the datasets library (the NeMo Speech container has it)") from e
    ds = datasets.load_dataset(repo, config, split=split, revision=revision or None, streaming=streaming)
    if "audio" in ds.column_names:
        ds = ds.cast_column("audio", datasets.Audio(decode=False))
    rows: Iterable[Mapping[str, Any]] = ds
    return rows


def _hf(p: DatasetImportParams, full: set[str]) -> Iterator[Record]:
    configs = dict(p.hf_configs) if p.hf_configs else {p.hf_config: p.locale}
    capped = bool(p.max_hours or p.max_utterances)
    for config, locale in configs.items():
        for row in load_hf(p.hf_repo, config, p.hf_split, p.hf_revision, streaming=capped):
            if locale and locale in full:
                break  # this configuration's language is full: read no further (with streaming, download no further)
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


# ---------------------------------------------------------------- audio


def load_audio(data: bytes | Path) -> audio.Audio:
    """Decode a file or bytes: WAV in pure Python, other formats through soundfile, else ffmpeg (as sdp_ingest)."""
    try:
        return audio.read(data)
    except audio.AudioError:
        with tempfile.TemporaryDirectory(prefix="import-") as tmp:
            src = data if isinstance(data, Path) else Path(tmp) / "input.bin"
            if isinstance(data, bytes):
                src.write_bytes(data)
            d = seg.decode(src, Path(tmp))
        import numpy as np

        return audio.Audio(
            samples=np.stack(d.channels, axis=1).reshape(-1), sample_rate=d.rate, channels=len(d.channels)
        )


def cut_audio(a: audio.Audio, r: Record) -> audio.Audio:
    """The record's span [offset, offset + duration) of a, and its channel when it names one."""
    if r.offset <= 0 and r.duration is None and r.channel is None:
        return a
    c = a.channels
    i0 = max(0, round(r.offset * a.sample_rate))
    i1 = a.frames if r.duration is None else min(a.frames, i0 + round(r.duration * a.sample_rate))
    if i0 >= a.frames:
        raise StepInputError(f"a segment starts at {r.offset} s, past the end of its {a.duration:.3f} s file")
    s = a.samples[i0 * c : i1 * c]
    if r.channel is not None and c > 1:
        if not 0 <= r.channel < c:
            raise StepInputError(f"channel {r.channel} of a {c}-channel file")
        return audio.Audio(samples=s[r.channel :: c], sample_rate=a.sample_rate, channels=1)
    return audio.Audio(samples=s, sample_rate=a.sample_rate, channels=c)


def verbatim_wav(r: Record, rate: int) -> bytes | None:
    """The record's audio as it is when it may be kept byte for byte: verbatim, a whole file and already the canonical
    WAV (mono 16-bit PCM at rate, the 44-byte header :func:`audio.wav_bytes` writes)."""
    if not r.verbatim or r.channel not in (None, 0) or r.offset > 0:
        return None
    raw = r.audio.read_bytes() if isinstance(r.audio, Path) else r.audio
    if len(raw) < 44 or (len(raw) - 44) % 2:
        return None
    n = len(raw) - 44
    canonical = struct.pack(
        "<4sI4s4sIHHIIHH4sI", b"RIFF", 36 + n, b"WAVE", b"fmt ", 16, 1, 1, rate, rate * 2, 2, 16, b"data", n
    )
    return raw if raw[:44] == canonical and n > 0 else None


# ---------------------------------------------------------------- Cadence bundles


def import_bundle(p: DatasetImportParams, root: Path, out: Path, ctx: Any = None) -> dict[str, Any]:
    """Re-create the dataset artifact a bundle holds (``datasets.export`` format ``cadence-bundle``): every file of
    the artifact from ``cas/b3/…`` byte for byte, the header's source completed from the bundle's record (name, licence,
    kind, languages, url) so the control plane registers the same source, splits, texts and identity. ``name`` and
    ``tags`` may rename and tag the collection; nothing else is rewritten."""
    f = root / "bundle.json"
    if not f.is_file():
        raise StepInputError(f"{root}: no bundle.json (a Cadence bundle from datasets.export)")
    doc = json.loads(f.read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("format") != BUNDLE_FORMAT:
        raise StepInputError(f"bundle.json is not {BUNDLE_FORMAT}")
    found = doc.get("record")
    record: dict[str, Any] = found if isinstance(found, dict) else {}
    sources = [s for s in record.get("sources") or [] if isinstance(s, dict)]
    blob = root / "cas" / "b3"
    artifact = str(doc.get("artifact") or "")
    hexd = artifact.removeprefix("b3:")
    man = blob / hexd[:2] / hexd
    if not man.is_file():
        raise StepInputError(f"bundle: the dataset manifest {artifact} is not under cas/b3")
    files = decode_manifest(man.read_bytes())
    out.mkdir(parents=True, exist_ok=True)
    for i, mf in enumerate(files, 1):
        h = mf.hash.removeprefix("b3:")
        src = blob / h[:2] / h
        if not src.is_file() or src.stat().st_size != mf.size:
            raise StepInputError(f"bundle: {mf.path} ({mf.hash}) is missing from cas/b3 or has another size")
        dst = out.joinpath(*mf.path.split("/"))
        dst.parent.mkdir(parents=True, exist_ok=True)
        dst.write_bytes(src.read_bytes())
        if i % 500 == 0:
            _report(ctx, i / len(files), f"{i}/{len(files)} files of the bundle")
    header = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    if not isinstance(header, dict) or header.get("format") != FORMAT:
        raise StepInputError(f"the bundle's dataset.json is not {FORMAT}")
    src_name = str((header.get("source") or {}).get("name") or "")
    source = next((s for s in sources if s.get("name") == src_name), sources[0] if len(sources) == 1 else None)
    if source is None:
        raise StepInputError(f"the bundle's record names no source {src_name!r}")
    if p.source_name and p.source_name != source.get("name"):
        raise StepInputError(f"source_name {p.source_name!r} differs from the bundle's source {source.get('name')!r}")
    licence = str(source.get("licence") or "")
    if p.licence.strip() and p.licence.strip() != licence:
        raise StepInputError(f"licence {p.licence!r} differs from the bundle's {licence!r}; a bundle keeps its licence")
    header["source"] = {
        "name": str(source["name"]),
        "licence": licence,
        "kind": str(source.get("kind") or "public"),
        "languages": sorted(str(x) for x in source.get("languages") or []),
        **({"url": str(source["url"])} if source.get("url") else {}),
    }
    # A cut from an ingest names the draft it froze on the other instance: here it is an import.
    for key in ("draftVersionId", "sourceInfo", "steps"):
        header.pop(key, None)
    collection = str(record.get("collection") or "").removeprefix("dataset/")
    header["name"] = p.name or str(header.get("name") or collection or source["name"])
    tags = list(dict.fromkeys([*(header.get("tags") or []), *p.tags]))
    if tags:
        header["tags"] = tags
    if p.eval_only:
        header["evalOnly"] = True
    (out / "dataset.json").write_text(json.dumps(header, ensure_ascii=False, indent=2, sort_keys=True) + "\n", "utf-8")
    _report(ctx, 1.0, f"bundle of {collection or header['name']}: {len(files)} files")
    return header


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


def write_dataset(
    p: DatasetImportParams, rows: Iterable[Record], out: Path, ctx: Any = None, full: set[str] | None = None
) -> dict[str, Any]:
    """Convert and write the dataset artifact into the directory ``out``; returns the header. A language that reaches
    its cap joins ``full``, so the reader can stop reading it."""
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
            if full is not None:
                full.add(language)
            continue
        body = verbatim_wav(r, p.sample_rate)
        if body is None:
            a = audio.canonical(cut_audio(load_audio(r.audio), r), p.sample_rate)
            if a.frames == 0:
                skipped["empty text"] += 1
                continue
            body = audio.wav_bytes(a)
            seconds = a.duration
        else:
            seconds = (len(body) - 44) // 2 / p.sample_rate
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
            "duration": seconds,
            "sampleRate": p.sample_rate,
            "channels": 1,
            "language": language,
            "text": text,
            "origin": r.origin,
            "split": "train" if noise else assign_split(p, r, text),
        }
        if r.speaker:
            line["speaker"] = r.speaker
        lines.append(line)
        per_lang[language] = (n + 1, hours + seconds / 3600)
        if full is not None and (
            (p.max_utterances and n + 1 >= p.max_utterances) or (p.max_hours and hours + seconds / 3600 >= p.max_hours)
        ):
            full.add(language)
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
