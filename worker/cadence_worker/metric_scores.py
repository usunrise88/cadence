"""The ``metric_scores`` artifact (phase 3 stream R): what a metric scorer beside ``wer_score`` writes — entity accuracy
(``entity_score``) and latency to final (``latency_score``). A directory artifact:

    summary.json      {schema: cadence.metric-scores/1, scorer: <kind@version>, metric: entities | latency, available,
                      reason?, …the metric's numbers}
    utterances.jsonl  one row per utterance the metric measured, in dataset order, with ``index`` (its position in the
                      dataset) and ``audio`` (its hash)

The control plane keeps the summary beside the eval record of the same cell (eval_metrics; evals.get ``metrics``);
the scorer's ``kind@version`` and the configuration it read (the language pack's ITN file, the VAD step) key it.
"""

from __future__ import annotations

import json
import math
from collections.abc import Iterable, Mapping, Sequence
from pathlib import Path
from typing import Any

SCHEMA = "cadence.metric-scores/1"


def write(out: Path, summary: Mapping[str, Any], rows: Iterable[Mapping[str, Any]]) -> None:
    out.mkdir(parents=True, exist_ok=True)
    with (out / "utterances.jsonl").open("w", encoding="utf-8") as f:
        for row in rows:
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
    (out / "summary.json").write_text(
        json.dumps(dict(summary), ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def percentile(values: Sequence[float], q: float) -> float | None:
    """The q-th percentile (0-100) by linear interpolation between closest ranks (type 7, NumPy's default)."""
    if not values:
        return None
    xs = sorted(values)
    pos = (len(xs) - 1) * q / 100
    lo = math.floor(pos)
    hi = min(lo + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (pos - lo)
