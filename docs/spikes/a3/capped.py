"""Run any NeMo example script under the A3 GPU memory cap (cap.py), unchanged otherwise.

  docs/spikes/a3/nemo.sh --gpu python /spike/capped.py /work/NeMo/examples/asr/<script>.py key=value ...
"""

import runpy
import sys

sys.path.insert(0, "/spike")
import cap  # noqa: E402

cap.apply()
script, sys.argv = sys.argv[1], sys.argv[1:]
try:
    runpy.run_path(script, run_name="__main__")
finally:
    print(f"[a3.cap] {cap.peak()}", flush=True)
