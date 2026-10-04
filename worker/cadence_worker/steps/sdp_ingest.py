"""``sdp_ingest@2`` — index audio on a mount in place (docs/review/2026-10-03-phase-4-plan.md, decision 3).

Walks a mount path, decodes every audio file (WAV in pure Python; μ-law, FLAC, MP3, OGG/Opus, M4A through ffmpeg),
splits stereo recordings into one track per party (roles from a ``<stem>.cadence.json`` sidecar or the
``channel_roles`` parameter), resamples each track to 16 kHz, finds speech with a per-channel energy detector and
cuts it into segments. The bot's channel is self-labelled from the TTS script in the sidecar (origin
``model:tts-script``); a ``<stem>.txt`` sidecar is the whole file's human transcript.

It writes only a ``segments`` artifact (``cadence.segments/1``, :mod:`cadence_worker.segments`): ``mount://`` URIs
with time ranges and channels, each segment's canonical hash (BLAKE3 of its 16 kHz 16-bit mono WAV) and ``file-b3``,
the canonical hash of the whole track it was cut from (what an import of the file hashes, so the leakage check finds a
golden set's audio re-cut from a mount) — no audio is copied. ``dataset_freeze`` cuts the same bytes later.

Version 2 adds ``exclude`` (a corpus's ``test/`` split is left out by default: it is where golden sets come from) and
``file-b3``. A pre-segmented corpus (one utterance per file, FLEURS) takes ``segmentation: file``: each file stays one
segment whose hash is the import's. Help: docs/help/steps/sdp-ingest.md.
"""

from __future__ import annotations

import fnmatch
import json
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Literal

import yaml
from pydantic import BaseModel

from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "sdp_ingest@2"
SIDECAR = ".cadence.json"
SCRIPT_ORIGIN = "model:tts-script"


class SdpIngestParams(BaseModel):
    source: str = cadence_field(
        "",
        description="Registered source (sources.new) the audio belongs to; its licence must allow ingest",
        source="docs/spec/04-blocks.md Block 1 (no licence, no ingest)",
        range={"minLength": 2, "maxLength": 100},
        registry="source",
    )
    path: str = cadence_field(
        "",
        description="Mount URI of the directory (or one file) to ingest, e.g. mount://corpora/fleurs-sr/70bb2e84b976",
        source="docs/review/2026-10-03-phase-4-plan.md (mount layout <source>/<revision>/)",
        range={"minLength": 9, "maxLength": 1000},
    )
    pattern: str = cadence_field(
        "**/*",
        description="Glob of the files read under path (audio only: wav, flac, mp3, ogg, opus, m4a, aac, webm)",
        source="Cadence recommendation",
        range={"minLength": 1, "maxLength": 200},
    )
    exclude: list[str] = cadence_field(
        ["test/*", "*/test/*"],
        description=(
            "Globs of files left out, matched against the path under path (fnmatch: * crosses /); the default leaves"
            " out a corpus's test split, where golden sets come from — [] reads everything"
        ),
        source="docs/spec/04-blocks.md Block 3 (golden-set audio never reaches training)",
        range={"maxLength": 50},
    )
    language: str = cadence_field(
        "",
        description="Language of every segment (BCP 47, e.g. sr-RS) unless a file's sidecar names one",
        source="Cadence recommendation",
        range={"maxLength": 35},
    )
    channels: Literal["auto", "mono", "split"] = cadence_field(
        "auto",
        description=(
            "auto splits a multi-channel file into one track per channel when its roles are known (sidecar or"
            " channel_roles) and mixes it down otherwise; mono always mixes down; split always splits"
        ),
        source="docs/spec/04-blocks.md Block 1 (one track per party)",
        range={"values": ["auto", "mono", "split"]},
    )
    channel_roles: list[str] = cadence_field(
        [],
        description="Role of each channel (caller, bot, mono) when no sidecar names them, e.g. [caller, bot]",
        source="Cadence recommendation",
        range={"maxLength": 8},
    )
    segmentation: Literal["vad", "file"] = cadence_field(
        "vad",
        description=(
            "vad cuts each track at its pauses; file keeps each track whole — for pre-segmented corpora (one utterance"
            " per file, e.g. FLEURS), whose segments then hash as an import of the same files"
        ),
        source="Cadence recommendation",
        range={"values": ["vad", "file"]},
    )
    vad_frame_ms: int = cadence_field(default_ref="data.ingest_vad_frame_ms")
    vad_margin_db: float = cadence_field(default_ref="data.ingest_vad_margin_db")
    vad_floor_db: float = cadence_field(default_ref="data.ingest_vad_floor_db")
    vad_min_speech_ms: int = cadence_field(default_ref="data.ingest_vad_min_speech_ms")
    vad_min_silence_ms: int = cadence_field(default_ref="data.ingest_vad_min_silence_ms")
    vad_pad_ms: int = cadence_field(default_ref="data.ingest_vad_pad_ms")
    max_segment_s: float = cadence_field(default_ref="data.ingest_max_segment_s")
    min_segment_s: float = cadence_field(default_ref="data.ingest_min_segment_s")
    max_files: int = cadence_field(
        0,
        description="Read at most this many files, in path order; 0 reads them all",
        source="Cadence recommendation",
        range={"min": 0, "max": 100000000},
    )
    max_hours: float = cadence_field(
        default_ref="data.max_hours", description="Stop after this many hours of segments; 0 takes everything"
    )


