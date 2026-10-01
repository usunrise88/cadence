"""Checkpoints, training states and base models of the Nemotron family as artifacts.

``checkpoint`` (a directory artifact): ``model.nemo`` (NeMo's tar: ``model_config.yaml``, ``model_weights.ckpt``, the
sentencepiece tokenizer files) and ``checkpoint.json`` — the neutral meta (R42: family, step, valWer, weightsHash) plus
what loading needs: the base model it descends from and the tokenizer reference (the base model's, R44).

``training-state`` (a directory artifact, only for resuming): ``last.ckpt`` (Lightning: weights, optimiser, scheduler
and loop state) and ``state.json`` (family, step, seed, best validation so far: ``bestValWer``, ``bestStep`` and
``bestFiles``, the content hashes of that published checkpoint's files, so a resumed lease keeps it as the best).

``base`` input: a ``base_model`` artifact (``cadence.base_model/1`` JSON: Hugging Face repository, revision, checkpoint
file, family) or a ``checkpoint`` directory (a run that starts from a checkpoint, R44).

Averaging works on the ``.nemo`` tars directly (no NeMo needed): the element-wise mean of every floating-point tensor
of ``model_weights.ckpt``; integer buffers and every other member come from the first checkpoint.
"""

from __future__ import annotations

import io
import json
import os
import shutil
import tarfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from cadence_nemo.family import NAME
from cadence_worker.cas import CasError, hash_file
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

NEMO_FILE = "model.nemo"
CHECKPOINT_JSON = "checkpoint.json"
STATE_CKPT = "last.ckpt"
STATE_JSON = "state.json"
BASE_MODEL_FORMAT = "cadence.base_model/1"
CHECKPOINT_FORMAT = "cadence.nemo-checkpoint/1"
WEIGHTS_MEMBER = "model_weights.ckpt"
CONFIG_MEMBER = "model_config.yaml"


@dataclass
class Base:
    """Where a step's weights start: a ``.nemo`` file and where it came from."""

    nemo: Path
    kind: str  # base_model | checkpoint
    reference: dict[str, Any] = field(default_factory=dict)  # base model (hfRepo, revision, versionId) of the lineage
    step: int | None = None
    tokenizer: dict[str, Any] = field(default_factory=dict)


def weights_hash(nemo: Path) -> str:
    return hash_file(nemo)


