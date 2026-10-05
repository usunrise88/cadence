"""The Nemotron family's deployable (``cadence.deployable/1``, phase 5 · stream D1): the Triton repository builder
and the pure parts of ``nemotron_export`` and ``nemotron_parity`` (no NeMo, no torch: unit-tested anywhere).

The served model is spike E1's *step graph* (docs/spikes/E1-onnx-triton.md): one call is one chunk of B streams, the
encoder caches, the prediction network's state and the last token are inputs and ``_out`` outputs, and Triton's
sequence batcher keeps them per stream as implicit state on the card. The request carries the pipeline decoder's
feature buffer (``cadence_nemo.pipeline``: the pre-encode cache and a chunk of log-mel frames); the caller sends
features, endpointing and detokenisation stay in the caller (E1 "Endpointing and text stay outside the server"; a
served featuriser is the next item).

Layout of the artifact (a directory):

    deployable.json        the header the control plane, the serve step and the delivery script read
    model/                 serving.modelDir: a Triton model directory (config.pbtxt names no model: the directory does)
      config.pbtxt
      1/model.plan         the TensorRT engine, fp32 with TF32 off (E1: fp16 breaks parity)
    client/transcribe      the smoke client the delivery script runs (python3, standard library only)
    onnx/step.onnx         the portable artifact: the fp32 step graph, its weights and streaming_cfg.json
    onnx/step.onnx.data
    onnx/streaming_cfg.json

E1's Triton 26.08 workarounds are built in: implicit states named apart (no name extends another), every state FP32,
zero initial state (the graph resets on ``start``), never ``use_growable_memory``, and a CUDA memory pool of at least
``maxStreams`` times the per-stream state in and out (``serving.cudaMemoryPoolMb``).
"""

from __future__ import annotations

import base64
import hashlib
import json
import math
import os
import re
from collections.abc import Iterable, Mapping, Sequence
from pathlib import Path
from typing import Any

import numpy as np

SCHEMA = "cadence.deployable/1"
FORMAT = "triton-tensorrt-cache-aware"
SERVER_KIND = "triton"
MODEL_DIR = "model"
PLAN_FILE = "1/model.plan"
CONFIG_FILE = "config.pbtxt"
CLIENT_FILE = "client/transcribe"
ONNX_DIR = "onnx"
DEPLOYABLE_JSON = "deployable.json"
CHUNKS_FORMAT = "cadence.nemo-chunks/1"
SMOKE_SCHEMA = "cadence.smoke-inputs/1"
SMOKE_JSON = "smoke.json"

# The TensorRT an engine must be built with for each Triton release the staging and delivery targets run: a plan is
# specific to the TensorRT version (and the GPU), so the builder refuses a server it has no matching TensorRT for.
# Triton 26.08 ships TensorRT 11.2.1 (E1; `trtexec` reports v110201).
TENSORRT_FOR_SERVER: dict[str, str] = {"26.08": "11.2.1"}
# Where the worker image keeps each TensorRT builder (worker/Dockerfile): <root>/<version>/bin/trtexec with its
# libraries in <root>/<version>/lib, apart from the TensorRT 10 the NeMo image ships.
TENSORRT_ROOT_ENV = "CADENCE_TENSORRT_ROOT"
TENSORRT_ROOT = "/opt/tensorrt"

# Request inputs besides the states; `start` is wired to Triton's CONTROL_SEQUENCE_START.
REQUEST_INPUTS = ("audio_signal", "length", "start", "prompt")
STATE_ORDER = ("cache_last_channel", "cache_last_time", "cache_last_channel_len", "dec_h", "dec_c", "last_token")
FLOAT_BYTES = 4

_SEG = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._-]{0,199}$")


class DeployError(Exception):
    """A deployable cannot be built as asked (reported by the steps as an input error)."""


# ---------------------------------------------------------------- TensorRT


def tensorrt_for(server_version: str) -> str:
    """The TensorRT version an engine for this Triton release needs; DeployError when the pack knows none."""
    v = TENSORRT_FOR_SERVER.get(server_version.strip())
    if v is None:
        known = ", ".join(f"Triton {s} → TensorRT {t}" for s, t in sorted(TENSORRT_FOR_SERVER.items()))
        raise DeployError(
            f"no TensorRT is known for Triton {server_version!r}; an engine must be built with the server's own "
            f"TensorRT (known: {known})"
        )
    return v


def tensorrt_root() -> Path:
    return Path(os.environ.get(TENSORRT_ROOT_ENV) or TENSORRT_ROOT)


def installed_tensorrt(root: Path | None = None) -> list[str]:
    r = root or tensorrt_root()
    if not r.is_dir():
        return []
    return sorted(d.name for d in r.iterdir() if (d / "bin" / "trtexec").is_file())


def trtexec_for(server_version: str, root: Path | None = None) -> tuple[Path, Path, str]:
    """(trtexec, its library directory, TensorRT version) for the server; DeployError when the image lacks it."""
    want = tensorrt_for(server_version)
    r = root or tensorrt_root()
    exe = r / want / "bin" / "trtexec"
    if not exe.is_file():
        have = installed_tensorrt(r)
        raise DeployError(
            f"Triton {server_version} needs an engine built with TensorRT {want}, and this worker image has "
            f"{', '.join('TensorRT ' + h for h in have) if have else 'no TensorRT builder'} under {r}; "
            "use a worker image built with the matching builder (worker/Dockerfile)"
        )
    return exe, r / want / "lib", want