class SdpIngestStep:
    version: ClassVar[str] = "2"
    consumes: ClassVar[Mapping[str, str]] = {}
    produces: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = SdpIngestParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = SdpIngestParams.model_validate(params.model_dump())
        ingest(p, outputs["segments"], ctx)


def vad_params(p: SdpIngestParams) -> seg.VadParams:
    return seg.VadParams(
        frame_ms=p.vad_frame_ms,
        margin_db=p.vad_margin_db,
        floor_db=p.vad_floor_db,
        min_speech_ms=p.vad_min_speech_ms,
        min_silence_ms=p.vad_min_silence_ms,
        pad_ms=p.vad_pad_ms,
        max_segment_s=p.max_segment_s,
        min_segment_s=p.min_segment_s,
    )


def _report(ctx: Any, fraction: float, message: str) -> None:
    fn = getattr(ctx, "progress", None)
    if callable(fn):
        fn(fraction, message)


def _log(ctx: Any, message: str, level: str = "info") -> None:
    fn = getattr(ctx, "log", None)
    if callable(fn):
        fn(message, level)


def audio_files(root: Path, pattern: str, exclude: list[str] | None = None) -> list[Path]:
    """The audio files under root matching pattern and none of exclude (globs on the path relative to root)."""
    if root.is_file():
        return [root]
    if not root.is_dir():
        raise StepInputError(f"{root} is neither a file nor a directory on the mount")
    skip = exclude or []
    return sorted(
        f
        for f in root.glob(pattern)
        if f.is_file()
        and f.suffix.lower() in seg.AUDIO_SUFFIXES
        and not any(fnmatch.fnmatchcase(f.relative_to(root).as_posix(), g) for g in skip)
    )


def missing(path: str, root: Path, mount_root: Path) -> StepInputError:
    """The error for a path that is not on the mount: what the nearest existing directory above it holds (a template's
    placeholder revision, a revision fetched under another name)."""
    up = root  # inside mount_root (mounts.resolve checked it), so the walk ends at mount_root at the latest
    while up != mount_root and not up.is_dir():
        up = up.parent
    where = up.relative_to(mount_root).as_posix() if up != mount_root else ""
    try:
        names = sorted(e.name for e in up.iterdir() if not e.name.startswith("."))
    except OSError:
        names = []
    shown = ", ".join(names[:12]) + (f", … ({len(names)} entries)" if len(names) > 12 else "")
    return StepInputError(
        f"{path} is not on the mount; {where or 'its root'} holds: {shown or 'nothing'}. Set the index step's path"
        " to mount://<mount>/<source>/<revision> (the layout the corpus scripts write)"
    )


