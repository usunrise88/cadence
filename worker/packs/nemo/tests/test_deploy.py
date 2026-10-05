"""The Nemotron deployable without NeMo or a card (phase 5 · stream D1): the TensorRT table and builder lookup, the
Triton model configuration, deployable.json and its manifest hash, the smoke inputs and the smoke client."""

from __future__ import annotations

import base64
import hashlib
import http.server
import json
import struct
import threading
from collections.abc import Iterator
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
import pytest

from cadence_nemo import deploy, smoke_client
from cadence_nemo.export_graph import STATE_NAMES
from cadence_nemo.family import FAMILY
from cadence_nemo.steps.export import ExportParams, ExportStep, engine_facts
from cadence_nemo.steps.parity import ParityParams, ParityStep
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

# The 80 ms geometry spike E1 exported (streaming_cfg.json of serve-80-fp32, trimmed to what the builder reads).
GEO: dict[str, Any] = {
    "att_context_size": [56, 0],
    "buffer_frames": 17,
    "chunk_ms": 80,
    "n_mels": 128,
    "sample_rate": 16000,
    "valid_out_len": 1,
    "max_symbols": 10,
    "float_state": True,
    "precision": "fp32",
    "state_shapes": {
        "cache_last_channel": [24, 56, 1024],
        "cache_last_time": [24, 1024, 8],
        "cache_last_channel_len": [1],
        "dec_h": [2, 640],
        "dec_c": [2, 640],
        "last_token": [1],
    },
    "state_io": {k: list(v) for k, v in STATE_NAMES.items()},
}


def test_tensorrt_for_the_server() -> None:
    assert deploy.tensorrt_for("26.08") == "11.2.1"
    with pytest.raises(deploy.DeployError, match=r"no TensorRT is known for Triton '26\.07'"):
        deploy.tensorrt_for("26.07")


def test_trtexec_lookup(tmp_path: Path) -> None:
    with pytest.raises(deploy.DeployError, match=r"needs an engine built with TensorRT 11\.2\.1"):
        deploy.trtexec_for("26.08", tmp_path)
    exe = tmp_path / "11.2.1" / "bin" / "trtexec"
    exe.parent.mkdir(parents=True)
    exe.write_text("#!/bin/sh\n")
    got, lib, version = deploy.trtexec_for("26.08", tmp_path)
    assert (got, lib, version) == (exe, tmp_path / "11.2.1" / "lib", "11.2.1")
    assert deploy.installed_tensorrt(tmp_path) == ["11.2.1"]


def test_trtexec_args_are_strict_fp32() -> None:
    args = deploy.trtexec_args(Path("/x/step.onnx"), Path("/x/model.plan"), GEO, 64, 32, 2048)
    assert "--noTF32" in args
    assert not any("fp16" in a for a in args)
    mx = next(a for a in args if a.startswith("--maxShapes="))
    assert mx.startswith(
        "--maxShapes=audio_signal:64x128x17,length:64x1,start:64x1,prompt:64x1,kv_cache_in:64x24x56x1024"
    )
    assert "prev_token_in:64x1" in mx
    assert next(a for a in args if a.startswith("--optShapes=")).startswith("--optShapes=audio_signal:32x")


def test_config_pbtxt() -> None:
    text = deploy.config_pbtxt(GEO, max_batch=64, max_streams=128, queue_us=1000)
    lines = [ln.strip() for ln in text.splitlines() if not ln.startswith("#")]
    assert not any(ln.startswith("name:") for ln in lines), "the directory names the model"
    assert 'platform: "tensorrt_plan"' in text
    assert "max_batch_size: 64" in text
    assert "max_candidate_sequences: 128 max_queue_delay_microseconds: 1000" in text
    assert "CONTROL_SEQUENCE_START" in text
    assert 'name: "start"' in text
    assert text.count("data_type: TYPE_FP32 dims: [ 24, 56, 1024 ]") == 2
    assert "TYPE_INT64 dims: [ 1 ]\n      initial_state" not in text  # every state FP32
    assert "use_growable_memory" not in text
    assert 'name: "tokens" data_type: TYPE_INT32 dims: [ 10 ]' in text
    assert 'name: "audio_signal" data_type: TYPE_FP32 dims: [ 128, 17 ]' in text


def test_config_refuses_what_triton_mishandles() -> None:
    with pytest.raises(deploy.DeployError, match="FP32"):
        deploy.config_pbtxt({**GEO, "float_state": False}, max_batch=64, max_streams=8, queue_us=0)
    bad = {**GEO, "state_io": {**GEO["state_io"], "cache_last_channel_len": ["kv_cache_in_len", "fill_out"]}}
    with pytest.raises(deploy.DeployError, match="must not extend"):
        deploy.config_pbtxt(bad, max_batch=64, max_streams=8, queue_us=0)


def test_state_per_stream_is_e1s() -> None:
    assert deploy.state_mb_per_stream(GEO) == pytest.approx(12.6, abs=0.1)


