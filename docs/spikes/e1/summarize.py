"""E1: one table of every parity run — identical share, WER of each path, the delta, and the disagreement between the
two hypotheses as a word error rate (ONNX scored against NeMo's output, the measure a parity tolerance can bound).

  docs/spikes/e1/run.sh python /repo/docs/spikes/e1/summarize.py /work/results/parity-*.json
"""

from __future__ import annotations

import json
import os
import sys

from parity import wer


def main() -> None:
    for p in sys.argv[1:]:
        d = json.load(open(p))
        s, rows = d["summary"], d["rows"]
        disagree = wer([r["nemo"] for r in rows], [r["onnx"] for r in rows])
        print(json.dumps({
            "run": os.path.basename(p)[:-5], "profile_ms": s["profile_ms"], "precision": s.get("precision"),
            "batch": f'{s["nemo_batch"]}/{s["ort_batch"]}', "tf32": s.get("ort_tf32", "default"),
            "identical": f'{s["identical_token_sequences"]}/{s["utterances"]}', "pct": s["identical_pct"],
            "wer_nemo": s["wer_nemo"], "wer_onnx": s["wer_onnx"], "delta": s["wer_delta"],
            "onnx_vs_nemo_wer": disagree, "rtf_nemo": s["rtf"]["nemo"], "rtf_ort": s["rtf"]["onnx"],
        }))


if __name__ == "__main__":
    main()
