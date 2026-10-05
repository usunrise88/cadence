"""toy_export — the export role of toy-ctc (phase 5): a checkpoint becomes a ``deployable`` (``cadence.deployable/1``)
that ``toy_serve`` loads in process. It exists to keep the export → serve → parity seam honest on a CPU (the
conformance suite's export and parity stages), never to serve anything:

    deployable.json   format toy-pt-dir, family, profile, weightsHash, serving {server: {kind: toy}, modelDir: model,
                      memoryMb, maxStreams, sampleRate, chunkMs, input: audio}, files, manifestSha256
    model/            the checkpoint's files (model.pt, config.json, tokenizer.json)

``files`` lists the model directory relative to ``modelDir`` with SHA-256 and size; ``manifestSha256`` is the SHA-256
of one ``"<sha256>  <path>\\n"`` line per file sorted by path (what a delivery script's ``find | sort | sha256sum``
prints over the installed directory). Help: docs/help/steps/toy-export.md.
"""

from __future__ import annotations

import hashlib
import json
import shutil
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_toy.family import NAME, RUNTIME, profile
from cadence_toy.model import SAMPLE_RATE, weights_hash
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

SCHEMA = "cadence.deployable/1"
FORMAT = "toy-pt-dir"
MODEL_DIR = "model"
CHECKPOINT_FILES = ("model.pt", "config.json", "tokenizer.json")


class ExportParams(BaseModel):
    profile: str = cadence_field(default_ref="packs.toy.profile")
    format: str = cadence_field(
        FORMAT,
        description="Deployable format to write (the family's exportFormats)",
        source="The toy-ctc family descriptor (exportFormats)",
        range={"values": [FORMAT]},
    )


def sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def manifest_sha256(files: list[dict[str, Any]]) -> str:
    """SHA-256 of the sorted ``"<sha256>  <path>\\n"`` lines of a model directory (sha256sum's format)."""
    text = "".join(f"{f['sha256']}  {f['path']}\n" for f in sorted(files, key=lambda f: str(f["path"]).encode()))
    return hashlib.sha256(text.encode()).hexdigest()


def model_files(model_dir: Path) -> list[dict[str, Any]]:
    out = []
    for f in sorted(p for p in model_dir.rglob("*") if p.is_file()):
        out.append({"path": f.relative_to(model_dir).as_posix(), "sha256": sha256_file(f), "bytes": f.stat().st_size})
    return out


class ExportStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint"}
    produces: ClassVar[Mapping[str, str]] = {"deployable": "deployable"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "export"}
    role: ClassVar[str] = "export"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = ExportParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ExportParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"toy-ctc has no latency profile {p.profile!r}") from e
        src = inputs["model"]
        out = outputs["deployable"]
        mdir = out / MODEL_DIR
        mdir.mkdir(parents=True, exist_ok=True)
        for name in CHECKPOINT_FILES:
            if not (src / name).is_file():
                raise StepInputError(f"the checkpoint lacks {name}")
            shutil.copyfile(src / name, mdir / name)
        files = model_files(mdir)
        whash = weights_hash(src / "model.pt")
        doc = {
            "schema": SCHEMA,
            "format": p.format,
            "family": NAME,
            "profile": prof["name"],
            "weightsHash": whash,
            "precision": "fp32",
            "serving": {
                "server": {"kind": "toy", "version": "1", "minVersion": "1"},
                "modelDir": MODEL_DIR,
                "memoryMb": 64,
                "maxStreams": 1024,
                "sampleRate": SAMPLE_RATE,
                "chunkMs": int(prof.get("chunkMs") or 0),
                "boost": {"static": False, "dynamic": False},
                "input": "audio",
            },
            "files": files,
            "manifestSha256": manifest_sha256(files),
        }
        (out / "deployable.json").write_text(json.dumps(doc, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        ctx.set_meta(
            "deployable",
            {"schema": SCHEMA, "format": p.format, "family": NAME, "profile": prof["name"], "weightsHash": whash},
        )
        ctx.progress(1.0, f"exported {prof['name']} as {p.format}")
