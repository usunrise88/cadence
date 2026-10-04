"""align_reference on a card (``pytest -m gpu`` inside the omni image): the NeMo pack's FLEURS Hebrew fixture aligned by
omniASR CTC 1B under the 8 GB step cap. Needs ``facebook/omniASR-CTC-1B`` at the pinned revision in the Hugging Face
cache (``HF_HOME``) or network access for one download (about 3.9 GB).

Checks what can be checked without hand-labelled timings: every word of a covered language gets a time, in order and
inside the audio, with a confident score (a mis-normalised waveform gives scores near zero); torchaudio's
``forced_align`` and the NumPy Viterbi agree within one frame (ties broken differently); a language the aligner does
not list stays unaligned (test_align.py)."""

from __future__ import annotations

import itertools
import json
from pathlib import Path
from typing import Any

import pytest

from cadence_omni.steps.align import AlignReferenceParams, AlignReferenceStep
from cadence_worker import ctc_align
from cadence_worker import reference_alignment as ra
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURES = Path(__file__).resolve().parents[2] / "nemo" / "cadence_nemo" / "fixtures"
CAP_MB = 8192
ALIGNER: dict[str, Any] = {
    "versionId": "ver_omni",
    "name": "auxiliary/omniasr-ctc-1b",
    "version": "2026-10-03.000000000000",
    "payload": {
        "roles": ["align"],
        "licence": "Apache-2.0",
        "outputsCommercialUse": True,
        "languages": ["he", "sr", "hr"],
        "hfRepo": "facebook/omniASR-CTC-1B",
        "revision": "8c22e3ffdaa4aab6431b128b84b991a7d9c2515c",
        "engine": "fairseq2-ctc",
    },
}

pytestmark = pytest.mark.gpu


def test_align_reference_on_the_card(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("fairseq2")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
    from cadence_omni import omniasr
    from cadence_omni.aligner import align_utterance
    from cadence_worker.members import read_utterances, samples_16k

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
    ctx = StepContext(lambda e: None, work_dir=tmp_path, card=Card(0, CAP_MB), auxiliaries={"aligner": ALIGNER})
    out = tmp_path / "alignment.jsonl"
    AlignReferenceStep().run(AlignReferenceParams(), {"data": data}, {"alignment": out}, ctx)
    header, rows = ra.read(out)
    assert header["aligned"] == 10, header
    assert header["method"] == "torchaudio.forced_align"
    assert 19.5 < header["frameMs"] < 20.5
    utts = {u.hash: u for u in read_utterances(data)}
    scores: list[float] = []
    for audio, row in rows.items():
        assert row["aligned"], row
        words = row["words"]
        tokens = utts[audio].text.split()
        assert [w["index"] for w in words] == list(range(len(tokens)))
        assert all(w["start"] < w["end"] <= utts[audio].duration + 0.03 for w in words)
        assert all(a["end"] <= b["start"] for a, b in itertools.pairwise(words))
        scores.extend(w["score"] for w in words)
    assert sum(scores) / len(scores) > 0.3, scores

    # torchaudio and the NumPy Viterbi: the same path up to ties.
    model = omniasr.OmniCtc(
        omniasr.snapshot(ALIGNER["payload"]["hfRepo"], ALIGNER["payload"]["revision"]), "cuda", "bfloat16"
    )
    _, forced = omniasr.forced_aligner()
    u = next(iter(utts.values()))
    a = align_utterance(model, samples_16k(u.path), u.text, forced)
    b = align_utterance(model, samples_16k(u.path), u.text, ctc_align.viterbi)
    for x, y in zip(a.words, b.words, strict=True):
        assert abs(x["start"] - y["start"]) <= a.frame_s + 1e-3
        assert abs(x["end"] - y["end"]) <= a.frame_s + 1e-3

    peak = torch.cuda.max_memory_allocated() / 2**20
    assert peak < CAP_MB, f"{peak:.0f} MiB allocated, over the {CAP_MB} MiB step cap"
    print(json.dumps({"meanScore": round(sum(scores) / len(scores), 3), "peakMiB": round(peak)}))
