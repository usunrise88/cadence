"""nemotron_export — the export role of the Nemotron family (phase 5 · stream D1; R46, spike E1): a checkpoint at one
latency profile becomes a ``deployable`` (``cadence.deployable/1``, :mod:`cadence_nemo.deploy`): spike E1's fp32 step
graph as ONNX (the portable artifact), the TensorRT engine built from it on the leased card with the TensorRT of the
target server (fp32, TF32 off), the Triton model directory around the engine (with ``streaming_cfg.json``: the
geometry, the tokenizer's pieces and the mel front end a client needs), ``deployable.json`` with SHA-256 per file and
the manifest hash the delivery script checks, and the smoke client.

The ONNX export runs on the CPU (about 45 s and 5 GB of RAM per profile); ``trtexec`` builds the engine on the card
(E1: 33 s, 2.4 GB of TensorRT allocations for the 80 ms graph). An engine is specific to the GPU and the TensorRT
version: ``server_version`` picks the TensorRT (Triton 26.08 → TensorRT 11.2.1) and the step refuses a server the
worker image has no builder for; ``card_class`` (the target's) and the card's name and compute capability are
recorded in ``serving.engine``. Help: docs/help/steps/nemotron-export.md.
"""

from __future__ import annotations

import os
import re
import shutil
import stat
import subprocess
import tempfile
import time
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo import checkpoint as ck
from cadence_nemo import deploy
from cadence_nemo.family import NAME, RUNTIME, att_context_size, profile
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

TRTEXEC_TIMEOUT_S = 3600
CLIENT_SOURCE = Path(__file__).resolve().parents[1] / "smoke_client.py"


class ExportParams(BaseModel):
    profile: str = cadence_field(
        default_ref="packs.nemo.profile", description="Latency profile the step graph is exported for"
    )
    format: str = cadence_field(default_ref="packs.nemo.export_format")
    card_class: str = cadence_field(default_ref="packs.nemo.export_card_class")
    server_version: str = cadence_field(default_ref="packs.nemo.export_server_version")
    max_batch: int = cadence_field(default_ref="packs.nemo.export_max_batch")
    opt_batch: int = cadence_field(default_ref="packs.nemo.export_opt_batch")
    max_streams: int = cadence_field(default_ref="packs.nemo.export_max_streams")
    queue_delay_us: int = cadence_field(default_ref="packs.nemo.export_queue_delay_us")
    workspace_mb: int = cadence_field(default_ref="packs.nemo.export_workspace_mb")
    serving_overhead_mb: int = cadence_field(default_ref="packs.nemo.export_serving_overhead_mb")


def _log_value(log: str, pattern: str) -> str:
    m = re.search(pattern, log)
    return m.group(1).strip() if m else ""


def engine_facts(log: str) -> dict[str, Any]:
    """What trtexec's log says about the card and the build."""
    out: dict[str, Any] = {
        "gpu": _log_value(log, r"Selected Device:\s*(.+)"),
        "computeCapability": _log_value(log, r"Compute Capability:\s*([0-9.]+)"),
    }
    if s := _log_value(log, r"Engine built in ([0-9.]+) sec"):
        out["buildSeconds"] = float(s)
    if s := _log_value(log, r"Peak memory usage of TRT CPU/GPU memory allocators: CPU \d+ MiB, GPU (\d+) MiB"):
        out["buildPeakTrtGpuMb"] = int(s)
    return out


def build_engine(exe: Path, lib: Path, args: list[str], log_path: Path) -> str:
    """Run trtexec with its own TensorRT libraries first on the library path (the image's TensorRT 10 stays apart)."""
    env = dict(os.environ)
    env["LD_LIBRARY_PATH"] = os.pathsep.join(p for p in (str(lib), env.get("LD_LIBRARY_PATH", "")) if p)
    with log_path.open("w", encoding="utf-8") as f:
        proc = subprocess.run(
            [str(exe), *args], stdout=f, stderr=subprocess.STDOUT, env=env, timeout=TRTEXEC_TIMEOUT_S, check=False
        )
    log = log_path.read_text(encoding="utf-8", errors="replace")
    if proc.returncode != 0 or "PASSED TensorRT.trtexec" not in log:
        tail = "\n".join(log.splitlines()[-25:])
        raise RuntimeError(f"trtexec failed (exit {proc.returncode}):\n{tail}")
    return log