def source_info(root: Path) -> dict[str, str]:
    f = (root if root.is_dir() else root.parent) / "SOURCE.yaml"
    if not f.is_file():
        return {}
    try:
        doc = yaml.safe_load(f.read_text(encoding="utf-8"))
    except yaml.YAMLError:
        return {}
    if not isinstance(doc, dict):
        return {}
    return {k: str(doc[k]) for k in ("licence", "url", "revision") if doc.get(k) not in (None, "")}


def sidecar(f: Path) -> dict[str, Any]:
    side = f.with_name(f.stem + SIDECAR)
    if not side.is_file():
        return {}
    try:
        doc = json.loads(side.read_text(encoding="utf-8"))
    except ValueError as e:
        raise StepInputError(f"{side.name} is not JSON: {e}") from e
    if not isinstance(doc, dict):
        raise StepInputError(f"{side.name} must be an object")
    return doc


def transcript(f: Path) -> str | None:
    t = f.with_suffix(".txt")
    if not t.is_file():
        return None
    return " ".join(t.read_text(encoding="utf-8").split())


def tracks(p: SdpIngestParams, nchan: int, roles: list[str]) -> list[tuple[int, str]]:
    """(channel, role) of every track of a file with nchan channels."""
    for r in roles:
        if r not in seg.ROLES:
            raise StepInputError(f"role {r!r} is not one of {', '.join(seg.ROLES)}")
    if nchan == 1:
        return [(0, roles[0] if roles else "mono")]
    split = p.channels == "split" or (p.channels == "auto" and len(roles) >= nchan)
    if not split:
        return [(seg.MIXED, "mono")]
    return [(c, roles[c] if c < len(roles) else "mono") for c in range(nchan)]