def test_manifest_matches_sha256sum(tmp_path: Path) -> None:
    d = tmp_path / "model"
    (d / "1").mkdir(parents=True)
    (d / "config.pbtxt").write_text("platform\n")
    (d / "1" / "model.plan").write_bytes(b"\0" * 1000)
    files = deploy.model_files(d)
    assert [f["path"] for f in files] == ["1/model.plan", "config.pbtxt"]
    lines = "".join(f"{hashlib.sha256((d / f['path']).read_bytes()).hexdigest()}  {f['path']}\n" for f in files)
    assert deploy.manifest_sha256(files) == hashlib.sha256(lines.encode()).hexdigest()
    (d / "1" / "bad name").write_text("x")
    with pytest.raises(deploy.DeployError):
        deploy.model_files(d)


def test_streaming_cfg_is_what_the_serve_client_reads(tmp_path: Path) -> None:
    """The model directory's streaming_cfg.json carries what nemotron_serve's client needs besides the geometry: the
    tokenizer's pieces and the mel front end (stream D12: D1's export wrote neither)."""
    from cadence_nemo import serving

    pre = {"_target_": "nemo.collections.asr.modules.AudioToMelSpectrogramPreprocessor", "n_fft": 512, "features": 128}
    geo = {
        **GEO,
        "chunk_size": [1, 8],
        "pre_encode_cache_size": [0, 9],
        "window_stride_s": 0.01,
        "prompt_dictionary": {"sr-RS": 7, "auto": 101},
        "blank_id": 3,
        "vocab_size": 3,
        "vocabulary": ["▁a", "b", "<sr-RS>"],
        "frontend": {"kind": "nemo", "preprocessor": pre},
    }
    deploy.write_streaming_cfg(tmp_path / "model" / deploy.STREAMING_CFG, geo)
    g = serving.Geometry.from_cfg(json.loads((tmp_path / "model" / deploy.STREAMING_CFG).read_text(encoding="utf-8")))
    assert g.vocabulary == ["▁a", "b", "<sr-RS>"]
    assert g.frontend == {"kind": "nemo", "preprocessor": pre}
    assert g.half == 256
    assert g.buffer_frames == 17
    assert serving.detokenize([0, 1, 2], g.vocabulary) == "ab"
    for drop, match in (("vocabulary", "vocabulary"), ("frontend", "front end")):
        with pytest.raises(deploy.DeployError, match=match):
            deploy.write_streaming_cfg(tmp_path / "x.json", {k: v for k, v in geo.items() if k != drop})
    with pytest.raises(deploy.DeployError, match="3 pieces"):
        deploy.write_streaming_cfg(tmp_path / "x.json", {**geo, "vocab_size": 4})


def test_deployable_doc() -> None:
    files = [
        {"path": "1/model.plan", "sha256": "a" * 64, "bytes": 2_559_655_140},
        {"path": "config.pbtxt", "sha256": "b" * 64, "bytes": 2141},
    ]
    doc = deploy.deployable_doc(
        geo=GEO,
        family="f",
        profile="80ms",
        weights_hash="b3:x",
        server_version="26.08",
        engine={"kind": "tensorrt", "version": "11.2.1"},
        files=files,
        max_batch=64,
        max_streams=128,
        overhead_mb=2300,
    )
    s = doc["serving"]
    assert doc["schema"] == deploy.SCHEMA
    assert doc["format"] == deploy.FORMAT
    assert s["modelDir"] == "model"
    assert s["input"] == "features"
    assert s["server"]["version"] == "26.08"
    assert s["cudaMemoryPoolMb"] == 1614  # 128 streams x 12.6 MB
    assert s["memoryMb"] == 2560 + 1614 + 2300
    assert doc["manifestSha256"] == deploy.manifest_sha256(files)
    assert doc["smoke"] == {"client": "client/transcribe", "input": deploy.CHUNKS_FORMAT}


def _chunks() -> list[tuple[np.ndarray[Any, Any], int, bool]]:
    rng = np.random.default_rng(0)
    return [
        (rng.standard_normal((128, 1)).astype(np.float32), 1, True),
        (rng.standard_normal((128, 17)).astype(np.float32), 17, False),
        (rng.standard_normal((128, 12)).astype(np.float32), 12, False),
    ]


def test_chunks_round_trip_through_the_client(tmp_path: Path) -> None:
    chunks = _chunks()
    doc = deploy.encode_chunks(29, 17, chunks)
    f = tmp_path / "01.json"
    deploy.write_json(f, doc)
    got = smoke_client.read_chunks(str(f))
    assert got["prompt"] == 29
    assert got["mels"] == 128
    assert got["frames"] == 17
    for (x, length, first), c in zip(chunks, got["chunks"], strict=True):
        buf = np.zeros((128, 17), np.float32)
        buf[:, : x.shape[1]] = x
        assert np.array_equal(np.array(c["values"], np.float32).reshape(128, 17), buf)
        assert c["length"] == length
        assert c["start"] == (1 if first else 0)
    # JSON numbers carry the float32 values exactly
    body = json.loads(json.dumps(smoke_client.request_body(got, 1, 7)))
    assert np.array_equal(
        np.array(body["inputs"][0]["data"], np.float32), np.array(got["chunks"][1]["values"], np.float32)
    )
    assert body["parameters"] == {"sequence_id": 7, "sequence_start": False, "sequence_end": False}
    assert body["inputs"][0]["shape"] == [1, 128, 17]
    assert body["inputs"][2]["data"] == [29]
    last = smoke_client.request_body(got, 2, 7)["parameters"]
    assert last["sequence_end"] is True


