"""``segments_cut@1`` — cut a ``segments`` artifact's audio from its mount into a ``dataset`` the pseudo-label members
read (phase 4 · stream B; the D → X interface of docs/review/2026-10-03-phase-4-plan.md).

The members (``nemotron_transcribe``, ``whisper_transcribe``, ``oasis_transcribe``) and ``lid_classify`` read a
``dataset``: this step writes one of format ``cadence.dataset/1`` with ``purpose: pseudo-label`` — every segment that
needs a label (no text of its own, or a pseudo-label from an earlier pass; the bot's TTS script and human transcripts
are left out), or every segment with ``which: all`` — as the canonical 16 kHz mono WAV ``dataset_freeze`` would cut,
checked against the segment's hash. The members key their hypotheses by that hash, so ``pseudolabel_ensemble`` joins
them back to the segments.

The control plane never registers it (the dataset hook skips purpose ``pseudo-label``) and never lets a training step
read it: it is scratch input, evicted with the run's other intermediate artifacts. Rows: ``{audio, hash, uri,
duration, language, text: "", role, speaker?}``. Help: docs/help/steps/segments-cut.md.
"""

from __future__ import annotations

import json
import tempfile
from collections.abc import Iterable, Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.protocol_gen import StepResources
from cadence_worker.segments import needs_label
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "segments_cut@1"
DATASET_FORMAT = "cadence.dataset/1"
PURPOSE = "pseudo-label"


class SegmentsCutParams(BaseModel):
    which: Literal["unlabelled", "all"] = cadence_field(
        "unlabelled",
        description="unlabelled cuts the segments a pseudo-label member labels (no text of their own, or an earlier "
        "pseudo-label); all cuts every segment (language identification over a whole corpus)",
        source="docs/review/2026-10-03-phase-4-plan.md decision 6 (members label segments without text)",
        range={"values": ["unlabelled", "all"]},
    )


class SegmentsCutStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "memoryGb": 2, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = SegmentsCutParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = SegmentsCutParams.model_validate(params.model_dump())
        header, rows = seg.read_segments(inputs["segments"])
        xs = unique(r for r in rows if p.which == "all" or needs_label(r))
        if not xs:
            raise StepInputError(
                "no segment needs a label: every segment has its own text (run data-ingest instead of pseudo-label)"
            )
        out = outputs["data"]
        lines = cut(xs, out, str(header.get("language") or ""), ctx)
        seconds = sum(float(x["duration"]) for x in lines)
        source = header.get("source")
        doc = {
            "format": DATASET_FORMAT,
            "purpose": PURPOSE,
            "name": str(source.get("name") or "") if isinstance(source, dict) else "",
            "language": str(header.get("language") or ""),
            "hours": round(seconds / 3600, 6),
            "counts": {"utterances": len(lines)},
            "segments": {"which": p.which, "of": len(rows)},
        }
        (out / "dataset.json").write_text(json.dumps(doc, ensure_ascii=False, sort_keys=True) + "\n", encoding="utf-8")
        with (out / "manifest.jsonl").open("w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line, ensure_ascii=False, sort_keys=True) + "\n")
        set_meta = getattr(ctx, "set_meta", None)
        if callable(set_meta):
            set_meta("data", {"purpose": PURPOSE, "utterances": len(lines), "hours": doc["hours"]})
        report = getattr(ctx, "progress", None)
        if callable(report):
            report(1.0, f"{len(lines)} of {len(rows)} segments cut ({doc['hours']:.3f} h)")


def unique(rows: Iterable[Mapping[str, Any]]) -> list[dict[str, Any]]:
    """The rows with a hash not seen before, in order: segments with the same audio are cut (and labelled) once; the
    ensemble gives every one of them the verdict on that audio."""
    seen: set[str] = set()
    out: list[dict[str, Any]] = []
    for r in rows:
        if r["hash"] not in seen:
            seen.add(r["hash"])
            out.append(dict(r))
    return out


def cut(xs: Sequence[dict[str, Any]], out: Path, language: str, ctx: Any = None) -> list[dict[str, Any]]:
    """Cut every segment from its file (decoded once per file), check it against its hash and write
    audio/<hex[:2]>/<hex>.wav; returns the manifest rows in segment order."""
    ms = mounts.mounts_of(ctx)
    by_file: dict[str, list[int]] = {}
    for i, x in enumerate(xs):
        by_file.setdefault(mounts.parse(str(x["uri"])).file_uri, []).append(i)
    lines: list[dict[str, Any] | None] = [None] * len(xs)
    done = 0
    report = getattr(ctx, "progress", None)
    with tempfile.TemporaryDirectory(prefix="segments-cut-") as tmp:
        for file_uri, idx in by_file.items():
            d = seg.decode(mounts.resolve(file_uri, ms), Path(tmp))
            tracks: dict[int, seg.Signal] = {}
            for i in idx:
                x = xs[i]
                ref = mounts.parse(str(x["uri"]))
                c = int(x.get("channel", ref.channel if ref.channel is not None else seg.MIXED))
                if c not in tracks:
                    tracks[c] = seg.track(d, c)
                i0, i1 = seg.bounds(float(x["start"]), float(x["end"]))
                wav = seg.wav_of(tracks[c], i0, i1)
                h = seg.hash_of(wav)
                if h != x["hash"]:
                    raise StepInputError(
                        f"{x['uri']}: the audio on the mount no longer matches the indexed segment ({h} ≠ {x['hash']});"
                        " the file changed since ingest — ingest it again"
                    )
                hexd = h.removeprefix("b3:")
                rel = f"audio/{hexd[:2]}/{hexd}.wav"
                dst = out / rel
                dst.parent.mkdir(parents=True, exist_ok=True)
                dst.write_bytes(wav)
                line: dict[str, Any] = {
                    "audio": rel,
                    "hash": h,
                    "uri": str(x["uri"]),
                    "duration": (i1 - i0) / seg.RATE,
                    "language": str(x.get("language") or language),
                    "text": "",
                    "role": str(x.get("role") or "mono"),
                }
                if x.get("speaker"):
                    line["speaker"] = str(x["speaker"])
                lines[i] = line
                done += 1
            if callable(report):
                report(done / len(xs) * 0.95, f"{done}/{len(xs)} segments cut")
    return [x for x in lines if x is not None]
