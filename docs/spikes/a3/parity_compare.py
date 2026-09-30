"""Parity through the served path: Triton (ONNX Runtime, CUDA EP) transcripts vs NeMo's own streaming decode.

  docs/spikes/a3/nemo.sh python /spike/parity_compare.py <nemo streaming_out.json> <triton texts.json>

The NeMo side must be decoded with pad_and_drop_preencoded=true (the exported graph bakes drop_extra_pre_encoded),
batch_size=1, same latency profile and the same manifest order. Reports WER of both (wer.py normalisation), the
delta, and the share of identical transcripts (R31 also asks >= 99.5 % identical token sequences).
"""

import json
import sys

sys.path.insert(0, "/spike")
import wer  # noqa: E402

nemo = [json.loads(x) for x in open(sys.argv[1], encoding="utf-8")]
tri = {int(k): v for k, v in json.load(open(sys.argv[2], encoding="utf-8")).items()}
n = min(len(nemo), len(tri))
rows_n = [{"text": nemo[i]["text"], "pred_text": nemo[i]["pred_text"]} for i in range(n)]
rows_t = [{"text": nemo[i]["text"], "pred_text": tri[i]} for i in range(n)]
same = sum(nemo[i]["pred_text"].strip() == tri[i].strip() for i in range(n))
a, b = wer.score(rows_n), wer.score(rows_t)
print(json.dumps({"utts": n, "identical": same, "identical_pct": round(100 * same / n, 2), "nemo": a, "onnx_triton": b,
                  "wer_delta_points": round(b["wer"] - a["wer"], 2)}, ensure_ascii=False))
for i in range(n):
    if nemo[i]["pred_text"].strip() != tri[i].strip():
        print("DIFF", i, "\n  nemo:", nemo[i]["pred_text"], "\n  onnx:", tri[i])