def shape_arg(geo: Mapping[str, Any], batch: int) -> str:
    """trtexec's shape list for every input of the step graph at batch size ``batch``."""
    sio: Mapping[str, Sequence[str]] = geo["state_io"]
    parts = [
        f"audio_signal:{batch}x{geo['n_mels']}x{geo['buffer_frames']}",
        f"length:{batch}x1",
        f"start:{batch}x1",
        f"prompt:{batch}x1",
    ]
    for s in STATE_ORDER:
        dims = "x".join(str(d) for d in geo["state_shapes"][s])
        parts.append(f"{sio[s][0]}:{batch}x{dims}")
    return ",".join(parts)


def trtexec_args(
    onnx: Path, plan: Path, geo: Mapping[str, Any], max_batch: int, opt_batch: int, workspace_mb: int
) -> list[str]:
    """Strict fp32 (``--noTF32``: NeMo decodes at matmul precision "highest"; E1: TF32 alone moved parity from 99.0 % to
    96.0 % identical), one optimisation profile from 1 to ``max_batch`` streams per execution."""
    opt = max(1, min(opt_batch, max_batch))
    return [
        f"--onnx={onnx}",
        f"--saveEngine={plan}",
        f"--minShapes={shape_arg(geo, 1)}",
        f"--optShapes={shape_arg(geo, opt)}",
        f"--maxShapes={shape_arg(geo, max_batch)}",
        f"--memPoolSize=workspace:{workspace_mb}M",
        "--noTF32",
        "--skipInference",
    ]


# ---------------------------------------------------------------- the Triton model directory


def token_slots(geo: Mapping[str, Any]) -> int:
    v = geo["valid_out_len"]
    out = int(v[1]) if isinstance(v, list | tuple) else int(v)
    return out * int(geo["max_symbols"])


def config_pbtxt(geo: Mapping[str, Any], *, max_batch: int, max_streams: int, queue_us: int) -> str:
    """The model's ``config.pbtxt`` (E1 build_repo.py, TensorRT backend). It sets no ``name``: Triton takes the model
    name from the directory, so the serve step and the delivery script install it under a versioned name."""
    if not geo.get("float_state"):
        raise DeployError("the served step graph keeps every state FP32 (Triton 26.08 hands INT64 states back as FP32)")
    sio: Mapping[str, Sequence[str]] = geo["state_io"]
    names = [n for s in STATE_ORDER for n in sio[s]]
    for a in names:
        for b in names:
            if a != b and b.startswith(a):
                raise DeployError(f"state names must not extend one another (Triton 26.08 confuses {a} and {b})")
    states = []
    for s in STATE_ORDER:
        dims = ", ".join(str(d) for d in geo["state_shapes"][s])
        i_name, o_name = sio[s]
        states.append(
            f'    {{ input_name: "{i_name}" output_name: "{o_name}" data_type: TYPE_FP32 dims: [ {dims} ]\n'
            f'      initial_state: {{ data_type: TYPE_FP32 dims: [ {dims} ] zero_data: true name: "zero_{i_name}" }} }}'
        )
    sep = ",\n"
    att = geo["att_context_size"]
    return (
        f"# Built by cadence_nemo (nemotron_export@1): the {geo.get('precision', 'fp32')} step graph of spike E1 as a\n"
        f"# TensorRT engine, profile {geo['chunk_ms']} ms (att_context_size [{att[0]}, {att[1]}]): a request is one\n"
        f"# feature buffer of {geo['buffer_frames']} frames (pre-encode cache + chunk) and answers"
        f" {token_slots(geo)} token slots (-1 = none).\n"
        "# No name: the directory names the model.\n"
        'platform: "tensorrt_plan"\n'
        f"max_batch_size: {max_batch}\n"
        "sequence_batching {\n"
        "  max_sequence_idle_microseconds: 60000000\n"
        f"  oldest {{ max_candidate_sequences: {max_streams} max_queue_delay_microseconds: {queue_us} }}\n"
        "  control_input [\n"
        '    { name: "start" control [ { kind: CONTROL_SEQUENCE_START int32_false_true: [ 0, 1 ] } ] }\n'
        "  ]\n"
        "  state [\n"
        f"{sep.join(states)}\n"
        "  ]\n"
        "}\n"
        "input [\n"
        f'  {{ name: "audio_signal" data_type: TYPE_FP32 dims: [ {geo["n_mels"]}, {geo["buffer_frames"]} ] }},\n'
        '  { name: "length" data_type: TYPE_INT64 dims: [ 1 ] },\n'
        '  { name: "prompt" data_type: TYPE_INT64 dims: [ 1 ] }\n'
        "]\n"
        "output [\n"
        f'  {{ name: "tokens" data_type: TYPE_INT32 dims: [ {token_slots(geo)} ] }},\n'
        '  { name: "encoded_len" data_type: TYPE_INT64 dims: [ 1 ] }\n'
        "]\n"
        "instance_group [ { count: 1 kind: KIND_GPU } ]\n"
    )


