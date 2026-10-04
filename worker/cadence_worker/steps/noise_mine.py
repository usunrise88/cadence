"""``noise_mine@1`` — a noise bank from the silences of call recordings (docs/spec/03-pipelines-defaults.md
"Augmentation", Background noise: "a noise bank mined from the non-speech regions of the project's own call
recordings (per-channel VAD)"; phase 4 · stream I).

Reads the ``segments`` artifact an ingest wrote (``sdp_ingest``: ``files.jsonl`` has every file's tracks, their roles
and the voice activity of each track). For every track whose role is in ``roles``, it takes the stretches where **no**
track of the file has speech (so crosstalk and echo of the other party stay out), keeps ``edge_margin_ms`` away from
any speech, cuts long silences into clips of at most ``max_clip_s``, drops clips shorter than ``min_clip_s``, digital
silence (below ``min_rms_db``, e.g. a TTS bot channel) and clips loud enough to be missed speech (above
``max_rms_db``), and takes at most ``max_clips_per_file`` per recording. The clips are cut from the same 16 kHz track
the ingest hashed, so each clip's ``mount://`` URI points at its source.

It writes a ``dataset`` artifact of purpose ``noise`` that names only the registered source and says how it was mined
(``mined``); the control plane registers a frozen ``noise_bank`` version in ``noise-bank/<name>`` (tag ``mined``).
Runtime-neutral, CPU. Help: docs/help/steps/noise-mine.md.
"""

from __future__ import annotations

import json
import tempfile
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, artifact_digest, cadence_field

KIND = "noise_mine@1"
FORMAT = "cadence.dataset/1"

Interval = tuple[float, float]


class NoiseMineParams(BaseModel):
    name: str = cadence_field(
        "",
        description="Registry collection of the noise bank, without noise-bank/; empty = <source>-calls",
        source="Cadence recommendation",
        range={"pattern": "^([a-z0-9][a-z0-9._-]{0,98}[a-z0-9])?$"},
    )
    roles: list[str] = cadence_field(
        ["caller", "bot"],
        description="Track roles whose silences are mined (caller, bot, mono); a bot track of synthetic speech is "
        "usually digital silence and drops out at min_rms_db",
        source="docs/review/2026-10-03-phase-4-plan.md stream I (per-channel VAD silences of caller and bot tracks)",
        range={"maxLength": 3},
    )
    min_clip_s: float = cadence_field(default_ref="data.noise_min_clip_s")
    max_clip_s: float = cadence_field(default_ref="data.noise_max_clip_s")
    edge_margin_ms: int = cadence_field(default_ref="data.noise_edge_margin_ms")
    min_rms_db: float = cadence_field(default_ref="data.noise_min_rms_db")
    max_rms_db: float = cadence_field(default_ref="data.noise_max_rms_db")
    max_clips_per_file: int = cadence_field(default_ref="data.noise_max_clips_per_file")
    max_hours: float = cadence_field(
        default_ref="data.max_hours", description="Stop after this many hours of noise clips; 0 takes everything"
    )
    tags: list[str] = cadence_field(
        [],
        description="Collection tags (domain:telephony, synthetic, …); noise-bank and mined are added",
        source="Cadence recommendation",
        range={"maxLength": 20},
    )


class NoiseMineStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"noise": "dataset"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = NoiseMineParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = NoiseMineParams.model_validate(params.model_dump())
        header, _ = seg.read(inputs["segments"])
        files = read_files(inputs["segments"])
        mine(p, header, files, outputs["noise"], ctx, segments_hash=artifact_digest(inputs["segments"]))


def read_files(root: Path) -> list[dict[str, Any]]:
    f = root / seg.FILES
    if not f.is_file():
        raise StepInputError(f"the segments have no {seg.FILES}: run sdp_ingest@1 (it lists every file's tracks)")
    out = []
    for n, raw in enumerate(f.read_text(encoding="utf-8").splitlines(), 1):
        if raw.strip():
            x = json.loads(raw)
            if not isinstance(x, dict) or not x.get("uri"):
                raise StepInputError(f"{seg.FILES} line {n} has no uri")
            out.append(x)
    return out


def track_channels(row: Mapping[str, Any]) -> list[int]:
    """The channel of each of a file's tracks, in files.jsonl order: one per channel when the file was split, else
    channel 0 of a mono file or every channel mixed down."""
    roles = list(row.get("roles") or [])
    nchan = int(row.get("channels") or 1)
    if len(roles) == nchan:
        return list(range(nchan))
    return [0 if nchan == 1 else seg.MIXED]


def union(runs: Sequence[Interval]) -> list[Interval]:
    out: list[list[float]] = []
    for a, b in sorted(runs):
        if out and a <= out[-1][1]:
            out[-1][1] = max(out[-1][1], b)
        else:
            out.append([a, b])
    return [(a, b) for a, b in out]


def silences(speech: Sequence[Interval], duration: float, margin: float) -> list[Interval]:
    """The stretches of [0, duration] at least margin away from every speech run."""
    busy = union([(max(0.0, a - margin), min(duration, b + margin)) for a, b in speech])
    out: list[Interval] = []
    t = 0.0
    for a, b in busy:
        if a > t:
            out.append((t, a))
        t = max(t, b)
    if t < duration:
        out.append((t, duration))
    return out


