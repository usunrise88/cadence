"""The NeMo pack on a card (``pytest -m gpu`` inside the nemo-speech image): materialize the base model, transcribe the
fixture clips with and without phrase boosting, score both with ``wer_score``. Needs the base model in the Hugging
Face cache (``HF_HOME`` or ``CADENCE_HF_READONLY_CACHES``) or network access."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest

from cadence_nemo.family import FAMILY, NAME
from cadence_nemo.steps.materialize import CheckpointFromBaseStep
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset
from cadence_worker.steps.wer_score import WerScoreParams, WerScoreStep

FIXTURES = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"
NORMALIZER = {
    "locale": "he-IL",
    "unicode": "NFC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": True,
    "mappings": [],
    "numbers": "keep",
}

pytestmark = pytest.mark.gpu


def _ctx(tmp: Path) -> StepContext:
    return StepContext(lambda e: None, work_dir=tmp, card=Card(0, 8192))


TRANSCRIBE = """
import json, sys
from pathlib import Path
from cadence_nemo.steps.transcribe import TranscribeParams, TranscribeStep
from cadence_worker.steps.context import Card, StepContext
inputs, out, tmp = json.loads(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
ctx = StepContext(lambda e: None, work_dir=tmp, card=Card(0, 8192))
TranscribeStep().run(TranscribeParams(), {k: Path(v) for k, v in inputs.items()}, {"hypotheses": out}, ctx)
"""


def _transcribe(tmp: Path, inputs: dict[str, Path], out: Path) -> None:
    """Each decode in its own process, as a lease runs it (NeMo 3.0 does not restore a second .nemo in one process)."""
    args = [sys.executable, "-c", TRANSCRIBE, json.dumps({k: str(v) for k, v in inputs.items()}), str(out), str(tmp)]
    subprocess.run(args, check=True)


def _score(tmp: Path, hyps: Path, data: Path, tag: str) -> dict[str, Any]:
    (tmp / "norm.json").write_text(json.dumps(NORMALIZER), encoding="utf-8")
    out = tmp / f"scores-{tag}"
    inputs = {"hypotheses": hyps, "data": data, "normalizer": tmp / "norm.json"}
    WerScoreStep().run(WerScoreParams(), inputs, {"scores": out}, _ctx(tmp))
    return dict(json.loads((out / "summary.json").read_text(encoding="utf-8")))


def test_materialize_transcribe_boost_and_score(tmp_path: Path) -> None:
    torch = pytest.importorskip("torch")
    pytest.importorskip("nemo")
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
    base = {
        "format": "cadence.base_model/1",
        "versionId": "ver_base",
        "family": {"name": NAME},
        "model": FAMILY.conformance["base_model"],
    }
    (tmp_path / "base.json").write_text(json.dumps(base), encoding="utf-8")
    model = tmp_path / "model"
    CheckpointFromBaseStep().run(
        CheckpointFromBaseStep.Params(), {"base": tmp_path / "base.json"}, {"checkpoint": model}, _ctx(tmp_path)
    )
    plain = tmp_path / "plain.jsonl"
    _transcribe(tmp_path, {"model": model, "data": data}, plain)
    s0 = _score(tmp_path, plain, data, "plain")
    # Boost every reference word the plain decode missed.
    rows = [json.loads(x) for x in (tmp_path / "scores-plain" / "utterances.jsonl").read_text("utf-8").splitlines()]
    missed = sorted({ref for r in rows for op, ref, _ in r["ops"] if op in "SD" and ref and len(ref) >= 4})
    assert missed, "the base model decodes every fixture word; pick another check"
    (tmp_path / "boost.txt").write_text("# weight: 0.5\n" + "\n".join(missed) + "\n", encoding="utf-8")
    boosted = tmp_path / "boosted.jsonl"
    _transcribe(tmp_path, {"model": model, "data": data, "boost": tmp_path / "boost.txt"}, boosted)
    s1 = _score(tmp_path, boosted, data, "boosted")
    a = json.loads(plain.read_text("utf-8").splitlines()[0])
    b = json.loads(boosted.read_text("utf-8").splitlines()[0])
    assert "boost" not in a["decoding"]
    assert b["decoding"]["boost"]["weight"] == 0.5
    assert b["decoding"]["boost"]["terms"] == len(missed)
    assert a["decodingHash"] != b["decodingHash"]
    assert a["weightsHash"] == b["weightsHash"]

    def recalled(tag: str) -> int:
        lines = (tmp_path / f"scores-{tag}" / "utterances.jsonl").read_text("utf-8").splitlines()
        rows = [json.loads(x) for x in lines]
        return sum(1 for r in rows for w in r["hyp"].split() if w in missed and w in r["ref"].split())

    assert recalled("boosted") > recalled("plain"), (s0["wer"], s1["wer"])


LIVE = """
import json, sys
from pathlib import Path
import numpy as np
from cadence_nemo import pipeline
from cadence_nemo.checkpoint import NEMO_FILE
from cadence_nemo.family import att_context_size, profile
from cadence_nemo.steps.transcribe import read_clip
from cadence_worker import live

model, data, out = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
clips = sorted((data / "audio").rglob("*.wav")) if (data / "audio").is_dir() else sorted(data.rglob("*.wav"))
m = pipeline.load(str(model / NEMO_FILE), {"160ms": att_context_size(profile("160ms"))}, stop_history_eou_ms=800)
pipe, att = m.pipelines["160ms"], m.att["160ms"]
auds = [read_clip(c) for c in clips]
streams = [
    pipeline.PipelineStream(target="A", pipeline=pipe, att=att, profile="160ms", language="he-IL") for _ in clips
]
files = [r.text for r in pipeline.decode_batch(streams, auds)]


class Channel:
    def __init__(self, msgs):
        self.inbox, self.sent = msgs, []

    def recv(self, timeout):
        if self.inbox:
            return self.inbox.pop(0)
        raise live.ChannelClosedError("closed")

    def send(self, text):
        self.sent.append(json.loads(text))

    def close(self, code=1000, reason=""):
        pass


lives = []
for a in auds:
    pcm = np.clip(np.round(a * 32768), -32768, 32767).astype("<i2")
    frames = [pcm[i : i + 320].tobytes() for i in range(0, pcm.size, 320)]
    start = json.dumps({"type": "start", "input": {"kind": "microphone", "sampleRate": 16000}})
    ch = Channel([start, *frames, json.dumps({"type": "end"})])
    st = pipeline.PipelineStream(target="A", pipeline=pipe, att=att, profile="160ms", language="he-IL")
    p = live.LiveParams.model_validate({"session": "t", "targets": [{"target": "A", "model": "m", "profile": "160ms",
                                        "language": "he-IL"}], "input": {"kind": "microphone"}})
    live.serve(ch, [st], p, work_dir=out.parent)
    lives.append(pipeline.join_finals([e for e in ch.sent if e["type"] == "final"]))
out.write_text(json.dumps({"files": files, "lives": lives}, ensure_ascii=False), encoding="utf-8")
"""


def test_live_words_equal_the_eval_decode(tmp_path: Path) -> None:
    """A live session (20 ms microphone frames through cadence_worker.live) and nemotron_transcribe@3's batched file
    decode give the same words: one decoder for live and evals (spike A5 finding 2)."""
    torch = pytest.importorskip("torch")
    pytest.importorskip("nemo")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
    base = {
        "format": "cadence.base_model/1",
        "versionId": "ver_base",
        "family": {"name": NAME},
        "model": FAMILY.conformance["base_model"],
    }
    (tmp_path / "base.json").write_text(json.dumps(base), encoding="utf-8")
    model = tmp_path / "model"
    CheckpointFromBaseStep().run(
        CheckpointFromBaseStep.Params(), {"base": tmp_path / "base.json"}, {"checkpoint": model}, _ctx(tmp_path)
    )
    out = tmp_path / "live.json"
    subprocess.run([sys.executable, "-c", LIVE, str(model), str(FIXTURES), str(out)], check=True)
    doc = json.loads(out.read_text(encoding="utf-8"))
    assert doc["files"] == doc["lives"]
    assert any(doc["files"]), "the base model decodes some fixture words"


MIXED = """
import json, sys
from pathlib import Path
import numpy as np
from cadence_nemo import pipeline
from cadence_nemo.checkpoint import NEMO_FILE
from cadence_nemo.family import att_context_size, profile
from cadence_nemo.steps.transcribe import read_clip

model, data, out = Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])
names = ["80ms", "1120ms"]
m = pipeline.load(str(model / NEMO_FILE), {n: att_context_size(profile(n)) for n in names}, stop_history_eou_ms=800)
he = [read_clip(data / f"he0{i}.wav") for i in range(1, 7)]
clips = {
    "tiny": he[0][:4000],  # 0.25 s: less than a first chunk, its only buffer is first and last at once
    "short": he[1][:19200],  # 1.2 s
    "mid": he[2],
    "long": np.concatenate([he[3], he[4], he[5]]),
}
clips["long2"] = clips["long"].copy()
doc = {}
for n in names:
    def decode(keys):
        streams = [pipeline.PipelineStream(target="A", pipeline=m.pipelines[n], att=m.att[n], profile=n,
                                           language="he-IL")
                   for _ in keys]
        return {k: {"text": r.text, "words": [w["word"] for w in r.words], "partials": [p["text"] for p in r.partials]}
                for k, r in zip(keys, pipeline.decode_batch(streams, [clips[k] for k in keys]))}
    doc[n] = {
        "mixed": decode(["tiny", "long", "short"]),
        "alone": {k: decode([k])[k] for k in ("tiny", "short", "long")},
        "with_mid": decode(["mid", "long"])["long"],
        "with_copy": decode(["long", "long2"]),
    }
