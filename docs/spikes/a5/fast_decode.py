"""A5: the three non-live decodes of the same clips at one profile, for the "same words" acceptance line.
   a) NeMo's own ``pipeline.run(files)`` (the streaming pipeline API, fast);
   b) ``LiveTarget`` fed the whole file in 80 ms pieces as fast as the card allows, then ``end`` (the live code path
      without pacing);
   c) the NeMo pack's own cache-aware loop (``cadence_nemo.streaming.decode_batch``, what ``nemotron_transcribe@1``
      and evals run), batch 1.
Writes /work/out/fast-<profile>.json with texts, load times, RTF and peak memory.
   docs/spikes/a5/run.sh --gpu python /a5/fast_decode.py 160ms
"""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path

sys.path.insert(0, "/a5")

import numpy as np
import soundfile as sf
import torch

from live_core import LiveTarget, build_pipeline, strip_tags

MODEL = os.environ["A5_MODEL"]
profile = sys.argv[1]
which = sys.argv[2] if len(sys.argv) > 2 else "abc"
clips = []
for tag in ("he", "ru"):
    for line in Path(f"/work/clips/{tag}/manifest.jsonl").read_text(encoding="utf-8").splitlines():
        r = json.loads(line)
        clips.append({"tag": tag, "path": f"/work/clips/{tag}/{r['audio']}", "ref": r["text"], "lang": r["language"]})

out: dict = {"profile": profile, "clips": [c["path"] for c in clips]}
torch.cuda.reset_peak_memory_stats()
pipe, load_s = build_pipeline(MODEL, profile, batch_size=int(os.environ.get("A5_BATCH", "8"))) if which != "c" else (None, 0.0)
out["pipelineLoadS"] = round(load_s, 2)
out["chunkS"] = pipe.chunk_size_in_secs if pipe else None
print("pipeline loaded", load_s, flush=True)

if "a" in which:
    from nemo.collections.asr.inference.streaming.framing.request_options import ASRRequestOptions

    res = {}
    for lang in ("he-IL", "ru-RU"):
        sub = [c for c in clips if c["lang"] == lang]
        t0 = time.perf_counter()
        r = pipe.run([c["path"] for c in sub], [ASRRequestOptions(language_code=lang) for _ in sub])
        dt = time.perf_counter() - t0
        for sid, v in r.items():
            res[v["audio_filepath"]] = strip_tags(v["text"])
        dur = sum(sf.info(c["path"]).duration for c in sub)
        out[f"pipelineRunRTF_{lang}"] = round(dt / dur, 4)
    out["a"] = [res[c["path"]] for c in clips]

if "b" in which:
    texts, rtf = [], []
    for c in clips:
        x, sr = sf.read(c["path"], dtype="float32")
        assert sr == 16000
        t = LiveTarget("t1", pipe, c["lang"], profile)
        ev = []
        t0 = time.perf_counter()
        for i in range(0, x.size, 1280):
            ev += t.push(x[i : i + 1280])
        ev += t.finalize("end")
        rtf.append((time.perf_counter() - t0) / (x.size / 16000))
        finals = [e for e in ev if e["type"] == "final"]
        texts.append("".join((" " if e["space"] else "") + e["text"] for e in finals if e["text"]).strip())
        out.setdefault("b_finals_per_clip", []).append(sum(1 for e in finals if e["text"]))
        out.setdefault("b_midword_joins", []).append(sum(1 for e in [f for f in finals if f["text"]][1:] if not e["space"]))
        if c is clips[0]:
            out["b_events_example"] = ev
    out["b"] = texts
    out["liveFastRTF_p50"] = round(float(np.median(rtf)), 4)

out["pipelinePeakAllocGiB"] = round(torch.cuda.max_memory_allocated() / 2**30, 3)
out["pipelinePeakReservedGiB"] = round(torch.cuda.max_memory_reserved() / 2**30, 3)

if "c" in which:
    del pipe
    torch.cuda.empty_cache()
    from cadence_nemo import streaming, training
    from cadence_nemo.family import att_context_size
    from cadence_nemo.family import profile as fprofile

    t0 = time.perf_counter()
    model = training.load_model(Path(MODEL), torch.device("cuda")).to(torch.float32)
    out["packLoadS"] = round(time.perf_counter() - t0, 2)
    texts = []
    cur = None
    for c in clips:
        if cur != c["lang"]:
            streaming.prepare(model, att_context_size(fprofile(profile)), c["lang"])
            cur = c["lang"]
        (s,) = streaming.decode_batch(model, [Path(c["path"])], 10.0)
        texts.append(strip_tags(s.text))
    out["c"] = texts

out["refs"] = [c["ref"] for c in clips]
for k in ("a", "b", "c"):
    if k in out:
        for j in ("a", "b", "c"):
            if j in out and j > k:
                same = sum(x == y for x, y in zip(out[k], out[j]))
                out[f"identical_{k}{j}"] = f"{same}/{len(clips)}"
Path("/work/out").mkdir(parents=True, exist_ok=True)
Path(f"/work/out/fast-{profile}-{which}{os.environ.get('A5_SUFFIX', '')}.json").write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
print(json.dumps({k: v for k, v in out.items() if not isinstance(v, list)}, ensure_ascii=False, indent=1))
