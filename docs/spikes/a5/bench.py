"""A5 throwaway: the client side of a live session, measuring what a person would feel.

Opens a session on the relay (POST /api/transcriptions, then the stream socket with the ticket and an Origin), sends
``start`` and the clips as 80 ms PCM16 frames, and records every event with a wall-clock stamp.

Modes (``--mode``):
  paced  real-time pace; after each clip ``finalize``; measures time to first partial after speech onset, partial lag
         (receive time minus the wall time at which the partial's audio end was sent), finalize -> last final.
  eou    real-time pace; each clip followed by ``--tail`` seconds of silence and no finalize: the automatic endpoint
         delay = final (endpoint "eou") receive time minus the wall time of the utterance end (energy VAD), then a
         finalize to close the segment.
  fast   no pacing (the "as fast as the card allows" file mode); words only.
``--rate 48000`` upsamples the 16 kHz clips client-side (resample_poly) to emulate a 48 kHz microphone, so the worker's
resampler is on the path. ``--telephony`` asks the worker for the 8 kHz G.711 simulation.
Writes /work/out/<--out>.json (events, per-clip metrics, summary).
"""

from __future__ import annotations

import argparse
import asyncio
import json
import time
from pathlib import Path
from typing import Any

import numpy as np
import soundfile as sf
from aiohttp import ClientSession, WSMsgType
from scipy import signal



