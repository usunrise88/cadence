"""``dataset_export@1`` — a frozen dataset version as a NeMo manifest or a Cadence bundle
(docs/spec/03-pipelines-defaults.md "Interoperability"; phase 4 · stream I).

``format: nemo-manifest`` writes what NeMo's ASR data loaders read: ``manifest.<split>.jsonl`` per split, one line per
utterance — ``audio_filepath`` (relative to the manifest), ``duration``, ``text``, ``lang`` and ``speaker`` when known
— and the WAV files beside them at their paths in the dataset (``audio/<ab>/<hash>.wav``), copied byte for byte.

``format: cadence-bundle`` writes what another Cadence instance imports (``dataset_import`` format
``cadence-bundle``): ``bundle.json`` — the version's registry record and sources (the ``record`` input the control
plane renders), the dataset artifact's hash and its files — and every blob of the artifact laid out as a content
store, ``cas/b3/<ab>/<hex>``. Mount scans recognise that layout, and the control plane records each blob as a copy on
the mount, so the cache may evict the version and ``datasets.materialize`` can bring it back.

The target is the content store (``cas``) or a directory on a writable mount. Help: docs/help/steps/dataset-export.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_worker import exports as ex
from cadence_worker.cas import ManifestFile, encode_manifest, hash_bytes, hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "dataset_export@1"
BUNDLE_FORMAT = "cadence.bundle/1"
RECORD_FORMAT = "cadence.registry-record/1"


class DatasetExportParams(BaseModel):
    format: Literal["nemo-manifest", "cadence-bundle"] = cadence_field(
        "nemo-manifest",
        description="nemo-manifest: manifest.<split>.jsonl with the WAV files; cadence-bundle: the registry record and "
        "the blobs as a content store, for another Cadence instance",
        source="docs/spec/03-pipelines-defaults.md Interoperability",
        range={"values": ["nemo-manifest", "cadence-bundle"]},
    )
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


class DatasetExportStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"dataset": "dataset", "record": "registry_record"}
    produces: ClassVar[Mapping[str, str]] = {"export": "export"}
    optional_inputs: ClassVar[tuple[str, ...]] = ("record",)
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "export"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = DatasetExportParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = DatasetExportParams.model_validate(params.model_dump())
        ds = ex.read_dataset(inputs["dataset"])
        if p.format == "cadence-bundle":
            if "record" not in inputs:
                raise StepInputError("cadence-bundle needs the record input (datasets.export renders it)")
            bundle(p, ds, read_record(inputs["record"]), outputs["export"], ctx)
        else:
            manifest(p, ds, outputs["export"], ctx)


def manifest(p: DatasetExportParams, ds: ex.Dataset, out: Path, ctx: Any = None) -> dict[str, Any]:
    w = ex.writer(p.target, out, ctx)
    done = 0
    for split, part in ds.by_split():
        rows = []
        for x in part:
            rel = str(x["audio"])
            w.copy(rel, ds.audio(x))
            row: dict[str, Any] = {
                "audio_filepath": rel,
                "duration": float(x["duration"]),
                "text": str(x.get("text") or ""),
                "lang": str(x.get("language") or ""),
            }
            if x.get("speaker"):
                row["speaker"] = str(x["speaker"])
            rows.append(row)
            done += 1
            if done % 200 == 0:
                ex.report(ctx, done / len(ds.lines), f"{done}/{len(ds.lines)} utterances")
        body = "".join(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n" for r in rows).encode("utf-8")
        w.write_bytes(f"manifest.{split}.jsonl", body)
    return ex.finish(out, p.format, w, version=p.version, utterances=len(ds.lines))


def read_record(path: Path) -> dict[str, Any]:
    f = path if path.is_file() else next(iter(sorted(path.rglob("*.json"))), path)
    try:
        doc = json.loads(f.read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the record input is not JSON: {e}") from e
    if not isinstance(doc, dict) or doc.get("format") != RECORD_FORMAT:
        raise StepInputError(f"the record input is not {RECORD_FORMAT}")
    return doc


def bundle(
    p: DatasetExportParams, ds: ex.Dataset, record: Mapping[str, Any], out: Path, ctx: Any = None
) -> dict[str, Any]:
    w = ex.writer(p.target, out, ctx)
    files = sorted((f for f in ds.root.rglob("*") if f.is_file()), key=lambda f: f.relative_to(ds.root).as_posix())
    listed: list[ManifestFile] = []
    for n, f in enumerate(files, 1):
        h = hash_file(f)
        listed.append(ManifestFile(f.relative_to(ds.root).as_posix(), h, f.stat().st_size))
        w.copy(blob_path(h), f)
        if n % 200 == 0:
            ex.report(ctx, n / len(files), f"{n}/{len(files)} blobs")
    man = encode_manifest(listed)
    artifact = hash_bytes(man)
    w.write_bytes(blob_path(artifact), man)
    payload = record.get("payload") if isinstance(record.get("payload"), Mapping) else {}
    recorded = str(((payload or {}).get("artifact") or {}).get("hash") or "")
    if recorded and recorded != artifact:
        raise StepInputError(f"the record names artifact {recorded}, the dataset input is {artifact}")
    doc = {
        "format": BUNDLE_FORMAT,
        "record": dict(record),
        "artifact": artifact,
        "files": [{"path": m.path, "hash": m.hash, "bytes": m.size} for m in listed],
    }
    w.write_bytes("bundle.json", (json.dumps(doc, ensure_ascii=False, indent=1, sort_keys=True) + "\n").encode("utf-8"))
    return ex.finish(out, p.format, w, version=p.version, utterances=len(ds.lines))


def blob_path(h: str) -> str:
    """Where a blob lives in a content-store layout: cas/b3/<ab>/<hex>."""
    hexd = h.removeprefix("b3:")
    return f"cas/b3/{hexd[:2]}/{hexd}"
