"""lid_classify@2 on a card (``pytest -m gpu`` inside the omni image): the NeMo pack's FLEURS Hebrew fixture classified
by SpeechBrain's VoxLingua107 ECAPA-TDNN under the 8 GB step cap. Needs ``speechbrain/lang-id-voxlingua107-ecapa`` at
the pinned revision in the Hugging Face cache (``HF_HOME`` or ``CADENCE_HF_READONLY_CACHES``) or network access for one
download (about 85 MB)."""

from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Any

import pytest

from cadence_omni.steps.lid import LidClassifyStep, LidParams
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[2] / "nemo" / "cadence_nemo" / "fixtures"
CAP_MB = 8192
LID: dict[str, Any] = {
    "versionId": "ver_lid",
    "name": "auxiliary/lid-voxlingua107",
    "version": "2026-10-03.000000000000",
    "payload": {
        "roles": ["lid"],
        "licence": "Apache-2.0 (model); VoxLingua107 CC-BY-4.0",
        "outputsCommercialUse": True,
        "languages": ["*"],
        "hfRepo": "speechbrain/lang-id-voxlingua107-ecapa",
        "revision": "0253049ae131d6a4be1c4f0d8b0ff483a0f8c8e9",
        "engine": "speechbrain-ecapa",
    },
}

pytestmark = pytest.mark.gpu


def test_lid_classify_on_the_card(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("speechbrain")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
    params = DatasetImportParams(
        format="folder-csv",
        path=str(FIXTURES),
        source_name="fixtures",
        licence="CC-BY-4.0",
        locale="he-IL",
        split_rule="all-test",
    )
    data = tmp_path / "data"
    write_dataset(params, records(params), data)
    ctx = StepContext(lambda e: None, work_dir=tmp_path, card=Card(0, CAP_MB), auxiliaries={"auxiliary": LID})
    out = tmp_path / "lid.jsonl"
    started = time.monotonic()
    LidClassifyStep().run(LidParams(), {"data": data}, {"lid": out}, ctx)
    seconds = time.monotonic() - started
    rows = [json.loads(x) for x in out.read_text(encoding="utf-8").splitlines()]
    assert len(rows) == 10
    # VoxLingua107's label iw comes out as he. On these 3 s read-speech clips the classifier is right on 8 of 10 and
    # names Slovenian on two (0.72 and 0.81; checked 2026-10-04, the same at batch 1 and 16): a read clip this short
    # is near the classifier's floor, and the ensemble's lid_min_confidence and the members' own detection cover it.
    tops = [r["top"] for r in rows]
    assert sum(r["language"] == "he" for r in rows) >= 8, tops
    assert all(r["confidence"] > 0.85 for r in rows if r["language"] == "he"), tops
    peak = torch.cuda.max_memory_allocated() / 2**20
    assert peak < CAP_MB, f"{peak:.0f} MiB allocated, over the {CAP_MB} MiB step cap"
    print(json.dumps({"lid": [r["top"][0] for r in rows], "peakMiB": round(peak), "seconds": round(seconds, 1)}))