class _Triton(http.server.BaseHTTPRequestHandler):
    seen: ClassVar[list[dict[str, Any]]] = []

    def do_POST(self) -> None:
        n = int(self.headers["Content-Length"])
        body = json.loads(self.rfile.read(n))
        _Triton.seen.append({"path": self.path, **body["parameters"]})
        k = len(_Triton.seen)
        answer = {"outputs": [{"name": "tokens", "datatype": "INT32", "shape": [1, 10], "data": [k, -1] + [-1] * 8}]}
        raw = json.dumps(answer).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *args: Any) -> None:
        pass


@pytest.fixture
def server() -> Iterator[str]:
    _Triton.seen = []
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Triton)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{srv.server_address[1]}"
    srv.shutdown()


def test_smoke_client_streams_one_sequence(tmp_path: Path, server: str, capsys: pytest.CaptureFixture[str]) -> None:
    f = tmp_path / "01.json"
    deploy.write_json(f, deploy.encode_chunks(29, 17, _chunks()))
    assert smoke_client.main(["transcribe", server, "asr-x-1", str(f)]) == 0
    assert capsys.readouterr().out == "1 2 3\n"
    seen = _Triton.seen
    assert [s["path"] for s in seen] == ["/v2/models/asr-x-1/infer"] * 3
    assert [(s["sequence_start"], s["sequence_end"]) for s in seen] == [(True, False), (False, False), (False, True)]
    assert len({s["sequence_id"] for s in seen}) == 1
    assert smoke_client.main(["transcribe", "http://127.0.0.1:9", "m", str(f)]) == 1


def test_smoke_client_refuses_other_files(tmp_path: Path) -> None:
    f = tmp_path / "x.json"
    f.write_text(json.dumps({"schema": "other"}))
    with pytest.raises(smoke_client.SmokeError):
        smoke_client.read_chunks(str(f))
    f.write_text(
        json.dumps(
            {
                "schema": deploy.CHUNKS_FORMAT,
                "prompt": 0,
                "mels": 2,
                "frames": 2,
                "chunks": [{"length": 1, "start": 1, "data": base64.b64encode(struct.pack("<3f", 1, 2, 3)).decode()}],
            }
        )
    )
    with pytest.raises(smoke_client.SmokeError, match="12 bytes, expected 16"):
        smoke_client.read_chunks(str(f))


def test_engine_facts() -> None:
    log = (
        "[I] Selected Device: NVIDIA RTX PRO 6000 Blackwell Workstation Edition\n"
        "[I] Compute Capability: 12.0\n"
        "[I] [TRT] [MemUsageStats] Peak memory usage of TRT CPU/GPU memory allocators: CPU 0 MiB, GPU 2434 MiB\n"
        "[I] Engine built in 32.5948 sec.\n"
    )
    assert engine_facts(log) == {
        "gpu": "NVIDIA RTX PRO 6000 Blackwell Workstation Edition",
        "computeCapability": "12.0",
        "buildSeconds": 32.5948,
        "buildPeakTrtGpuMb": 2434,
    }


def test_export_refuses_a_server_without_builder(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(deploy.TENSORRT_ROOT_ENV, str(tmp_path / "none"))
    ctx = StepContext(lambda e: None, work_dir=tmp_path)
    with pytest.raises(StepInputError, match="no TensorRT builder"):
        ExportStep().run(ExportParams(), {"model": tmp_path}, {"deployable": tmp_path / "out"}, ctx)
    with pytest.raises(StepInputError, match="no TensorRT is known"):
        ExportStep().run(
            ExportParams(server_version="26.07"), {"model": tmp_path}, {"deployable": tmp_path / "out"}, ctx
        )
    with pytest.raises(StepInputError, match="exports triton-tensorrt-cache-aware"):
        ExportStep().run(ExportParams(format="onnx"), {"model": tmp_path}, {"deployable": tmp_path / "out"}, ctx)


def test_family_maps_the_deploy_roles() -> None:
    d: dict[str, Any] = dict(FAMILY.descriptor)
    assert d["roles"]["export"] == "nemotron_export"
    assert d["roles"]["parity"] == "nemotron_parity"
    assert d["roles"]["serve"] == "nemotron_serve"
    assert d["exportFormats"] == [{"format": deploy.FORMAT, "server": "triton", "default": True}]
    assert ExportStep.role == "export"
    assert ExportStep.resources["jobKind"] == "export"
    assert ParityStep.role == "parity"
    assert ParityStep.produces == {"hypotheses": "hypotheses", "smoke": "smoke_inputs"}
    assert ParityParams().batch_size == 8
    assert ParityParams().smoke_utterances == 20
    assert ExportParams().max_streams == 128
    assert ExportParams().server_version == "26.08"