def read_checkpoint(d: Path) -> dict[str, Any]:
    if not d.is_dir() or not (d / NEMO_FILE).is_file() or not (d / CHECKPOINT_JSON).is_file():
        raise StepInputError(f"{d.name} is not a {NAME} checkpoint (model.nemo and checkpoint.json)")
    doc = json.loads((d / CHECKPOINT_JSON).read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("family") != NAME:
        raise StepInputError(
            f"{d.name} is a {doc.get('family') if isinstance(doc, dict) else '?'!r} checkpoint, not {NAME}"
        )
    return doc


def write_checkpoint(d: Path, meta: Mapping[str, Any]) -> dict[str, Any]:
    """Complete ``checkpoint.json`` beside an already written ``model.nemo``; returns the neutral meta (R42)."""
    doc = {"format": CHECKPOINT_FORMAT, "family": NAME, **meta, "weightsHash": weights_hash(d / NEMO_FILE)}
    (d / CHECKPOINT_JSON).write_text(json.dumps(doc, indent=2, sort_keys=True), encoding="utf-8")
    return doc


def link_checkpoint(src: Path, dst: Path) -> None:
    """The same checkpoint under a second output name (hard links: same content, same artifact hash)."""
    dst.mkdir(parents=True, exist_ok=True)
    for f in src.iterdir():
        if f.is_file():
            target = dst / f.name
            if target.exists():
                target.unlink()
            try:
                os.link(f, target)
            except OSError:
                target.write_bytes(f.read_bytes())


def file_hashes(d: Path) -> dict[str, str]:
    """The content hashes of a (flat) checkpoint directory's files by name: what a training state records of the best
    published checkpoint, so a resumed lease can rebuild it from the content store (:func:`restore_files`)."""
    return {f.name: hash_file(f) for f in sorted(d.iterdir()) if f.is_file()}


def restore_files(files: Any, blob: Callable[[str], Path], dst: Path) -> bool:
    """Hard-link the files a training state recorded (``{name: hash}``) from the content store into ``dst`` — the
    best checkpoint published before a pause, the same artifact again. False (``dst`` removed) when the record is
    malformed or a blob is not in the store."""
    shutil.rmtree(dst, ignore_errors=True)
    if not isinstance(files, Mapping) or not files:
        return False
    try:
        srcs: dict[str, Path] = {}
        for name, h in files.items():
            if not isinstance(name, str) or not isinstance(h, str) or name != Path(name).name or name.startswith("."):
                return False
            srcs[name] = blob(h)
            if not srcs[name].is_file():
                return False
        dst.mkdir(parents=True, exist_ok=True)
        for name, src in srcs.items():
            try:
                os.link(src, dst / name)
            except OSError:
                shutil.copyfile(src, dst / name)
    except (OSError, RuntimeError, CasError):
        shutil.rmtree(dst, ignore_errors=True)
        return False
    return True


def publish_validation(
    ctx: StepContext, d: Path, meta: Mapping[str, Any], wer: float, best_link: Path | None = None
) -> dict[str, Any]:
    """Register a validation's checkpoint while training runs: complete ``checkpoint.json`` beside the ``model.nemo``
    already in d, hard-link it to ``best_link`` when it is the best so far (the release's ``checkpoint_best`` is then
    the same artifact), and publish d as an instance of the ``checkpoint`` output (the harness stores and removes d).
    Returns the checkpoint document."""
    doc = write_checkpoint(d, {**meta, "valWer": wer})
    if best_link is not None:
        shutil.rmtree(best_link, ignore_errors=True)
        link_checkpoint(d, best_link)
    ctx.publish("checkpoint", d, neutral_meta(doc), {"val_wer": wer})
    return doc


def neutral_meta(doc: Mapping[str, Any]) -> dict[str, Any]:
    keys = ("family", "step", "valWer", "weightsHash", "averagedFrom", "base", "tokenizer")
    return {k: doc[k] for k in keys if k in doc}


def read_state(d: Path) -> dict[str, Any]:
    if not (d / STATE_JSON).is_file() or not (d / STATE_CKPT).is_file():
        raise StepInputError("the training state to resume from lacks last.ckpt or state.json")
    doc = json.loads((d / STATE_JSON).read_text(encoding="utf-8"))
    if not isinstance(doc, dict) or doc.get("family") != NAME:
        raise StepInputError(
            f"the training state belongs to {doc.get('family') if isinstance(doc, dict) else '?'!r}, not {NAME}"
        )
    return doc


def write_state_json(d: Path, doc: Mapping[str, Any]) -> None:
    d.mkdir(parents=True, exist_ok=True)
    tmp = d / (STATE_JSON + ".tmp")
    tmp.write_text(json.dumps({"family": NAME, **doc}, indent=2, sort_keys=True), encoding="utf-8")
    os.replace(tmp, d / STATE_JSON)


def read_base(path: Path, download: Callable[[Mapping[str, Any]], Path]) -> Base:
    """The ``base`` input: a checkpoint directory, or a base_model artifact whose ``.nemo`` ``download`` fetches."""
    if path.is_dir():
        doc = read_checkpoint(path)
        return Base(
            nemo=path / NEMO_FILE,
            kind="checkpoint",
            reference=dict(doc.get("base") or {}),
            step=int(doc["step"]) if isinstance(doc.get("step"), int) else None,
            tokenizer=dict(doc.get("tokenizer") or {}),
        )
    try:
        doc = json.loads(path.read_bytes())
    except (OSError, ValueError) as e:
        raise StepInputError(f"the base input is neither a checkpoint directory nor a base_model artifact: {e}") from e
    if not isinstance(doc, dict) or doc.get("format") != BASE_MODEL_FORMAT:
        raise StepInputError(f"the base input is not a {BASE_MODEL_FORMAT} artifact")
    family = doc.get("family") or {}
    fname = family.get("name") if isinstance(family, dict) else family
    if fname and fname != NAME:
        raise StepInputError(f"the base model belongs to family {fname!r}, not {NAME}")
    model = doc.get("model") or {}
    if not model.get("hfRepo") or not model.get("revision") or not model.get("checkpointFile"):
        raise StepInputError("the base model names no hfRepo, revision and checkpointFile")
    ref = {
        "hfRepo": model["hfRepo"],
        "revision": model["revision"],
        "checkpointFile": model["checkpointFile"],
        "versionId": doc.get("versionId"),
        "collection": doc.get("collection"),
    }
    tokenizer = {
        "kind": "sentencepiece",
        "source": "base-model",
        "hfRepo": model["hfRepo"],
        "revision": model["revision"],
    }
    return Base(nemo=download(model), kind="base_model", reference=ref, tokenizer=tokenizer)


def download_base(model: Mapping[str, Any]) -> Path:
    """The base model's ``.nemo`` at its pinned revision: from the Hugging Face cache when present (a read-only cache
    works), else downloaded into it (``HF_HOME``; the worker image sets /var/lib/cadence/hf)."""
    import huggingface_hub as hf  # from the NeMo Speech image

    repo, revision, filename = str(model["hfRepo"]), str(model["revision"]), str(model["checkpointFile"])
    for cache in [None, *filter(None, os.environ.get("CADENCE_HF_READONLY_CACHES", "").split(":"))]:
        hit = hf.try_to_load_from_cache(repo, filename, cache_dir=cache, revision=revision)
        if isinstance(hit, str) and Path(hit).is_file():
            return Path(hit)
    return Path(hf.hf_hub_download(repo, filename, revision=revision))


# ---------------------------------------------------------------- averaging (.nemo tars, CPU only)


def _find(names: list[str], name: str) -> str:
    for k in names:
        if k == name or k.endswith("/" + name):
            return k
    raise StepInputError(f"the .nemo file has no {name}")


def _load_weights(tar: tarfile.TarFile, member: str) -> dict[str, Any]:
    import torch

    f = tar.extractfile(member)
    if f is None:
        raise StepInputError(f"the .nemo member {member} is not a file")
    state = torch.load(f, map_location="cpu", weights_only=True)
    if not isinstance(state, dict):
        raise StepInputError("model_weights.ckpt is not a state dict")
    return state


def average_nemo(sources: list[Path], out: Path, progress: Callable[[int, int], None] | None = None) -> None:
    """Write ``out`` (a .nemo) whose weights are the mean of ``sources``' weights. Memory: about two copies of the
    weights (the float32 running sum and the checkpoint being read)."""
    import torch

    if len(sources) < 2:
        raise StepInputError("averaging needs at least two checkpoints (checkpoints.0, checkpoints.1, …)")
    with tarfile.open(sources[0], "r:*") as tar:
        names = [m.name for m in tar.getmembers() if m.isfile()]
        wkey, ckey = _find(names, WEIGHTS_MEMBER), _find(names, CONFIG_MEMBER)
        config = tar.extractfile(ckey).read()  # type: ignore[union-attr]
        acc = _load_weights(tar, wkey)
    dtypes = {k: v.dtype for k, v in acc.items() if torch.is_tensor(v) and v.is_floating_point()}
    for k in dtypes:
        acc[k] = acc[k].float() if acc[k].dtype != torch.float32 else acc[k]
    if progress:
        progress(1, len(sources))
    for i, src in enumerate(sources[1:], 1):
        with tarfile.open(src, "r:*") as tar:
            other = [m.name for m in tar.getmembers() if m.isfile()]
            if tar.extractfile(_find(other, CONFIG_MEMBER)).read() != config:  # type: ignore[union-attr]
                raise StepInputError(
                    f"checkpoint {i} has another model configuration than checkpoint 0; cannot average"
                )
            state = _load_weights(tar, _find(other, WEIGHTS_MEMBER))
        if set(state) != set(acc):
            raise StepInputError(f"checkpoint {i} has other tensors than checkpoint 0; cannot average")
        for k in dtypes:
            acc[k].add_(state[k].float())
        del state
        if progress:
            progress(i + 1, len(sources))
    for k, dt in dtypes.items():
        acc[k] = acc[k].div_(len(sources)).to(dt)
    buf = io.BytesIO()
    torch.save(acc, buf)
    del acc
    out.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(sources[0], "r:*") as src_tar, tarfile.open(out, "w:") as tar:
        for m in src_tar.getmembers():
            if not m.isfile():
                continue
            info = tarfile.TarInfo(m.name)
            info.mode = 0o644
            if m.name == wkey:
                info.size = buf.getbuffer().nbytes
                buf.seek(0)
                tar.addfile(info, buf)
            else:
                info.size = m.size
                tar.addfile(info, src_tar.extractfile(m))