out.write_text(json.dumps(doc, ensure_ascii=False), encoding="utf-8")
"""


def test_a_batch_of_short_and_long_clips_decodes_each_as_alone(tmp_path: Path) -> None:
    """decode_batch steps streams of different lengths together (a short clip's last buffer beside a long clip's
    middle ones, a sub-chunk clip whose first buffer is also its last): each clip gets the words, and the partials, it
    gets decoded alone, and a clip's words never depend on which clips share its steps (at 80 ms they may depend on
    how many do: see decode_batch)."""
    torch = pytest.importorskip("torch")
    pytest.importorskip("nemo")
    if not torch.cuda.is_available():
        pytest.skip("no CUDA card")
    base = {
        "format": "cadence.base_model/1",
        "versionId": "ver_base",
        "family": {"name": NAME},
        "model": FAMILY.conformance["base_model"],
    }
    (tmp_path / "base.json").write_text(json.dumps(base), encoding="utf-8")
    model = tmp_path / "model"
    CheckpointFromBaseStep().run(
        CheckpointFromBaseStep.Params(), {"base": tmp_path / "base.json"}, {"checkpoint": model}, _ctx(tmp_path)
    )
    out = tmp_path / "mixed.json"
    subprocess.run([sys.executable, "-c", MIXED, str(model), str(FIXTURES), str(out)], check=True)
    doc = json.loads(out.read_text(encoding="utf-8"))
    for prof, d in doc.items():
        # The short clips end early, so the long one steps alone most of the way: as alone, at every profile.
        assert d["mixed"] == d["alone"], prof
        # Two streams of the same audio in one batch decode alike, and like the long clip beside another clip.
        assert d["with_copy"]["long"] == d["with_copy"]["long2"] == d["with_mid"], prof
    assert any(d["alone"]["long"]["text"] for d in doc.values()), "the base model decodes the long clip"
