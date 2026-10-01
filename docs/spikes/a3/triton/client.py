"""A3 step 5 client: S concurrent real-time streams of 80 ms chunks against `streaming_asr`; p50/p95/p99 latency.

  DOCKER_EXTRA="--network host" docs/spikes/a3/nemo.sh python /spike/triton/client.py <model.nemo> [N_UTTS] [S,S,...]

Features come from NeMo's CacheAwareStreamingAudioBuffer (pad_and_drop_preencoded=True), computed up front so the
latency is Triton only: time from sending chunk k to receiving its TOKENS. Each stream sends chunk k at
t0 + k * 80 ms (real-time pacing; a late response delays the next send, which is counted). Transcripts are
detokenised here and scored with wer.py as a correctness check against the NeMo/ONNX paths.
"""

from __future__ import annotations

import json
import re
import sys
import threading
import time

sys.path.insert(0, "/spike")
sys.path.insert(1, "/work/pylib")

import numpy as np  # noqa: E402
import tritonclient.grpc as grpcclient  # noqa: E402

import wer  # noqa: E402
from nemo.collections.asr.models import ASRModel  # noqa: E402
from nemo.collections.asr.parts.utils.streaming_utils import CacheAwareStreamingAudioBuffer  # noqa: E402

URL = "127.0.0.1:18301"
FRAMES = 17  # 8 new mel frames (80 ms) + 9 pre-encode cache frames, from streaming_cfg.json
CHUNK_S = 0.08
TAG = re.compile(r"\s*<[a-z]{2}-[A-Z]{2}>")


def prepare(model_path: str, n: int) -> tuple[list, list, object]:
    model = ASRModel.restore_from(model_path, map_location="cpu").eval()
    model.encoder.set_default_att_context_size([56, 0])
    model.encoder.setup_streaming_params()
    buf = CacheAwareStreamingAudioBuffer(model=model, online_normalization=False, pad_and_drop_preencoded=True)
    rows = [json.loads(x) for x in open("/work/data/manifests/test_fleurs.json", encoding="utf-8")][:n]
    utts = []
    for r in rows:
        buf.reset_buffer()
        buf.append_audio_file(r["audio_filepath"], stream_id=-1)
        chunks = []
        for chunk, lens in buf:
            f = chunk[0].numpy().astype(np.float32)
            assert f.shape[1] <= FRAMES, f.shape
            pad = np.zeros((f.shape[0], FRAMES), np.float32)
            pad[:, : f.shape[1]] = f
            chunks.append((pad, int(lens[0])))
        utts.append(chunks)
    return rows, utts, model.tokenizer


def run(level: int, utts: list, rows: list, tok) -> dict:  # type: ignore[no-untyped-def]
    lat: list[float] = []
    texts: dict[int, str] = {}
    lock = threading.Lock()

    def stream(sid: int, idx: int) -> None:
        cli = grpcclient.InferenceServerClient(URL)
        chunks, toks, mine = utts[idx], [], []
        t0 = time.perf_counter()
        for k, (feat, ln) in enumerate(chunks):
            wait = t0 + k * CHUNK_S - time.perf_counter()
            if wait > 0:
                time.sleep(wait)
            a = grpcclient.InferInput("FEATS", [1, 128, FRAMES], "FP32")
            a.set_data_from_numpy(feat[None])
            b = grpcclient.InferInput("LEN", [1, 1], "INT64")
            b.set_data_from_numpy(np.array([[ln]], np.int64))
            s = time.perf_counter()
            res = cli.infer("streaming_asr", [a, b], sequence_id=sid, sequence_start=(k == 0),
                            sequence_end=(k == len(chunks) - 1))
            mine.append(time.perf_counter() - s)
            toks += [t for t in res.as_numpy("TOKENS").reshape(-1).tolist() if t >= 0]
        with lock:
            lat.extend(mine)
            texts[idx] = TAG.sub("", tok.ids_to_text(toks)).strip()

    jobs = list(range(len(utts)))
    t_start = time.perf_counter()
    nxt, sid = 0, 1000 * level
    active: list[threading.Thread] = []
    while nxt < len(jobs) or active:
        active = [t for t in active if t.is_alive()]
        while len(active) < level and nxt < len(jobs):
            sid += 1
            th = threading.Thread(target=stream, args=(sid, jobs[nxt]))
            th.start()
            active.append(th)
            nxt += 1
        time.sleep(0.005)
    wall = time.perf_counter() - t_start
    a = np.array(lat) * 1000
    audio_s = sum(len(u) for u in utts) * CHUNK_S
    sc = wer.score([{"text": rows[i]["text"], "pred_text": texts[i]} for i in sorted(texts)])
    with open(f"/work/triton/texts_s{level}.json", "w", encoding="utf-8") as f:  # for the parity cross-check
        json.dump({i: texts[i] for i in sorted(texts)}, f, ensure_ascii=False, indent=0)
    return {"streams": level, "chunks": len(a), "p50_ms": round(float(np.percentile(a, 50)), 1),
            "p95_ms": round(float(np.percentile(a, 95)), 1), "p99_ms": round(float(np.percentile(a, 99)), 1),
            "max_ms": round(float(a.max()), 1), "rtf_total": round(wall / audio_s, 3), "wer": sc["wer"],
            "utts": sc["utts"]}


def main() -> None:
    n = int(sys.argv[2]) if len(sys.argv) > 2 else 64
    levels = [int(x) for x in (sys.argv[3] if len(sys.argv) > 3 else "1,8,32,64").split(",")]
    rows, utts, tok = prepare(sys.argv[1], n)
    for lv in levels:
        print(json.dumps(run(lv, utts, rows, tok)), flush=True)


if __name__ == "__main__":
    main()
