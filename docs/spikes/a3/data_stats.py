"""Duration and tokens-per-second distribution of a NeMo manifest under the base model's tokenizer.

  docs/spikes/a3/nemo.sh python /spike/data_stats.py /work/data/manifests/train.json
OOMptimizer's `--ratio` (output tokens per input second) sizes the RNN-T joint (B x T x U x V), which dominates
memory for this model, so a step kind should measure it on the actual data instead of using the default 12.
"""

import json
import sys

import numpy as np
import sentencepiece as spm

TOK = "/work/model/x/427ad33c6285472cb01c3eb843d2309d_tokenizer.model"  # extracted from the .nemo

sp = spm.SentencePieceProcessor(model_file=TOK)
durs, rates = [], []
for line in open(sys.argv[1], encoding="utf-8"):
    r = json.loads(line)
    n = len(sp.encode(r["text"] + " <he-IL>"))
    durs.append(r["duration"])
    rates.append(n / r["duration"])
d, t = np.array(durs), np.array(rates)
print(f"utts={len(d)} hours={d.sum() / 3600:.2f}")
print("duration p50/p90/p99/max:", np.percentile(d, [50, 90, 99, 100]).round(2).tolist())
print("tokens/s p50/p90/p99/max:", np.percentile(t, [50, 90, 99, 100]).round(2).tolist())
for hi in (4, 6, 8, 10, 12, 14, 16, 18, 20):
    print(f"  <= {hi:>2}s: {(d <= hi).mean() * 100:5.1f}%")
