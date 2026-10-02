"""A5: one line per (bench run, target): latencies, words equal to the fast decodes, WER, memory, relay time.
   python3 docs/spikes/a5/report.py ~/cadence-spikes/a5/out t160-1120-ru-paced-16k ...
"""

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from compare import norm, wer  # noqa: E402

def strict_onset(path: str) -> float:
    """Onset with 240 ms of sustained energy (10 of 12 frames): rejects the clicks and breaths the bench's 4-of-5
    rule takes for speech at the start of two FLEURS ru clips."""
    import numpy as np
    import soundfile as sf

    x, sr = sf.read(path, dtype="float32")
    n = int(0.02 * sr)
    f = x[: x.size // n * n].reshape(-1, n)
    db = 10 * np.log10(np.mean(f.astype(np.float64) ** 2, axis=1) + 1e-12)
    thr = max(np.percentile(db, 10) + 20, db.max() - 35)
    sus = np.convolve((db > thr).astype(int), np.ones(12, dtype=int), mode="valid") >= 10
    idx = np.nonzero(sus)[0]
    return float(idx[0] * 0.02) if idx.size else 0.0


def pq(xs: list[float]) -> dict:
    import numpy as np

    if not xs:
        return {}
    return {"p50": round(float(np.percentile(xs, 50)), 1), "p95": round(float(np.percentile(xs, 95)), 1), "n": len(xs)}


CLIPDIR = sys.argv.pop(2).split("=", 1)[1] if len(sys.argv) > 2 and sys.argv[2].startswith("--clips=") else None
out = Path(sys.argv[1])
for run in sys.argv[2:]:
    d = json.loads((out / f"{run}.json").read_text())
    agg, summ = d["aggregate"], d["summary"]
    for t in d["started"]["targets"]:
        tg, prof = t["target"], t["profile"]
        fast = json.loads((out / f"fast-{prof}-ab.json").read_text())
        idx = {Path(p).name: i for i, p in enumerate(fast["clips"])}
        live = [r[f"words_{tg}"] for r in d["perClip"]]
        rows = [idx[r["clip"]] for r in d["perClip"]]
        b = [fast["b"][i] for i in rows]
        a = [fast["a"][i] for i in rows]
        refs = [fast["refs"][i] for i in rows]
        g = lambda k: agg.get(f"{k}_{tg}", {})  # noqa: E731
        line = {
            "run": run,
            "target": tg,
            "profile": prof,
            "clips": len(live),
            "finalizeMs": g("finalizeMs"),
            "ttfpMs": g("ttfpMs"),
            "partialLagMs": g("partialLagMs"),
            "eouDelayMs": g("eouDelayMs"),
            "eouMissed": agg.get(f"eouMissed_{tg}"),
            "earlyEou": agg.get(f"earlyEou_{tg}"),
            "sameWordsAsLiveFast": f"{sum(norm(x) == norm(y) for x, y in zip(live, b))}/{len(live)}",
            "sameRawAsLiveFast": f"{sum(x == y for x, y in zip(live, b))}/{len(live)}",
            "sameWordsAsPipelineRun": f"{sum(norm(x) == norm(y) for x, y in zip(live, a))}/{len(live)}",
            "wer": wer(refs, live),
            "werFast": wer(refs, b),
            "stepMs": {k: summ["targets"][tg][k] for k in ("stepMsP50", "stepMsP95", "stepMsMax")},
            "rtf": summ["rtf"],
            "gpu": summ["gpu"],
            "rttMs": {"relay": agg.get("rttRelayMs"), "worker": agg.get("rttWorkerMs")},
            "relayUs": d.get("relayStats"),
        }
        if CLIPDIR:
            tt = []
            for r in d["perClip"]:
                if r.get(f"ttfpMs_{tg}") is None:
                    continue
                tag = "he" if r["clip"].startswith("he") else "ru"
                tt.append(r[f"ttfpMs_{tg}"] + (r["onsetS"] - strict_onset(f"{CLIPDIR}/{tag}/{r['clip']}")) * 1000)
            line["ttfpMsStrictVad"] = pq(tt)
        if d["args"]["mode"] == "eou":
            line.pop("sameWordsAsLiveFast")
        print(json.dumps(line, ensure_ascii=False))