def energy_vad(x: np.ndarray, sr: int) -> tuple[float, float]:
    """Speech onset and end (s) by frame energy (a stand-in for Silero VAD, which the runtime image lacks): 20 ms
    frames above max(noise floor p10 + 20 dB, peak - 35 dB), sustained for 4 of 5 frames (rejects clicks)."""
    n = int(0.02 * sr)
    f = x[: x.size // n * n].reshape(-1, n)
    db = 10 * np.log10(np.mean(f.astype(np.float64) ** 2, axis=1) + 1e-12)
    thr = max(np.percentile(db, 10) + 20, db.max() - 35)
    hot = (db > thr).astype(int)
    sus = np.convolve(hot, np.ones(5, dtype=int), mode="valid") >= 4  # window k covers frames k..k+4
    idx = np.nonzero(sus)[0]
    if idx.size == 0:
        return 0.0, x.size / sr
    return idx[0] * 0.02, (idx[-1] + 5) * 0.02


def load_clips(tags: list[str], limit: int) -> list[dict[str, Any]]:
    out = []
    for tag in tags:
        rows = Path(f"/work/clips/{tag}/manifest.jsonl").read_text(encoding="utf-8").splitlines()[:limit]
        for line in rows:
            r = json.loads(line)
            x, sr = sf.read(f"/work/clips/{tag}/{r['audio']}", dtype="float32")
            on, end = energy_vad(x, sr)
            out.append({"tag": tag, "audio": r["audio"], "lang": r["language"], "x": x, "sr": sr, "on": on, "end": end})
    return out


async def run(a: argparse.Namespace) -> dict[str, Any]:
    clips = load_clips(a.tags.split(","), a.limit)
    for c in clips:  # the "microphone rate" copy is made before the session: resampling inside it blocks the reader
        if a.rate != c["sr"]:
            g = np.gcd(a.rate, c["sr"])
            c["x"] = signal.resample_poly(c["x"], a.rate // g, c["sr"] // g).astype(np.float32)
    events: list[dict[str, Any]] = []
    got = asyncio.Event()
    async with ClientSession() as http:
        async with http.post(f"{a.relay}/api/transcriptions", json={"targets": "configured"}) as r:
            sess = await r.json()
        ws_url = a.relay.replace("http", "ws", 1) + sess["streamUrl"]
        async with http.ws_connect(ws_url, origin=a.origin, max_msg_size=1 << 20) as ws:

            async def reader() -> None:
                async for m in ws:
                    if m.type == WSMsgType.TEXT:
                        e = json.loads(m.data)
                        e["_t"] = time.monotonic()
                        events.append(e)
                        got.set()
                    elif m.type in (WSMsgType.CLOSE, WSMsgType.CLOSED, WSMsgType.ERROR):
                        break

            rt = asyncio.create_task(reader())

            async def wait_for(pred: Any, timeout: float = 30.0) -> dict[str, Any]:
                deadline = time.monotonic() + timeout
                seen = 0
                while True:
                    for e in events[seen:]:
                        if pred(e):
                            return e
                    seen = len(events)
                    got.clear()
                    left = deadline - time.monotonic()
                    if left <= 0:
                        raise TimeoutError("no matching event")
                    try:
                        await asyncio.wait_for(got.wait(), left)
                    except TimeoutError:
                        pass

            async def ping_loop() -> None:
                while True:
                    await asyncio.sleep(1.0)
                    t = time.monotonic()
                    await ws.send_str(json.dumps({"type": "ping", "t": t}))
                    await ws.send_str(json.dumps({"type": "keepalive", "t": t}))

            lang = clips[0]["lang"]
            await ws.send_str(
                json.dumps(
                    {
                        "type": "start",
                        "input": {"kind": "file" if a.mode == "fast" else "microphone", "sampleRate": a.rate},
                        "telephony": a.telephony,
                        "pace": "fast" if a.mode == "fast" else "realtime",
                        "language": lang,
                    }
                )
            )
            started = await wait_for(lambda e: e["type"] == "started")
            targets = [t["target"] for t in started["targets"]]
            pt = asyncio.create_task(ping_loop())
            per_clip = []
            t_audio = 0.0  # session audio clock (s) at the start of the current clip
            t0 = time.monotonic()  # wall time at which session audio time 0 was due
            for c in clips:
                if c["lang"] != lang:
                    # one language per session in this spike; a new language would be a new session (R48)
                    continue
                x = c["x"]
                if a.mode == "eou":
                    x = np.concatenate([x, np.zeros(int(a.tail * a.rate), dtype=np.float32)])
                # the exact inverse of soundfile's int16 -> float32 (x / 32768): a 16 kHz clip arrives bit-identical
                pcm = np.clip(np.round(x * 32768), -32768, 32767).astype("<i2").tobytes()
                step = int(a.frame_ms / 1000 * a.rate) * 2
                n_ev = len(events)
                clip_wall0 = t0 + t_audio if a.mode != "fast" else time.monotonic()
                for k, i in enumerate(range(0, len(pcm), step)):
                    if a.mode != "fast":
                        due = clip_wall0 + (k + 1) * a.frame_ms / 1000  # a frame can be sent once its audio was captured
                        d = due - time.monotonic()
                        if d > 0:
                            await asyncio.sleep(d)
                    await ws.send_bytes(pcm[i : i + step])
                dur = len(pcm) / 2 / a.rate
                rec: dict[str, Any] = {"clip": c["audio"], "durS": round(dur, 3), "onsetS": c["on"], "endS": c["end"]}
                if a.mode == "eou":
                    for tg in targets:
                        try:
                            # the endpoint that closes the utterance (an early one mid-utterance is counted apart)
                            e = await wait_for(
                                lambda e, tg=tg: e["type"] == "final" and e["target"] == tg and e["endpoint"] == "eou"
                                and e["audioEnd"] >= t_audio + c["end"] and events.index(e) >= n_ev,
                                timeout=a.tail + 5,
                            )
                            rec[f"eouDelayMs_{tg}"] = round((e["_t"] - (clip_wall0 + c["end"])) * 1000, 1)
                        except TimeoutError:
                            rec[f"eouDelayMs_{tg}"] = None
                t_fin = time.monotonic()
                await ws.send_str(json.dumps({"type": "finalize"}))
                for tg in targets:
                    e = await wait_for(
                        lambda e, tg=tg: e["type"] == "final" and e["target"] == tg and e["endpoint"] == "finalize"
                        and events.index(e) >= n_ev
                    )
                    rec[f"finalizeMs_{tg}"] = round((e["_t"] - t_fin) * 1000, 1)
                    seg = [e for e in events[n_ev:] if e.get("target") == tg]
                    fins = [e for e in seg if e["type"] == "final" and e["text"]]
                    # a final with space=false continues the previous one's last word (NeMo's mid-word endpoint)
                    rec[f"words_{tg}"] = "".join((" " if e.get("space", True) else "") + e["text"] for e in fins).strip()
                    rec[f"finals_{tg}"] = len(fins)
                    rec[f"earlyEou_{tg}"] = sum(
                        1 for e in fins if e["endpoint"] == "eou" and e["audioEnd"] < t_audio + c["end"] - 0.3
                    )
                    parts = [e for e in seg if e["type"] == "partial"]
                    if parts and a.mode != "fast":
                        rec[f"ttfpMs_{tg}"] = round((parts[0]["_t"] - (clip_wall0 + c["on"])) * 1000, 1)
                        rec[f"firstPartialAudioS_{tg}"] = round(parts[0]["audioEnd"] - t_audio, 3)
                        lags = [(p["_t"] - (t0 + p["audioEnd"])) * 1000 for p in parts]
                        rec[f"partialLagMsP50_{tg}"] = round(float(np.median(lags)), 1)
                        rec[f"partialLagMs_{tg}"] = [round(v, 1) for v in lags]
                per_clip.append(rec)
                if a.mode == "fast":
                    t_audio += dur
                    t0 = time.monotonic() - t_audio
                else:
                    t_audio += dur
                    # keep the wall clock honest: the next clip's audio starts now (finalize waits are not audio)
                    t0 = time.monotonic() - t_audio
                print(json.dumps({k: v for k, v in rec.items() if not isinstance(v, list)}, ensure_ascii=False), flush=True)
            pt.cancel()
            await ws.send_str(json.dumps({"type": "end"}))
            summary = await wait_for(lambda e: e["type"] == "summary", 60)
            relay_stats = [e for e in events if e["type"] == "stats" and e.get("source") == "relay"]
            await asyncio.sleep(0.2)
            rt.cancel()
    pong = {
        src: [
            (e["_t"] - e["t"]) * 1000 for e in events if e["type"] == "pong" and e.get("source") == src and e.get("t")
        ]
        for src in ("relay", "worker")
    }

    def pq(xs: list[float]) -> dict[str, Any]:
        xs = [v for v in xs if v is not None]
        if not xs:
            return {}
        return {"p50": round(float(np.percentile(xs, 50)), 1), "p95": round(float(np.percentile(xs, 95)), 1), "n": len(xs)}

    agg: dict[str, Any] = {}
    for tg in targets:
        for key in ("finalizeMs", "ttfpMs", "eouDelayMs", "partialLagMs"):
            vals: list[float] = []
            for r in per_clip:
                v = r.get(f"{key}_{tg}")
                if isinstance(v, list):
                    vals += v
                elif v is not None:
                    vals.append(v)
            if vals:
                agg[f"{key}_{tg}"] = pq(vals)
        agg[f"earlyEou_{tg}"] = sum(r.get(f"earlyEou_{tg}", 0) for r in per_clip)
        agg[f"eouMissed_{tg}"] = sum(1 for r in per_clip if f"eouDelayMs_{tg}" in r and r[f"eouDelayMs_{tg}"] is None)
    agg["rttRelayMs"] = pq(pong["relay"])
    agg["rttWorkerMs"] = pq(pong["worker"])
    return {
        "args": vars(a),
        "started": started,
        "perClip": per_clip,
        "aggregate": agg,
        "summary": summary,
        "relayStats": relay_stats[-1] if relay_stats else None,
        "events": [{k: v for k, v in e.items()} for e in events],
    }


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--relay", default="http://127.0.0.1:18480")
    ap.add_argument("--origin", default="http://127.0.0.1:18480")
    ap.add_argument("--mode", choices=["paced", "eou", "fast"], default="paced")
    ap.add_argument("--tags", default="ru")
    ap.add_argument("--limit", type=int, default=12)
    ap.add_argument("--rate", type=int, default=16000)
    ap.add_argument("--tail", type=float, default=2.0)
    ap.add_argument("--telephony", action="store_true")
    ap.add_argument("--frame-ms", type=float, default=80.0, help="client frame length (R48: 80 ms)")
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    res = asyncio.run(run(a))
    Path("/work/out").mkdir(exist_ok=True)
    Path(f"/work/out/{a.out}.json").write_text(json.dumps(res, ensure_ascii=False, indent=1), encoding="utf-8")
    print(json.dumps({"aggregate": res["aggregate"], "summary": res["summary"], "relay": res["relayStats"]}, indent=1))


main()
