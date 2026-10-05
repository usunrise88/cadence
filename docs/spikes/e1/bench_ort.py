"""E1 helper: steady-state time of one step.onnx call per batch size under ONNX Runtime (CUDA EP), state on the card.

  docs/spikes/e1/run.sh --gpu python /repo/docs/spikes/e1/bench_ort.py <onnx_dir> [B,B,...] [--profile] [--opt-out f]

The state outputs are bound to card buffers and fed back as the next call's inputs (I/O binding, ping-pong), which is
what Triton's implicit state does for the ONNX Runtime backend; features come from the host each call (17x128 floats
per stream). Prints ms per call and per stream, and the share of nodes ORT placed on the CPU (shape subgraphs).
"""

from __future__ import annotations

import json
import os
import sys
import time

import numpy as np
import onnxruntime as ort

GB = 1024**3


def main() -> None:
    d = sys.argv[1]
    sizes = [int(v) for v in (sys.argv[2] if len(sys.argv) > 2 and not sys.argv[2].startswith("--") else "1,8,16,32,64").split(",")]
    geo = json.load(open(os.path.join(d, "streaming_cfg.json")))
    so = ort.SessionOptions()
    so.log_severity_level = 3
    so.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
    if "--opt-out" in sys.argv:
        so.optimized_model_filepath = sys.argv[sys.argv.index("--opt-out") + 1]
    if "--profile" in sys.argv:
        so.enable_profiling = True
        so.profile_file_prefix = "/work/tmp/ortprof"
    cuda = {"gpu_mem_limit": int(float(os.environ.get("ORT_GB", "4")) * GB), "arena_extend_strategy": "kSameAsRequested",
            "cudnn_conv_algo_search": os.environ.get("CONV_SEARCH", "EXHAUSTIVE"),
            "cudnn_conv_use_max_workspace": "1", "use_tf32": os.environ.get("TF32", "0")}
    t0 = time.time()
    sess = ort.InferenceSession(os.path.join(d, "step.onnx"), so, providers=[("CUDAExecutionProvider", cuda)])
    print(f"load {time.time() - t0:.1f}s providers {sess.get_providers()}")
    shapes = geo["state_shapes"]
    sio = geo.get("state_io") or {n: [n, f"{n}_next"] for n in shapes}
    idt = np.float32 if geo.get("float_state") else np.int64
    T, M, blank = geo["buffer_frames"], geo["n_mels"], geo["blank_id"]
    res = {}
    for B in sizes:
        st = {}
        for n, s in shapes.items():
            dt = idt if n in ("cache_last_channel_len", "last_token") else np.float32
            v = np.full([B, *s], blank if n == "last_token" else 0, dt)
            st[n] = ort.OrtValue.ortvalue_from_numpy(v, "cuda", 0)
        feats = np.random.randn(B, M, T).astype(np.float32) * 0.1
        times = []
        for it in range(60):
            io = sess.io_binding()
            io.bind_cpu_input("audio_signal", feats)
            io.bind_cpu_input("length", np.full((B, 1), T, np.int64))
            io.bind_cpu_input("start", np.full((B, 1), 1 if it == 0 else 0, np.int32))
            io.bind_cpu_input("prompt", np.full((B, 1), 29, np.int64))
            for n in shapes:
                io.bind_ortvalue_input(sio[n][0], st[n])
                io.bind_output(sio[n][1], "cuda")
            io.bind_output("tokens", "cpu")
            io.bind_output("encoded_len", "cpu")
            s = time.perf_counter()
            sess.run_with_iobinding(io)
            io.synchronize_outputs()
            times.append(time.perf_counter() - s)
            bound = [sio[n][1] for n in shapes] + ["tokens", "encoded_len"]  # get_outputs() is in binding order
            outs = dict(zip(bound, io.get_outputs(), strict=True))
            for n in shapes:
                st[n] = outs[sio[n][1]]
        a = np.array(times[10:]) * 1000
        res[B] = {"ms_p50": round(float(np.percentile(a, 50)), 2), "ms_p95": round(float(np.percentile(a, 95)), 2),
                  "ms_per_stream": round(float(np.percentile(a, 50)) / B, 3)}
        print(B, json.dumps(res[B]), flush=True)
    if "--profile" in sys.argv:
        print("profile", sess.end_profiling())


if __name__ == "__main__":
    main()