def state_mb_per_stream(geo: Mapping[str, Any]) -> float:
    """A stream's implicit state, in and out (FP32), in MB (10^6 bytes; E1: 6.3 MB each way at 80 ms)."""
    n = sum(math.prod(int(d) for d in dims) for dims in geo["state_shapes"].values())
    return 2 * n * FLOAT_BYTES / 1e6


# ---------------------------------------------------------------- deployable.json


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def manifest_sha256(files: Iterable[Mapping[str, Any]]) -> str:
    """D3's ``delivery.ManifestSHA256``: the SHA-256 of one ``<sha256>  <path>\\n`` line per file (sha256sum's
    format) sorted by path bytes — what ``find | LC_ALL=C sort | sha256sum`` gives over the installed directory."""
    rows = sorted(((str(f["path"]), str(f["sha256"])) for f in files), key=lambda r: r[0].encode())
    text = "".join(f"{s}  {p}\n" for p, s in rows)
    return hashlib.sha256(text.encode()).hexdigest()


def model_files(model_dir: Path) -> list[dict[str, Any]]:
    """Every file of the model directory, paths relative to it, with SHA-256 and size."""
    out: list[dict[str, Any]] = []
    for p in sorted(model_dir.rglob("*")):
        if not p.is_file():
            continue
        rel = p.relative_to(model_dir).as_posix()
        if not all(_SEG.match(seg) for seg in rel.split("/")):
            raise DeployError(f"model file {rel!r} is not a path the delivery script handles")
        out.append({"path": rel, "sha256": sha256_file(p), "bytes": p.stat().st_size})
    out.sort(key=lambda f: str(f["path"]).encode())
    return out


def deployable_doc(
    *,
    geo: Mapping[str, Any],
    family: str,
    profile: str,
    weights_hash: str,
    server_version: str,
    engine: Mapping[str, Any],
    files: Sequence[Mapping[str, Any]],
    max_batch: int,
    max_streams: int,
    overhead_mb: int,
) -> dict[str, Any]:
    """``deployable.json``: the serving metadata and the model directory's files with their manifest hash."""
    plan_bytes = sum(int(f["bytes"]) for f in files if f["path"] == PLAN_FILE)
    per_stream = state_mb_per_stream(geo)
    pool_mb = math.ceil(max_streams * per_stream)
    memory_mb = math.ceil(plan_bytes / 1e6) + pool_mb + overhead_mb
    return {
        "schema": SCHEMA,
        "format": FORMAT,
        "family": family,
        "profile": profile,
        "weightsHash": weights_hash,
        "precision": "fp32",
        "serving": {
            "server": {"kind": SERVER_KIND, "version": server_version, "minVersion": server_version},
            "modelDir": MODEL_DIR,
            "memoryMb": memory_mb,
            "cudaMemoryPoolMb": pool_mb,
            "statePerStreamMb": round(per_stream, 3),
            "maxStreams": max_streams,
            "maxBatch": max_batch,
            "sampleRate": int(geo["sample_rate"]),
            "chunkMs": int(geo["chunk_ms"]),
            "boost": {"static": False, "dynamic": False},
            "engine": dict(engine),
            "input": "features",
        },
        "smoke": {"client": CLIENT_FILE, "input": CHUNKS_FORMAT},
        "onnx": {"dir": ONNX_DIR, "opset": 17},
        "files": [dict(f) for f in files],
        "manifestSha256": manifest_sha256(files),
    }


# ---------------------------------------------------------------- smoke inputs (cadence.nemo-chunks/1)


def encode_chunks(prompt: int, frames: int, chunks: Sequence[tuple[np.ndarray[Any, Any], int, bool]]) -> dict[str, Any]:
    """One stream's feature buffers as the smoke client reads them: each buffer right-padded to ``frames``, float32
    little-endian, mels by frames row-major, base64; ``start`` 1 on the stream's first chunk."""
    out: list[dict[str, Any]] = []
    mels = 0
    for feats, length, first in chunks:
        x = np.asarray(feats, dtype=np.float32)
        mels = int(x.shape[0])
        buf = np.zeros((mels, frames), dtype="<f4")
        n = min(frames, int(x.shape[1]))
        buf[:, :n] = x[:, :n]
        out.append(
            {"length": int(length), "start": 1 if first else 0, "data": base64.b64encode(buf.tobytes()).decode()}
        )
    return {"schema": CHUNKS_FORMAT, "prompt": int(prompt), "mels": mels, "frames": int(frames), "chunks": out}


def smoke_doc(items: Sequence[Mapping[str, str]]) -> dict[str, Any]:
    return {"schema": SMOKE_SCHEMA, "format": CHUNKS_FORMAT, "items": [dict(i) for i in items]}


def smoke_name(i: int) -> str:
    return f"{i + 1:02d}.json"


def write_json(path: Path, doc: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(doc, ensure_ascii=False, indent=1, sort_keys=True) + "\n", encoding="utf-8")