def write_client(out: Path) -> None:
    dst = out / deploy.CLIENT_FILE
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(CLIENT_SOURCE, dst)
    dst.chmod(dst.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


class ExportStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"model": "checkpoint"}
    produces: ClassVar[Mapping[str, str]] = {"deployable": "deployable"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 6, "diskGb": 12, "jobKind": "export"}
    role: ClassVar[str] = "export"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = ExportParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ExportParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"{NAME} has no latency profile {p.profile!r}") from e
        if p.format != deploy.FORMAT:
            raise StepInputError(f"{NAME} exports {deploy.FORMAT}, not {p.format!r}")
        if p.opt_batch > p.max_batch:
            raise StepInputError(f"opt_batch {p.opt_batch} is above max_batch {p.max_batch}")
        try:
            exe, lib, trt_version = deploy.trtexec_for(p.server_version)
        except deploy.DeployError as e:
            raise StepInputError(str(e)) from e
        meta = ck.read_checkpoint(inputs["model"])
        nemo = inputs["model"] / ck.NEMO_FILE
        whash = str(meta.get("weightsHash") or ck.weights_hash(nemo))
        att = att_context_size(prof)
        out = outputs["deployable"]
        out.mkdir(parents=True, exist_ok=True)
        # Restoring a .nemo unpacks it into the temp directory (2.5 GB): keep that in the lease's scratch.
        tempfile.tempdir = str(ctx.work_dir)
        ctx.progress(0.0, f"exporting the step graph at {prof['name']} on the CPU")
        geo = _export(nemo, att, out / deploy.ONNX_DIR, ctx.work_dir)
        ctx.log("step graph exported", seconds=geo.get("export_seconds"), nodes=geo.get("onnx_nodes"))
        ctx.progress(0.4, f"building the TensorRT {trt_version} engine (fp32, TF32 off)")
        model_dir = out / deploy.MODEL_DIR
        plan = model_dir / deploy.PLAN_FILE
        plan.parent.mkdir(parents=True, exist_ok=True)
        args = deploy.trtexec_args(
            out / deploy.ONNX_DIR / "step.onnx", plan, geo, p.max_batch, p.opt_batch, p.workspace_mb
        )
        t0 = time.perf_counter()
        try:
            log = build_engine(exe, lib, args, ctx.work_dir / "trtexec.log")
        except subprocess.TimeoutExpired as e:
            raise RuntimeError(f"trtexec did not finish in {TRTEXEC_TIMEOUT_S} s") from e
        facts = engine_facts(log)
        ctx.log("engine built", seconds=round(time.perf_counter() - t0, 1), **facts)
        try:
            config = deploy.config_pbtxt(
                geo, max_batch=p.max_batch, max_streams=p.max_streams, queue_us=p.queue_delay_us
            )
        except deploy.DeployError as e:
            raise StepInputError(str(e)) from e
        (model_dir / deploy.CONFIG_FILE).write_text(config, encoding="utf-8")
        # The model directory is self-contained: the serve step and the delivery bundle read the client's geometry,
        # vocabulary and front end beside the engine.
        deploy.write_streaming_cfg(model_dir / deploy.STREAMING_CFG, geo)
        write_client(out)
        engine = {
            "kind": "tensorrt",
            "version": trt_version,
            "precision": "fp32",
            "tf32": False,
            "cardClass": p.card_class,
            "gpu": facts.get("gpu", ""),
            "computeCapability": facts.get("computeCapability", ""),
            "optBatch": p.opt_batch,
        }
        try:
            files = deploy.model_files(model_dir)
        except deploy.DeployError as e:
            raise StepInputError(str(e)) from e
        doc = deploy.deployable_doc(
            geo=geo,
            family=NAME,
            profile=prof["name"],
            weights_hash=whash,
            server_version=p.server_version,
            engine=engine,
            files=files,
            max_batch=p.max_batch,
            max_streams=p.max_streams,
            overhead_mb=p.serving_overhead_mb,
        )
        deploy.write_json(out / deploy.DEPLOYABLE_JSON, doc)
        ctx.set_meta(
            "deployable",
            {
                "schema": deploy.SCHEMA,
                "format": deploy.FORMAT,
                "family": NAME,
                "profile": prof["name"],
                "weightsHash": whash,
                "precision": "fp32",
                "engine": engine,
                "server": doc["serving"]["server"],
                "memoryMb": doc["serving"]["memoryMb"],
                "manifestSha256": doc["manifestSha256"],
            },
        )
        ctx.progress(1.0, f"{deploy.FORMAT} at {prof['name']}: {doc['serving']['memoryMb']} MB to serve")


def _export(nemo: Path, att: list[int], out: Path, scratch: Path) -> dict[str, Any]:
    from cadence_nemo import export_graph

    return export_graph.export(nemo, att, out, scratch)
