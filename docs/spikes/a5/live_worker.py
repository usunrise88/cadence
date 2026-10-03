"""A5 throwaway: the worker's ``live`` job. Loads one pipeline per target (model x latency profile), then dials the
relay (``/worker/live/{jobId}``, the R14 pull model) once per session and speaks the R48 protocol:

  up   start (JSON) · binary PCM16 LE mono frames at the capture rate · finalize · keepalive · end
  down started · partial · final · stats · pong · error · summary

    docs/spikes/a5/run.sh --gpu --name a5-worker python /a5/live_worker.py \
        --targets '[{"name":"A","profile":"160ms"},{"name":"B","profile":"1120ms"}]' --sessions 0
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import time
from typing import Any

sys.path.insert(0, "/a5")
import numpy as np
import torch
from aiohttp import ClientSession, WSMsgType

from live_core import SR, LiveTarget, StreamingResampler, Telephony, build_shared, pcm16_to_float


def pct(xs: list[float], p: float) -> float | None:
    return round(float(np.percentile(xs, p)), 2) if xs else None


class Job:
    def __init__(self, targets: list[dict[str, Any]], dtype: str) -> None:
        t0 = time.perf_counter()
        # one checkpoint here, so one model shared by every target (a different checkpoint would load its own)
        pipes, s = build_shared(os.environ["A5_MODEL"], [t["profile"] for t in targets], dtype)
        self.targets = [(t, pipes[t["profile"]]) for t in targets]
        self.load_total = round(time.perf_counter() - t0, 2)
        self.load = {t["name"]: round(s, 2) for t in targets}
        torch.cuda.synchronize()
        self.mem_after_load = round(torch.cuda.memory_allocated() / 2**30, 3)
        print(f"loaded {self.load} total {self.load_total}s, allocated {self.mem_after_load} GiB", flush=True)

    async def session(self, url: str, token: str) -> bool:
        async with ClientSession() as http:
            async with http.ws_connect(url, headers={"Authorization": f"Bearer {token}"}, max_msg_size=1 << 20) as ws:
                print("dialled relay, waiting for a session", flush=True)
                return await self.serve(ws)

    async def serve(self, ws: Any) -> bool:
        torch.cuda.reset_peak_memory_stats()
        live: list[LiveTarget] = []
        res: StreamingResampler | None = None
        tel: Telephony | None = None
        audio_in = 0  # 16 kHz samples decoded
        compute_s = 0.0
        frame_ms: list[float] = []
        last_stats = time.monotonic()

        async def send(evs: list[dict[str, Any]]) -> None:
            for e in evs:
                await ws.send_str(json.dumps(e, ensure_ascii=False))

        async def feed(x: np.ndarray) -> None:
            nonlocal compute_s
            t0 = time.perf_counter()
            evs: list[dict[str, Any]] = []
            for t in live:
                evs += t.push(x)
            dt = time.perf_counter() - t0
            compute_s += dt
            frame_ms.append(dt * 1000)
            await send(evs)

        async for msg in ws:
            if msg.type == WSMsgType.BINARY:
                if not live:
                    await ws.send_str(json.dumps({"type": "error", "title": "audio before start"}))
                    continue
                x = pcm16_to_float(msg.data)
                assert res is not None
                y = res.push(x)
                if tel is not None:
                    y = tel.push(y)
                audio_in += y.size
                await feed(y)
                if time.monotonic() - last_stats > 1.0:
                    last_stats = time.monotonic()
                    await ws.send_str(
                        json.dumps(
                            {
                                "type": "stats",
                                "source": "worker",
                                "rtf": round(compute_s / max(audio_in / SR, 1e-9), 4),
                                "audioS": round(audio_in / SR, 2),
                            }
                        )
                    )
                continue
            if msg.type != WSMsgType.TEXT:
                break
            m = json.loads(msg.data)
            kind = m.get("type")
            if kind == "start":
                inp = m.get("input") or {}
                rate = int(inp.get("sampleRate") or SR)
                res = StreamingResampler(rate, SR)
                tel = Telephony() if m.get("telephony") else None
                lang = m.get("language") or "he-IL"
                live = [
                    LiveTarget(t["name"], pipe, t.get("language") or lang, t["profile"]) for t, pipe in self.targets
                ]
                print("start", json.dumps(m)[:300], flush=True)
                await ws.send_str(
                    json.dumps(
                        {
                            "type": "started",
                            "targets": [
                                {
                                    "target": t.name,
                                    "profile": t.profile,
                                    "chunkMs": round(t.chunk / SR * 1000),
                                    "language": t.lang,
                                    "loadS": self.load[t.name],
                                }
                                for t in live
                            ],
                            "captureRate": rate,
                            "resampler": "scipy.signal.resample_poly (streaming)",
                            "telephony": "16k->8k, G.711 mu-law, ->16k" if tel else None,
                            "input": inp,
                        }
                    )
                )
            elif kind == "keepalive":
                await ws.send_str(json.dumps({"type": "pong", "source": "worker", "t": m.get("t")}))
            elif kind in ("finalize", "end"):
                t0 = time.perf_counter()
                evs: list[dict[str, Any]] = []
                if kind == "end" and res is not None:
                    tail = res.flush()
                    if tel is not None:
                        tail = tel.push(tail)
                        tail = np.concatenate([tail, tel.flush()])
                    for t in live:
                        evs += t.push(tail)
                for t in live:
                    evs += t.finalize(kind)
                compute_s += time.perf_counter() - t0
                await send(evs)
                if kind == "end":
                    summary = {
                        "type": "summary",
                        "audioS": round(audio_in / SR, 2),
                        "rtf": round(compute_s / max(audio_in / SR, 1e-9), 4),
                        "frameMsP50": pct(frame_ms, 50),
                        "frameMsP95": pct(frame_ms, 95),
                        "frames": len(frame_ms),
                        "targets": {
                            t.name: {
                                "profile": t.profile,
                                "steps": len(t.step_ms),
                                "stepMsP50": pct(t.step_ms, 50),
                                "stepMsP95": pct(t.step_ms, 95),
                                "stepMsMax": pct(t.step_ms, 100),
                            }
                            for t in live
                        },
                        "gpu": {
                            "maxAllocatedGiB": round(torch.cuda.max_memory_allocated() / 2**30, 3),
                            "maxReservedGiB": round(torch.cuda.max_memory_reserved() / 2**30, 3),
                            "allocatedAfterLoadGiB": self.mem_after_load,
                        },
                        "load": {"perTargetS": self.load, "totalS": self.load_total},
                    }
                    await ws.send_str(json.dumps(summary))
                    print("summary", json.dumps(summary), flush=True)
                    await ws.close()
                    return True
        return False


async def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--relay", default="ws://127.0.0.1:18480")
    ap.add_argument("--job", default="job_a5")
    ap.add_argument("--token", default="cwk_a5spike")
    ap.add_argument("--targets", default='[{"name":"A","profile":"160ms"}]')
    ap.add_argument("--dtype", default="float32")
    ap.add_argument("--sessions", type=int, default=0, help="0 = serve forever")
    a = ap.parse_args()
    job = Job(json.loads(a.targets), a.dtype)
    n = 0
    while a.sessions == 0 or n < a.sessions:
        try:
            await job.session(f"{a.relay}/worker/live/{a.job}", a.token)
            n += 1
        except Exception as e:  # noqa: BLE001 - a spike: reconnect on anything
            print("session error:", repr(e), flush=True)
            await asyncio.sleep(1)


asyncio.run(main())
