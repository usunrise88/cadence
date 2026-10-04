"""whisper_transcribe on a card (``pytest -m gpu`` inside the nemo-speech image): the pack's FLEURS Hebrew fixture
decoded by Whisper large-v3 in fp16 under the 8 GB step cap, with Whisper's own language detection beside the text
(lid_classify@2 is the omni pack's, packs/omni/tests/test_lid_gpu.py). Needs
``openai/whisper-large-v3`` at the pinned revision in the Hugging Face cache (``HF_HOME`` or
``CADENCE_HF_READONLY_CACHES``) or network access for one download (about 3 GB)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from cadence_nemo.steps.whisper_member import WhisperParams, WhisperTranscribeStep
from cadence_worker import scoring
from cadence_worker.normalize import Normalizer
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset
from cadence_worker.steps.wer_score import read_references

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"
CAP_MB = 8192
BASIC = {
    "locale": "*",
    "unicode": "NFKC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": True,
    "mappings": [],
    "numbers": "keep",
}
WHISPER = {
    "versionId": "ver_whisper",
    "name": "auxiliary/whisper-large-v3",
    "version": "2026-10-03.000000000000",
    "payload": {
        "roles": ["pseudolabel", "lid"],
        "licence": "Apache-2.0",
        "outputsCommercialUse": True,
        "languages": ["*"],
        "hfRepo": "openai/whisper-large-v3",
        "revision": "06f233fe06e710322aca913c1bc4249a0d71fce1",
        "engine": "transformers-whisper",
    },
}

pytestmark = pytest.mark.gpu


def _rows(path: Path) -> list[dict[str, Any]]:
    return [json.loads(x) for x in path.read_text(encoding="utf-8").splitlines()]


def test_whisper_member_on_the_card(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("transformers")
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
    ctx = StepContext(lambda e: None, work_dir=tmp_path, card=Card(0, CAP_MB), auxiliaries={"auxiliary": WHISPER})

    hyp = tmp_path / "hyp.jsonl"
    WhisperTranscribeStep().run(WhisperParams(), {"data": data}, {"hypotheses": hyp}, ctx)
    rows = _rows(hyp)
    assert len(rows) == 10
    assert {r["language"] for r in rows} == {"he"}
    assert {r["detectedLanguage"] for r in rows} == {"he"}, rows
    norm = Normalizer.from_json(json.dumps(BASIC))
    refs = {r.audio: r.text for r in read_references(data)}
    wer = scoring.wer((norm(refs[r["audio"]]), norm(r["text"])) for r in rows)
    assert wer < 0.5, (wer, [r["text"] for r in rows])

    peak = torch.cuda.max_memory_allocated() / 2**20
    assert peak < CAP_MB, f"{peak:.0f} MiB allocated, over the {CAP_MB} MiB step cap"
    print(json.dumps({"wer": round(wer, 4), "peakMiB": round(peak)}))
