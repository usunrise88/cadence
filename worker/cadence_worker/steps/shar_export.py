"""``shar_export@1`` — a frozen dataset version as Lhotse Shar (docs/spec/03-pipelines-defaults.md "Interoperability";
phase 4 · stream I).

Reads a ``dataset`` artifact and writes one Shar directory per split (``train/``, ``validation/``, ``test/``), each with
shards of ``shard_utterances`` cuts:

    <split>/cuts.NNNNNN.jsonl.gz      one MonoCut per utterance (id = the audio's b3 hex, one supervision with text,
                                      language, speaker; custom: origin, split, uri?), its recording a Shar placeholder
    <split>/recording.NNNNNN.tar      per cut, in cut order: <id>.wav (the dataset's WAV, byte for byte) then <id>.json
                                      (the placeholder recording) — what Lhotse's SharWriter writes and its
                                      LazySharIterator reads

Archives and gzip streams are deterministic (no timestamps, owners or modes from the host), so the same version
exports to the same bytes everywhere. The target is the content store (``cas``) or a directory on a writable mount
(``mount://exports/…``); the output is an ``export`` artifact (:mod:`cadence_worker.exports`). Help:
docs/help/steps/shar-export.md.
"""

from __future__ import annotations

import gzip
import io
import json
import math
import tarfile
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import exports as ex
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "shar_export@1"
EXPORT_FORMAT = "lhotse-shar"


class SharExportParams(BaseModel):
    target: str = cadence_field(
        "cas",
        description="cas (the export stays in the content store) or a directory on a writable mount, mount://exports/…",
        source="docs/review/2026-10-03-phase-4-plan.md decision 1 (the exports mount)",
        range={"minLength": 3, "maxLength": 1000},
    )
    version: str = cadence_field(
        "",
        description="The dataset version (ver_…) the export reads; recorded in export.json (datasets.export sets it)",
        source="Cadence recommendation",
        range={"maxLength": 64},
    )
    name: str = cadence_field(
        "",
        description="The version's collection without dataset/ (recorded for people reading the export)",
        source="Cadence recommendation",
        range={"maxLength": 100},
    )
    shard_utterances: int = cadence_field(default_ref="data.shar_shard_utterances")


class SharExportStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"dataset": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"export": "export"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "export"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = SharExportParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = SharExportParams.model_validate(params.model_dump())
        export(p, ex.read_dataset(inputs["dataset"]), outputs["export"], ctx)


def _wav_info(path: Path) -> tuple[int, int]:
    """Sample rate and frames of a canonical (mono PCM16) WAV."""
    head = path.open("rb").read(44)
    if head[:4] != b"RIFF" or head[8:12] != b"WAVE" or head[36:40] != b"data":
        raise StepInputError(f"{path.name} is not a canonical WAV (the dataset's audio is mono 16-bit PCM)")
    rate = int.from_bytes(head[24:28], "little")
    frames = int.from_bytes(head[40:44], "little") // 2
    return rate, frames


def placeholder(cid: str, rate: int, frames: int) -> dict[str, Any]:
    """A Shar placeholder recording: the audio comes from the tar beside the cuts."""
    return {
        "id": cid,
        "sources": [{"type": "shar", "channels": [0], "source": ""}],
        "sampling_rate": rate,
        "num_samples": frames,
        "duration": frames / rate,
        "channel_ids": [0],
    }


def cut(x: Mapping[str, Any], cid: str, rate: int, frames: int) -> dict[str, Any]:
    dur = frames / rate
    sup: dict[str, Any] = {
        "id": cid,
        "recording_id": cid,
        "start": 0.0,
        "duration": dur,
        "channel": 0,
        "text": str(x.get("text") or ""),
        "language": str(x.get("language") or ""),
    }
    if x.get("speaker"):
        sup["speaker"] = str(x["speaker"])
    custom: dict[str, Any] = {"origin": str(x.get("origin") or "human"), "split": str(x.get("split") or "")}
    if x.get("uri"):
        custom["uri"] = str(x["uri"])
    return {
        "id": cid,
        "start": 0.0,
        "duration": dur,
        "channel": 0,
        "supervisions": [sup],
        "recording": placeholder(cid, rate, frames),
        "custom": custom,
        "type": "MonoCut",
    }


def _tar_add(tar: tarfile.TarFile, name: str, body: bytes) -> None:
    info = tarfile.TarInfo(name)
    info.size, info.mtime, info.mode, info.uid, info.gid, info.uname, info.gname = len(body), 0, 0o644, 0, 0, "", ""
    tar.addfile(info, io.BytesIO(body))


def shard(ds: ex.Dataset, part: Sequence[Mapping[str, Any]]) -> tuple[bytes, bytes]:
    """The cuts (gzip JSON lines) and recording tar of one shard."""
    cuts = io.BytesIO()
    tar_buf = io.BytesIO()
    with (
        gzip.GzipFile(filename="", mode="wb", fileobj=cuts, mtime=0) as gz,
        tarfile.open(fileobj=tar_buf, mode="w", format=tarfile.USTAR_FORMAT) as tar,
    ):
        for x in part:
            src = ds.audio(x)
            cid = hash_file(src).removeprefix("b3:")
            rate, frames = _wav_info(src)
            c = cut(x, cid, rate, frames)
            gz.write((json.dumps(c, ensure_ascii=False, sort_keys=True) + "\n").encode("utf-8"))
            _tar_add(tar, f"{cid}.wav", src.read_bytes())
            _tar_add(tar, f"{cid}.json", json.dumps(c["recording"], sort_keys=True).encode("utf-8"))
    return cuts.getvalue(), tar_buf.getvalue()


def export(p: SharExportParams, ds: ex.Dataset, out: Path, ctx: Any = None) -> dict[str, Any]:
    if p.shard_utterances < 1:
        raise StepInputError("shard_utterances must be at least 1")
    w = ex.writer(p.target, out, ctx)
    done = 0
    for split, part in ds.by_split():
        for k in range(math.ceil(len(part) / p.shard_utterances)):
            cuts, tar = shard(ds, part[k * p.shard_utterances : (k + 1) * p.shard_utterances])
            w.write_bytes(f"{split}/cuts.{k:06d}.jsonl.gz", cuts)
            w.write_bytes(f"{split}/recording.{k:06d}.tar", tar)
            done += min(p.shard_utterances, len(part) - k * p.shard_utterances)
            ex.report(ctx, done / len(ds.lines), f"{done}/{len(ds.lines)} utterances in Shar shards")
    return ex.finish(out, EXPORT_FORMAT, w, version=p.version, utterances=len(ds.lines))
