"""E1 step 3d: S concurrent real-time streams against the step model; latency per chunk and time to final.

  docs/spikes/e1/triton/client.sh python /repo/docs/spikes/e1/triton/client.py /work/bench/feats-80.npz \
      --streams 1,8,16,32 --seconds 60 [--procs 1] [--out /work/results/triton-x.json]

Each stream plays the parity sample's clips one after another (stream s starts at clip s*7 mod N), one Triton sequence
per clip (sequence_start on the first chunk, sequence_end on the last). Chunk k becomes available when its audio has
arrived: t_clip + sum(real[0..k]) / 16 kHz; it is sent then, or when the stream's previous response came back if that
is later (a sequence is strictly ordered). Measured, from the moment the chunk's audio was complete:
  * latency  — until its tokens are back (every chunk), p50/p95/p99;
  * time to final — the same for the clip's last chunk (the final at the end of the audio, R54's measure with the
    utterance end at the clip end; the 800 ms endpointing wait of the decoder config is the same served or not and is
    left out).
Also: real-time factor (client wall time per audio second, per stream) and, from Triton's metrics, executions, average
batch and compute per execution. The first 5 s of every level are warm-up and not counted. Tokens of each clip's
first complete decode are kept for the correctness check.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import multiprocessing as mp
import time
import urllib.request

import numpy as np

URL = "127.0.0.1:18401"
METRICS = "http://127.0.0.1:18402/metrics"
SR = 16000


def metrics(model: str) -> dict[str, float]:
    out: dict[str, float] = {}
    for line in urllib.request.urlopen(METRICS, timeout=5).read().decode().splitlines():
        if line.startswith("#") or f'model="{model}"' not in line:
            continue
        name, val = line.rsplit(" ", 1)
        key = name.split("{")[0]
        out[key] = out.get(key, 0.0) + float(val)
    return out


async def run_streams(d: dict, ids: list[int], seconds: float, warm: float, model: str, t_zero: float) -> dict:
    import tritonclient.grpc.aio as grpcclient

    cli = grpcclient.InferenceServerClient(URL)
    clips = d["clips"]
    lat: list[float] = []
    ttf: list[float] = []
    tokens: dict[int, list[int]] = {}
    audio = 0.0
    errors = 0

    async def stream(sid: int) -> None:
        nonlocal audio, errors
        n = len(clips)
        ci = (sid * 7) % n
        seq = 10_000_000 * (sid + 1)
        while time.perf_counter() - t_zero < seconds:
            lo, hi = clips[ci]
            seq += 1
            t_clip = time.perf_counter()
            avail = t_clip + np.cumsum(d["real"][lo:hi]) / SR
            toks: list[int] = []
            ok = True
            for k in range(hi - lo):
                now = time.perf_counter()
                if avail[k] > now:
                    await asyncio.sleep(avail[k] - now)
                j = lo + k
                a = grpcclient.InferInput("audio_signal", [1, *d["feats"].shape[1:]], "FP32")
                a.set_data_from_numpy(d["feats"][j : j + 1])
                b = grpcclient.InferInput("length", [1, 1], "INT64")
                b.set_data_from_numpy(d["length"][j : j + 1].reshape(1, 1))
                c = grpcclient.InferInput("prompt", [1, 1], "INT64")
                c.set_data_from_numpy(np.array([[d["prompt"]]], np.int64))
                try:
                    r = await cli.infer(model, [a, b, c], sequence_id=seq, sequence_start=(k == 0),
                                        sequence_end=(k == hi - lo - 1),
                                        outputs=[grpcclient.InferRequestedOutput("tokens")])
                except Exception as e:  # noqa: BLE001
                    if errors == 0:
                        print("first error:", str(e)[:400], flush=True)
                    errors += 1
                    ok = False
                    break
                done = time.perf_counter()
                toks += [int(t) for t in r.as_numpy("tokens").reshape(-1) if t >= 0]
                if avail[k] - t_zero >= warm and done - t_zero < seconds:
                    lat.append(done - avail[k])
                    if k == hi - lo - 1:
                        ttf.append(done - avail[k])
                        audio += (hi - lo) and float(d["real"][lo:hi].sum()) / SR
            if ok and ci not in tokens:
                tokens[ci] = toks
            ci = (ci + 1) % n

    await asyncio.gather(*(stream(s) for s in ids))
    await cli.close()
    return {"lat": lat, "ttf": ttf, "tokens": tokens, "audio": audio, "errors": errors}


def worker(path: str, ids: list[int], seconds: float, warm: float, model: str, t_zero_wall: float, q: mp.Queue) -> None:
    d = load(path)
    # align the processes' clocks on wall time, then use the monotonic clock
    while time.time() < t_zero_wall:
        time.sleep(0.001)
    t_zero = time.perf_counter()
    q.put(asyncio.run(run_streams(d, ids, seconds, warm, model, t_zero)))


def load(path: str) -> dict:
    z = np.load(path)
    clip = z["clip"]
    bounds = np.flatnonzero(np.diff(np.r_[-1, clip, -2]))
    clips = [(int(bounds[i]), int(bounds[i + 1])) for i in range(len(bounds) - 1)]
    return {"feats": z["feats"], "length": z["length"], "real": z["real"], "prompt": int(z["prompt"]),
            "chunk_ms": int(z["chunk_ms"]), "clips": clips}


def pct(a: list[float], q: float) -> float:
    return round(float(np.percentile(np.array(a) * 1000, q)), 1) if a else float("nan")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("feats")
    ap.add_argument("--streams", default="1,8,16,32")
    ap.add_argument("--seconds", type=float, default=60)
    ap.add_argument("--warm", type=float, default=5)
    ap.add_argument("--procs", type=int, default=1)
    ap.add_argument("--model", default="nemotron_step")
    ap.add_argument("--out", default="")
    ap.add_argument("--label", default="")
    a = ap.parse_args()
    chunk_ms = load(a.feats)["chunk_ms"]
    results = []
    all_tokens: dict[int, list[int]] = {}
    for S in [int(v) for v in a.streams.split(",")]:
        m0 = metrics(a.model)
        procs = max(1, min(a.procs, S))
        q: mp.Queue = mp.Queue()
        t_zero_wall = time.time() + 3.0
        ps = [mp.Process(target=worker, args=(a.feats, list(range(p, S, procs)), a.seconds, a.warm, a.model,
                                               t_zero_wall, q)) for p in range(procs)]
        for p in ps:
            p.start()
        parts = [q.get() for _ in ps]
        for p in ps:
            p.join()
        m1 = metrics(a.model)
        lat = [x for p in parts for x in p["lat"]]
        ttf = [x for p in parts for x in p["ttf"]]
        audio = sum(p["audio"] for p in parts)
        for p in parts:
            for k, v in p["tokens"].items():
                all_tokens.setdefault(int(k), v)
        dm = {k: m1.get(k, 0.0) - m0.get(k, 0.0) for k in m1}
        execs = dm.get("nv_inference_exec_count", 0.0)
        reqs = dm.get("nv_inference_request_success", 0.0)
        comp = dm.get("nv_inference_compute_infer_duration_us", 0.0)
        queue = dm.get("nv_inference_queue_duration_us", 0.0)
        row = {
            "label": a.label, "streams": S, "chunk_ms": chunk_ms, "chunks": len(lat), "finals": len(ttf),
            "lat_p50_ms": pct(lat, 50), "lat_p95_ms": pct(lat, 95), "lat_p99_ms": pct(lat, 99),
            "ttf_p50_ms": pct(ttf, 50), "ttf_p95_ms": pct(ttf, 95),
            "rtf_per_stream": round(float(np.mean(lat)) * 1000 / chunk_ms, 3) if lat else None,
            "errors": sum(p["errors"] for p in parts),
            # compute_infer is summed per request (a batch adds its execution time once per request in it)
            "triton": {"requests": int(reqs), "executions": int(execs),
                       "avg_batch": round(reqs / execs, 2) if execs else None,
                       "exec_ms": round(comp / reqs / 1000, 2) if reqs else None,
                       "queue_ms_per_req": round(queue / reqs / 1000, 2) if reqs else None,
                       # input and output handling around the model: the per-stream state is gathered into the
                       # batch and scattered back here
                       "input_ms": round(dm.get("nv_inference_compute_input_duration_us", 0.0) / reqs / 1000, 2)
                       if reqs else None,
                       "output_ms": round(dm.get("nv_inference_compute_output_duration_us", 0.0) / reqs / 1000, 2)
                       if reqs else None,
                       "busy": round(comp / reqs / 1e6 * execs / a.seconds, 3) if reqs else None},
            "budget_ok": (pct(lat, 95) <= 100.0) if lat else None,
        }
        print(json.dumps(row), flush=True)
        results.append(row)
    if a.out:
        with open(a.out, "w") as f:
            json.dump({"levels": results, "tokens": {str(k): v for k, v in sorted(all_tokens.items())}}, f)


if __name__ == "__main__":
    main()
