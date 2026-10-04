"""``hf_push@1`` — a frozen dataset version pushed to the Hugging Face Hub (docs/spec/03-pipelines-defaults.md
"Interoperability"; phase 4 · stream I).

``datasets.export`` with format ``hf-hub`` runs it after the licence check and an approval the admin decides. It lays
the dataset out as an ``audiofolder`` dataset — ``data/<split>/<hash>.wav`` and ``data/<split>/metadata.jsonl``
(``file_name``, ``transcription``, ``language``, ``speaker_id``, ``duration``) — writes ``README.md`` (the Hub's YAML
header: licence, languages, task, one config whose splits point at ``data/<split>/``; then the dataset card), creates
the repository (private unless ``private`` is false) when it is missing and uploads everything in one commit.

The token comes from the secret ``hf-token`` (the lease's ``HF_TOKEN``); ``HF_ENDPOINT`` points at another Hub. The
output ``export`` artifact names the repository, the commit and every file pushed. ``huggingface_hub`` comes from the
runtime image (the NeMo Speech container has it). Help: docs/help/steps/hf-push.md.
"""

from __future__ import annotations

import importlib
import json
import os
import re
import shutil
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

import yaml
from pydantic import BaseModel

from cadence_worker import exports as ex
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "hf_push@1"
EXPORT_FORMAT = "hf-hub"
TOKEN_ENV = "HF_TOKEN"
REPO = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,95}/[A-Za-z0-9][A-Za-z0-9._-]{0,95}$")
HUB_LICENCE = re.compile(r"^[a-z0-9][a-z0-9.+-]{0,63}$")


class HfPushParams(BaseModel):
    repo: str = cadence_field(
        "",
        description="The Hub dataset repository <org>/<name>; created when missing",
        source="docs/spec/03-pipelines-defaults.md Interoperability",
        range={"pattern": REPO.pattern},
    )
    private: bool = cadence_field(default_ref="storage.export_hub_private")
    licence: str = cadence_field(
        "",
        description="The version's licence for the card's header (an SPDX id: CC-BY-4.0 becomes cc-by-4.0)",
        source="docs/spec/03-pipelines-defaults.md Interoperability (a licence check before the push)",
        range={"minLength": 1, "maxLength": 200},
    )
    version: str = cadence_field(
        "",
        description="The dataset version (ver_…) pushed; named in the commit message and export.json",
        source="Cadence recommendation",
        range={"maxLength": 64},
    )
    name: str = cadence_field(
        "",
        description="The version's collection without dataset/ (the card's title)",
        source="Cadence recommendation",
        range={"maxLength": 100},
    )


class HfPushStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"dataset": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"export": "export"}
    secrets: ClassVar[tuple[str, ...]] = ("hf-token",)
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "export"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = HfPushParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = HfPushParams.model_validate(params.model_dump())
        push(p, ex.read_dataset(inputs["dataset"]), outputs["export"], ctx)


def hub_licence(licence: str) -> str:
    """The Hub's licence id for an SPDX-like licence, else other."""
    v = licence.strip().lower()
    return v if HUB_LICENCE.match(v) else "other"


def readme(p: HfPushParams, ds: ex.Dataset, splits: list[str]) -> str:
    langs = sorted({str(x.get("language") or "").split("-")[0] for x in ds.lines} - {""})
    lic = hub_licence(p.licence)
    head: dict[str, Any] = {
        "license": lic,
        "language": langs,
        "task_categories": ["automatic-speech-recognition"],
        "pretty_name": p.name or str(ds.header.get("name") or ""),
        "configs": [{"config_name": "default", "data_files": [{"split": s, "path": f"data/{s}/*"} for s in splits]}],
    }
    if lic == "other":
        head["license_name"] = p.licence
    card = ""
    if isinstance(ds.header.get("card"), str) and (ds.root / ds.header["card"]).is_file():
        card = (ds.root / ds.header["card"]).read_text(encoding="utf-8")
    title = p.name or str(ds.header.get("name") or "dataset")
    body = card or f"# {title}\n\n{len(ds.lines)} utterances.\n"
    note = f"\nExported from Cadence (dataset version {p.version}).\n" if p.version else ""
    return "---\n" + yaml.safe_dump(head, sort_keys=True, allow_unicode=True) + "---\n\n" + body + note


def layout(p: HfPushParams, ds: ex.Dataset, root: Path) -> list[dict[str, Any]]:
    """Write the audiofolder into root and list its files."""
    splits: list[str] = []
    for split, part in ds.by_split():
        splits.append(split)
        d = root / "data" / split
        d.mkdir(parents=True, exist_ok=True)
        rows = []
        for x in part:
            src = ds.audio(x)
            name = hash_file(src).removeprefix("b3:") + ".wav"
            shutil.copyfile(src, d / name)
            row: dict[str, Any] = {
                "file_name": name,
                "transcription": str(x.get("text") or ""),
                "language": str(x.get("language") or ""),
                "duration": float(x["duration"]),
            }
            if x.get("speaker"):
                row["speaker_id"] = str(x["speaker"])
            rows.append(row)
        (d / "metadata.jsonl").write_text(
            "".join(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n" for r in rows), encoding="utf-8"
        )
    (root / "README.md").write_text(readme(p, ds, splits), encoding="utf-8")
    return [
        {"path": f.relative_to(root).as_posix(), "hash": hash_file(f), "bytes": f.stat().st_size}
        for f in sorted(root.rglob("*"))
        if f.is_file()
    ]


def hub_api(token: str) -> Any:
    """An authenticated HfApi; tests replace it."""
    try:
        hub: Any = importlib.import_module("huggingface_hub")
    except ImportError as e:
        raise StepInputError(
            "hf_push needs huggingface_hub (the NeMo Speech image has it; this runtime does not)"
        ) from e
    return hub.HfApi(endpoint=os.environ.get("HF_ENDPOINT") or None, token=token)


def push(p: HfPushParams, ds: ex.Dataset, out: Path, ctx: Any = None) -> dict[str, Any]:
    if not REPO.match(p.repo):
        raise StepInputError(f"repo {p.repo!r} is not <org>/<name>")
    if not p.licence.strip():
        raise StepInputError("licence is required: the card names the dataset's licence")
    token = os.environ.get(TOKEN_ENV, "")
    if not token:
        raise StepInputError("no Hub token: the admin stores it as the secret hf-token (Settings → Secrets)")
    with tempfile.TemporaryDirectory(prefix="hf-push-") as tmp:
        root = Path(tmp)
        files = layout(p, ds, root)
        ex.report(ctx, 0.5, f"{len(ds.lines)} utterances laid out; pushing to {p.repo}")
        api = hub_api(token)
        api.create_repo(repo_id=p.repo, repo_type="dataset", private=p.private, exist_ok=True)
        info = api.upload_folder(
            repo_id=p.repo,
            repo_type="dataset",
            folder_path=str(root),
            commit_message=f"Cadence export of {p.name or 'a dataset'} ({p.version or 'unversioned'})",
        )
    commit = str(getattr(info, "oid", "") or "")
    url = str(getattr(info, "commit_url", "") or f"https://huggingface.co/datasets/{p.repo}")
    hub = {"repo": p.repo, "commit": commit, "url": url, "private": p.private}
    ex.report(ctx, 1.0, f"pushed to {p.repo} ({commit[:12] or 'no commit id'})")
    return ex.finish(
        out,
        EXPORT_FORMAT,
        None,
        target=f"hf://datasets/{p.repo}",
        version=p.version,
        utterances=len(ds.lines),
        hub=hub,
        files=files,
    )
