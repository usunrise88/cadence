"""E1 step 2: parity of the exported step graph (ONNX Runtime) against Cadence's NeMo pipeline decoder (R31).

  docs/spikes/e1/run.sh --gpu python /repo/docs/spikes/e1/parity.py <model.nemo> <onnx_dir> <out.json>
      [--n 200] [--batch 8] [--ort-batch 8] [--lang hr-HR] [--provider cuda|cpu]

* Sample: the first N files (sorted by name) of FLEURS sr test (/corpora/fleurs-sr/70bb2e84b976/test); references are
  the `.txt` sidecars, transliterated sr-Cyrl-Latn as dataset_import@3 does (the model writes Latin under hr-HR).
* NeMo: `cadence_nemo.pipeline.decode_batch` — exactly nemotron_transcribe@4 (fp32, greedy_batch, max_symbols 10,
  EOU at 800 ms, batch 8 by default). The token sequence of each stream is read from the hypothesis the pipeline hands
  its greedy decoder on the stream's last step (endpointing does not reset the RNN-T state, so it is the whole stream).
* ONNX: the same feature buffers (PipelineStream's Features and _chunks: a short first chunk without cache, then
  pre-encode cache + chunk, the last one right-padded), each right-padded to the profile's buffer length, through
  step.onnx with every stream's state carried in numpy; batches of --ort-batch streams step together as in the pipeline.
* Scores: identical token sequences (the locale tag included), identical transcripts, and WER of both against the
  references under one normaliser (NFKC, lowercase, punctuation removed) — R31 needs |dWER| <= 0.1 and >= 99.5 %
  identical token sequences.
"""

from __future__ import annotations

import argparse
import glob
import json
import os
import re
import time
import unicodedata
from typing import Any

import numpy as np
import soundfile as sf

CORPUS = "/corpora/fleurs-sr/70bb2e84b976/test"
GB = 1024**3


def norm(s: str) -> str:
    s = unicodedata.normalize("NFKC", s).lower()
    s = "".join(" " if unicodedata.category(ch).startswith(("P", "S")) and ch != "%" else ch for ch in s)
    return re.sub(r"\s+", " ", s).strip()


def edit_distance(r: list[str], h: list[str]) -> int:
    prev = list(range(len(h) + 1))
    for i, a in enumerate(r, 1):
        cur = [i] + [0] * len(h)
        for j, b in enumerate(h, 1):
            cur[j] = min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a != b))
        prev = cur
    return prev[-1]


def wer(refs: list[str], hyps: list[str]) -> float:
    e = w = 0
    for r, h in zip(refs, hyps, strict=True):
        rr, hh = norm(r).split(), norm(h).split()
        e += edit_distance(rr, hh)
        w += len(rr)
    return round(100.0 * e / max(w, 1), 3)


def sample(n: int) -> list[dict[str, Any]]:
    from cadence_worker.translit import transliterate

    rows = []
    for wav in sorted(glob.glob(os.path.join(CORPUS, "*.wav")))[:n]:
        x, sr = sf.read(wav, dtype="float32", always_2d=True)
        assert sr == 16000, (wav, sr)
        ref = open(wav[:-4] + ".txt", encoding="utf-8").read().strip()
        rows.append({"id": os.path.basename(wav)[:-4], "audio": x[:, 0].copy(), "ref": transliterate(ref, "sr-Cyrl-Latn")})
    return rows


