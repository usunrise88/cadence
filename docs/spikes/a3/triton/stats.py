"""Per-model Triton statistics (avg batch, queue / compute times per execution) - where the chunk latency goes.

  python3 docs/spikes/a3/triton/stats.py [http://127.0.0.1:18300]
"""

import json
import sys
import urllib.request

base = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18300"
for name in ("streaming_asr", "encoder_prompt", "decoder_joint"):
    d = json.load(urllib.request.urlopen(f"{base}/v2/models/{name}/stats"))["model_stats"][0]
    s, n, e = d["inference_stats"], d["inference_count"], d["execution_count"]

    def ms(k: str) -> float:
        return round(s[k]["ns"] / max(1, s[k]["count"]) / 1e6, 2)

    print(f"{name}: requests={n} executions={e} avg_batch={n / max(1, e):.2f} ms per request: queue={ms('queue')} "
          f"input={ms('compute_input')} infer={ms('compute_infer')} output={ms('compute_output')}")