def ingest(p: SdpIngestParams, out: Path, ctx: Any = None) -> dict[str, Any]:
    if not p.source:
        raise StepInputError("source is required: the registered source the audio belongs to (sources.new)")
    ms = mounts.mounts_of(ctx)
    ref = mounts.parse(p.path)
    root = mounts.resolve(p.path, ms)
    if not root.exists():
        raise missing(p.path, root, mounts.resolve(mounts.format_uri(ref.name, ""), ms))
    files = audio_files(root, p.pattern, p.exclude)
    if p.max_files:
        files = files[: p.max_files]
    if not files:
        raise StepInputError(f"no audio files under {p.path} matching {p.pattern!r} (excluding {p.exclude})")
    vp = vad_params(p)
    lines: list[dict[str, Any]] = []
    file_rows: list[dict[str, Any]] = []
    roles_seen: set[str] = set()
    hours = 0.0
    read = 0
    with tempfile.TemporaryDirectory(prefix="sdp-ingest-") as tmp:
        for n, f in enumerate(files):
            if p.max_hours and hours >= p.max_hours:
                _log(ctx, f"max_hours {p.max_hours} reached after {read} files")
                break
            rel = mounts.relative(f, ms, ref.name)
            file_uri = mounts.format_uri(ref.name, rel)
            side = sidecar(f)
            d = seg.decode(f, Path(tmp))
            if not d.channels or d.duration <= 0:
                _log(ctx, f"{rel}: no audio, skipped", "warn")
                continue
            read += 1
            roles = [str(r) for r in (side.get("roles") or p.channel_roles)]
            speakers = [str(s) for s in (side.get("speakers") or [])]
            language = str(side.get("language") or p.language)
            text = transcript(f)
            script = [t for t in side.get("script") or [] if isinstance(t, dict)]
            tr = tracks(p, len(d.channels), roles)
            signals = {c: seg.track(d, c) for c, _ in tr}
            runs = {c: seg.speech(x, vp) for c, x in signals.items()}
            for c, role in tr:
                x16 = signals[c]
                dur16 = x16.size / seg.RATE
                whole = seg.hash_of(seg.wav_of(x16, 0, x16.size))  # file-b3: the track as an import hashes it
                others = sorted(r for oc, rs in runs.items() if oc != c for r in rs)
                turns = [t for t in script if int(t.get("channel", -2)) == c]
                spans: list[tuple[float, float, str | None, str | None]] = []
                if turns:
                    for t in turns:
                        i0, i1 = seg.bounds(max(0.0, float(t["start"])), min(dur16, float(t["end"])))
                        if i1 > i0:
                            spans.append(
                                (i0 / seg.RATE, i1 / seg.RATE, " ".join(str(t.get("text", "")).split()), SCRIPT_ORIGIN)
                            )
                elif p.segmentation == "file" or (text is not None and len(tr) == 1):
                    i0, i1 = seg.bounds(0.0, dur16)
                    spans.append((i0 / seg.RATE, i1 / seg.RATE, text, "human" if text else None))
                else:
                    spans.extend((a, b, None, None) for a, b in seg.segments_of(x16, runs[c], vp))
                for a, b, txt, origin in spans:
                    i0, i1 = seg.bounds(a, b)
                    wav = seg.wav_of(x16, i0, i1)
                    line: dict[str, Any] = {
                        "uri": mounts.format_uri(ref.name, rel, a, b, None if c == seg.MIXED else c),
                        "file": file_uri,
                        seg.FILE_FINGERPRINT: whole,
                        "hash": seg.hash_of(wav),
                        "bytes": len(wav),
                        "start": a,
                        "end": b,
                        "duration": round(b - a, 6),
                        "channel": c,
                        "role": role,
                        "vad": seg.vad_of(runs[c], a, b),
                        "level": seg.level(x16[i0:i1]),
                        "sourceRate": d.rate,
                    }
                    if d.codec:
                        line["codec"] = d.codec
                    if len(tr) > 1:
                        line["crosstalk"] = round(seg.covered(_union(others), a, b), 4)
                        line["eou"] = seg.eou_of(runs[c], a, b, others)
                    if language:
                        line["language"] = language
                    if 0 <= c < len(speakers) and speakers[c]:
                        line["speaker"] = speakers[c]
                    elif c == seg.MIXED and len(speakers) == 1 and speakers[0]:
                        line["speaker"] = speakers[0]
                    if txt:
                        line["text"] = txt
                        line["origin"] = origin or "human"
                    lines.append(line)
                    hours += (b - a) / 3600
                roles_seen.add(role)
            row: dict[str, Any] = {
                "uri": file_uri,
                "duration": round(d.duration, 6),
                "sampleRate": d.rate,
                "channels": len(d.channels),
                "roles": [r for _, r in tr],
                "speech": [[[a, b] for a, b in runs[c]] for c, _ in tr],
            }
            if d.codec:
                row["codec"] = d.codec
            file_rows.append(row)
            _report(ctx, (n + 1) / len(files), f"{n + 1}/{len(files)} files, {len(lines)} segments")
    if not lines:
        raise StepInputError(f"no speech found in {read} files under {p.path}")
    header: dict[str, Any] = {
        "source": {"name": p.source},
        "root": p.path,
        "files": read,
        "roles": sorted(roles_seen),
        "steps": [KIND],
    }
    if p.language:
        header["language"] = p.language
    if info := source_info(root):
        header["sourceInfo"] = info
    return seg.write(out, header, lines, files=file_rows)


def _union(runs: list[tuple[float, float]]) -> list[tuple[float, float]]:
    out: list[list[float]] = []
    for a, b in sorted(runs):
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [(a, b) for a, b in out]