def clips(gaps: Sequence[Interval], min_s: float, max_s: float) -> list[Interval]:
    """Gaps cut into clips of at most max_s, dropping pieces shorter than min_s (on the 16 kHz grid)."""
    out: list[Interval] = []
    for a, b in gaps:
        t = a
        while b - t >= min_s:
            e = min(b, t + max_s)
            i0, i1 = seg.bounds(t, e)
            out.append((i0 / seg.RATE, i1 / seg.RATE))
            t = e
    return [(a, b) for a, b in out if b - a >= min_s - 1e-9]


def mine(
    p: NoiseMineParams,
    header: Mapping[str, Any],
    files: Sequence[Mapping[str, Any]],
    out: Path,
    ctx: Any = None,
    segments_hash: str = "",
) -> dict[str, Any]:
    source = str((header.get("source") or {}).get("name") or "")
    if not source:
        raise StepInputError("the segments name no source (segments.json source.name)")
    bad = sorted(set(p.roles) - set(seg.ROLES))
    if bad or not p.roles:
        raise StepInputError(f"roles must be some of {', '.join(seg.ROLES)} (got {p.roles})")
    if p.min_clip_s > p.max_clip_s:
        raise StepInputError("min_clip_s must not exceed max_clip_s")
    ms = mounts.mounts_of(ctx)
    out.mkdir(parents=True, exist_ok=True)
    lines: list[dict[str, Any]] = []
    seen: set[str] = set()
    dropped = {"quiet": 0, "loud": 0, "cap": 0}
    hours = 0.0
    used_files = 0
    with tempfile.TemporaryDirectory(prefix="noise-mine-") as tmp:
        for n, row in enumerate(files):
            if p.max_hours and hours >= p.max_hours:
                break
            roles = [str(r) for r in row.get("roles") or []]
            chans = track_channels(row)
            speech = [(float(a), float(b)) for track in row.get("speech") or [] for a, b in track]
            wanted = [(i, c) for i, c in enumerate(chans) if i < len(roles) and roles[i] in p.roles]
            if not wanted:
                continue
            ref = mounts.parse(str(row["uri"]))
            d = seg.decode(mounts.resolve(ref.file_uri, ms), Path(tmp))
            duration = d.duration
            gaps = silences(speech, duration, p.edge_margin_ms / 1000)
            taken = 0
            for i, c in wanted:
                x16 = seg.track(d, c)
                for a, b in clips(gaps, p.min_clip_s, p.max_clip_s):
                    if taken >= p.max_clips_per_file:
                        dropped["cap"] += 1
                        continue
                    i0, i1 = seg.bounds(a, b)
                    lv = seg.level(x16[i0:i1])
                    if lv["rmsDb"] < p.min_rms_db:
                        dropped["quiet"] += 1
                        continue
                    if lv["rmsDb"] > p.max_rms_db:
                        dropped["loud"] += 1
                        continue
                    wav = seg.wav_of(x16, i0, i1)
                    h = seg.hash_of(wav)
                    if h in seen:
                        continue
                    seen.add(h)
                    hexd = h.removeprefix("b3:")
                    rel = f"audio/{hexd[:2]}/{hexd}.wav"
                    (out / rel).parent.mkdir(parents=True, exist_ok=True)
                    (out / rel).write_bytes(wav)
                    lines.append(
                        {
                            "audio": rel,
                            "duration": round((i1 - i0) / seg.RATE, 6),
                            "sampleRate": seg.RATE,
                            "channels": 1,
                            "language": "und",
                            "text": "",
                            "origin": "human",
                            "split": "train",
                            "uri": mounts.format_uri(ref.name, ref.path, a, b, None if c == seg.MIXED else c),
                            "role": roles[i],
                        }
                    )
                    taken += 1
                    hours += (i1 - i0) / seg.RATE / 3600
            used_files += 1 if taken else 0
            report = getattr(ctx, "progress", None)
            if callable(report):
                report((n + 1) / len(files), f"{n + 1}/{len(files)} recordings, {len(lines)} noise clips")
    if not lines:
        raise StepInputError(
            f"no noise clips in {len(files)} recordings (dropped: {dropped}); lower min_clip_s or edge_margin_ms, "
            "or check the roles"
        )
    tags = list(dict.fromkeys([*p.tags, "noise-bank", "mined"]))
    head: dict[str, Any] = {
        "format": FORMAT,
        "name": p.name or f"{source}-calls",
        "description": f"Background noise mined from the silences of {used_files} recordings of source {source}",
        "source": {"name": source},
        "splitRule": "all-train",
        "counts": {"train": len(lines), "validation": 0, "test": 0},
        "hours": sum(x["duration"] for x in lines) / 3600,
        "purpose": "noise",
        "tags": tags,
        "mined": {
            "stepKind": KIND,
            "segments": segments_hash,
            "root": str(header.get("root") or ""),
            "roles": list(p.roles),
            "files": used_files,
            "clips": len(lines),
            "dropped": dropped,
            "minClipS": p.min_clip_s,
            "maxClipS": p.max_clip_s,
            "edgeMarginMs": p.edge_margin_ms,
            "rmsDb": [p.min_rms_db, p.max_rms_db],
        },
    }
    with (out / "manifest.jsonl").open("w", encoding="utf-8") as f:
        for x in lines:
            f.write(json.dumps(x, ensure_ascii=False, sort_keys=True) + "\n")
    (out / "dataset.json").write_text(json.dumps(head, ensure_ascii=False, indent=2, sort_keys=True) + "\n", "utf-8")
    return head