def capture_tokens() -> dict[int, list[int]]:
    """Patch the pipeline's run_greedy_decoder to record each stream's hypothesis on its last step."""
    from nemo.collections.asr.inference.pipelines.cache_aware_rnnt_pipeline import CacheAwareRNNTPipeline

    got: dict[int, list[int]] = {}
    orig = CacheAwareRNNTPipeline.run_greedy_decoder

    def run_greedy_decoder(self: Any, state: Any, request: Any, hyp: Any) -> bool:  # noqa: ANN401
        if request.is_last:
            y = hyp.y_sequence
            got[int(request.stream_id)] = [int(v) for v in (y.tolist() if hasattr(y, "tolist") else y)]
        return bool(orig(self, state, request, hyp))

    CacheAwareRNNTPipeline.run_greedy_decoder = run_greedy_decoder
    return got


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("model")
    ap.add_argument("onnx_dir")
    ap.add_argument("out")
    ap.add_argument("--n", type=int, default=200)
    ap.add_argument("--batch", type=int, default=8)
    ap.add_argument("--ort-batch", type=int, default=8)
    ap.add_argument("--lang", default="hr-HR")
    ap.add_argument("--provider", default="cuda")
    ap.add_argument("--torch-gb", type=float, default=4.0)
    ap.add_argument("--ort-gb", type=float, default=3.5)
    # ORT's CUDA EP default for TF32 matmuls (runs without the flag); "0" forces strict fp32 like NeMo's "highest"
    ap.add_argument("--tf32", default="")
    a = ap.parse_args()

    import onnxruntime as ort
    import torch

    from cadence_nemo import pipeline
    from cadence_nemo.lang import strip_tags

    geo = json.load(open(os.path.join(a.onnx_dir, "streaming_cfg.json")))
    att = [int(v) for v in geo["att_context_size"]]
    if torch.cuda.is_available():
        total = torch.cuda.get_device_properties(0).total_memory
        torch.cuda.set_per_process_memory_fraction(min(1.0, a.torch_gb * GB / total))
    rows = sample(a.n)
    audio_s = sum(r["audio"].size for r in rows) / 16000

    # ---- NeMo pipeline decoder (nemotron_transcribe@4) -------------------------------------------------------------
    os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
    got = capture_tokens()
    model = pipeline.load(a.model, {"p": att}, stop_history_eou_ms=800, batch_size=a.batch,
                          device="cuda" if torch.cuda.is_available() else "cpu")
    pipe = model.pipelines["p"]
    tok = pipe.asr_model.asr_model.tokenizer
    nemo_text: list[str] = []
    nemo_tok: list[list[int]] = []
    t0 = time.perf_counter()
    for i in range(0, len(rows), a.batch):
        batch = rows[i : i + a.batch]
        streams = [pipeline.PipelineStream(target="A", pipeline=pipe, att=att, profile="p", language=a.lang)
                   for _ in batch]
        res = pipeline.decode_batch(streams, [r["audio"] for r in batch])
        for s, r in zip(streams, res, strict=True):
            nemo_text.append(r.text)
            nemo_tok.append(got.get(s.stream_id, []))
    t_nemo = time.perf_counter() - t0
    peak_torch = torch.cuda.max_memory_reserved() / GB if torch.cuda.is_available() else 0.0

    # ---- the same buffers through step.onnx --------------------------------------------------------------------------
    plans = []
    for r in rows:
        s = pipeline.PipelineStream(target="A", pipeline=pipe, att=att, profile="p", language=a.lang)
        s._open()
        assert s.feats is not None
        s.feats.push(r["audio"])
        s.feats.finish()
        plans.append([(c.features.detach().float().cpu().numpy(), int(c.length), bool(c.first)) for c in s._chunks(final=True)])
    T = int(geo["buffer_frames"])
    so = ort.SessionOptions()
    so.log_severity_level = 3
    prov: list[Any] = ["CPUExecutionProvider"]
    if a.provider == "cuda":
        prov = [("CUDAExecutionProvider", {"gpu_mem_limit": int(a.ort_gb * GB), "arena_extend_strategy":
                                           "kSameAsRequested", "cudnn_conv_algo_search": "DEFAULT",
                                           **({"use_tf32": a.tf32} if a.tf32 else {})}), "CPUExecutionProvider"]
    sess = ort.InferenceSession(os.path.join(a.onnx_dir, "step.onnx"), so, providers=prov)
    blank = int(geo["blank_id"])
    prompt = int(geo["prompt_dictionary"][a.lang])
    shapes = geo["state_shapes"]

    idt = np.float32 if geo.get("float_state") else np.int64

    def zero_state() -> dict[str, np.ndarray]:
        st = {k: np.zeros(v, np.float32) for k, v in shapes.items() if k not in ("cache_last_channel_len", "last_token")}
        st["cache_last_channel_len"] = np.zeros([1], idt)
        st["last_token"] = np.full([1], blank, idt)
        return st

    sio = geo.get("state_io") or {n: [n, f"{n}_next"] for n in shapes}
    onnx_tok: list[list[int]] = [[] for _ in rows]
    t0 = time.perf_counter()
    steps = 0
    for i in range(0, len(rows), a.ort_batch):
        idx = list(range(i, min(len(rows), i + a.ort_batch)))
        states = {j: zero_state() for j in idx}
        k = 0
        while True:
            live = [j for j in idx if k < len(plans[j])]
            if not live:
                break
            feats = np.zeros((len(live), geo["n_mels"], T), np.float32)
            for b, j in enumerate(live):
                f = plans[j][k][0]
                feats[b, :, : f.shape[1]] = f[:, :T]
            feed = {
                "audio_signal": feats,
                "length": np.array([[plans[j][k][1]] for j in live], np.int64),
                "start": np.array([[1 if plans[j][k][2] else 0] for j in live], np.int32),
                "prompt": np.full((len(live), 1), prompt, np.int64),
            }
            for n in shapes:
                feed[sio[n][0]] = np.stack([states[j][n] for j in live])
            out = sess.run(None, feed)
            names = [o.name for o in sess.get_outputs()]
            o = dict(zip(names, out, strict=True))
            for b, j in enumerate(live):
                onnx_tok[j] += [int(t) for t in o["tokens"][b] if t >= 0]
                for n in shapes:
                    states[j][n] = o[sio[n][1]][b]
            k += 1
            steps += 1
    t_onnx = time.perf_counter() - t0
    onnx_text = [strip_tags(tok.ids_to_text(t)).strip() for t in onnx_tok]
    nemo_tok_text = [strip_tags(tok.ids_to_text(t)).strip() for t in nemo_tok]

    refs = [r["ref"] for r in rows]
    same_tok = sum(x == y for x, y in zip(nemo_tok, onnx_tok, strict=True))
    same_text = sum(norm(x) == norm(y) for x, y in zip(nemo_text, onnx_text, strict=True))
    summary = {
        "profile_ms": geo["chunk_ms"], "att_context_size": att, "precision": geo.get("precision", "fp32"),
        "utterances": len(rows), "audio_hours": round(audio_s / 3600, 3), "lang": a.lang,
        "nemo_batch": a.batch, "ort_batch": a.ort_batch, "ort_provider": sess.get_providers()[0], "ort_tf32": a.tf32 or "default",
        "identical_token_sequences": same_tok, "identical_pct": round(100.0 * same_tok / len(rows), 2),
        "identical_transcripts": same_text,
        "wer_nemo": wer(refs, nemo_text), "wer_nemo_from_tokens": wer(refs, nemo_tok_text), "wer_onnx": wer(refs, onnx_text),
        "nemo_pipeline_text_equals_its_tokens": sum(norm(x) == norm(y) for x, y in zip(nemo_text, nemo_tok_text, strict=True)),
        "seconds": {"nemo": round(t_nemo, 1), "onnx": round(t_onnx, 1)},
        "rtf": {"nemo": round(t_nemo / audio_s, 4), "onnx": round(t_onnx / audio_s, 4)},
        "onnx_steps": steps, "torch_peak_reserved_gb": round(peak_torch, 2),
    }
    summary["wer_delta"] = round(summary["wer_onnx"] - summary["wer_nemo"], 3)
    summary["pass_r31"] = abs(summary["wer_delta"]) <= 0.1 and summary["identical_pct"] >= 99.5
    print(json.dumps(summary))
    diffs = [{"id": r["id"], "ref": r["ref"], "nemo": nt, "onnx": ot, "nemo_tokens": len(a_), "onnx_tokens": len(b_),
              "first_diff": next((q for q, (u, v) in enumerate(zip(a_, b_)) if u != v), min(len(a_), len(b_)))}
             for r, nt, ot, a_, b_ in zip(rows, nemo_text, onnx_text, nemo_tok, onnx_tok, strict=True) if a_ != b_]
    os.makedirs(os.path.dirname(a.out), exist_ok=True)
    with open(a.out, "w", encoding="utf-8") as f:
        json.dump({"summary": summary, "differences": diffs,
                   "rows": [{"id": r["id"], "nemo": nt, "onnx": ot} for r, nt, ot in zip(rows, nemo_text, onnx_text, strict=True)]},
                  f, ensure_ascii=False, indent=0)


if __name__ == "__main__":
    main()
