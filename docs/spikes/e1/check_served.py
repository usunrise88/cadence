"""E1 step 3e: are the served transcripts the parity run's? Detokenises the token sequences a benchmark client kept
(first complete decode of each clip) and compares them with a parity run's NeMo and ONNX Runtime transcripts; also
compares parity runs with each other (the batch noise floor of NeMo itself).

  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/check_served.py <model.nemo> <parity.json> [triton.json ...]
  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/check_served.py --cross <parity_a.json> <parity_b.json>
"""

from __future__ import annotations

import json
import os
import sys

from parity import norm, wer


def cross(pa: str, pb: str) -> None:
    a, b = json.load(open(pa)), json.load(open(pb))
    ra, rb = a["rows"], b["rows"]
    out = {}
    for x in ("nemo", "onnx"):
        for y in ("nemo", "onnx"):
            out[f"{x}@A vs {y}@B"] = sum(norm(p[x]) == norm(q[y]) for p, q in zip(ra, rb, strict=True))
    print(json.dumps({"A": os.path.basename(pa), "B": os.path.basename(pb), "utterances": len(ra), "identical": out}))


def main() -> None:
    if sys.argv[1] == "--cross":
        cross(sys.argv[2], sys.argv[3])
        return
    from nemo.collections.asr.models import ASRModel

    from cadence_nemo.lang import strip_tags
    from parity import sample

    os.environ["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
    tok = ASRModel.restore_from(sys.argv[1], map_location="cpu").tokenizer
    par = json.load(open(sys.argv[2]))
    rows = par["rows"]
    refs = {r["id"]: r["ref"] for r in sample(len(rows))}
    for path in sys.argv[3:]:
        t = json.load(open(path))["tokens"]
        idx = sorted(int(k) for k in t)
        served = {i: strip_tags(tok.ids_to_text(t[str(i)])).strip() for i in idx}
        same_onnx = sum(norm(served[i]) == norm(rows[i]["onnx"]) for i in idx)
        same_nemo = sum(norm(served[i]) == norm(rows[i]["nemo"]) for i in idx)
        r = [refs[rows[i]["id"]] for i in idx]
        print(json.dumps({
            "served": os.path.basename(path), "clips": len(idx), "identical_to_ort": same_onnx,
            "identical_to_nemo": same_nemo, "wer_served": wer(r, [served[i] for i in idx]),
            "wer_nemo_same_clips": wer(r, [rows[i]["nemo"] for i in idx]),
            "wer_ort_same_clips": wer(r, [rows[i]["onnx"] for i in idx]),
        }))


if __name__ == "__main__":
    main()
