"""Phase 4 · stream B: segments_cut turns the untranscribed segments of an ingest into the dataset the pseudo-label
members read, each clip the canonical WAV whose hash is the segment's identity."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from test_ingest import ingest

from cadence_worker import segments as seg
from cadence_worker.cas import hash_file
from cadence_worker.members import read_utterances
from cadence_worker.steps.base import StepInputError, missing_metadata
from cadence_worker.steps.segments_cut import PURPOSE, SegmentsCutParams, SegmentsCutStep, needs_label


class Meta:
    def __init__(self, ctx: Any) -> None:
        self.mounts = ctx.mounts
        self.meta: dict[str, Any] = {}

    def progress(self, fraction: float, message: str = "") -> None:
        pass

    def set_meta(self, output: str, meta: dict[str, Any]) -> None:
        self.meta[output] = meta


def test_metadata_complete() -> None:
    assert missing_metadata(SegmentsCutStep) == []


def test_cuts_the_unlabelled_segments_with_their_hashes(tmp_path: Path) -> None:
    segments, ctx = ingest(tmp_path)
    rows = seg.read_segments(segments)
    want = [r for r in rows if needs_label(r)]
    assert want, "the fixture has untranscribed segments"
    assert len(want) < len(rows), "the fixture has transcribed segments"
    out = tmp_path / "data"
    m = Meta(ctx)
    SegmentsCutStep().run(SegmentsCutParams(), {"segments": segments}, {"data": out}, m)

    header = json.loads((out / "dataset.json").read_text(encoding="utf-8"))
    assert header["format"] == "cadence.dataset/1"
    assert header["purpose"] == PURPOSE
    assert m.meta["data"]["purpose"] == PURPOSE
    assert m.meta["data"]["utterances"] == len(want)
    utts = read_utterances(out)  # what every member reads
    assert [u.hash for u in utts] == [r["hash"] for r in want]
    assert all(u.text == "" and u.language == "sr-RS" for u in utts)
    for u in utts:
        assert hash_file(u.path) == u.hash  # the members key their rows by this hash

    # which: all cuts every segment, the bot's scripted turns and the transcribed files included.
    every = tmp_path / "all"
    SegmentsCutStep().run(SegmentsCutParams(which="all"), {"segments": segments}, {"data": every}, Meta(ctx))
    assert len(read_utterances(every)) == len(rows)


def test_nothing_to_label_is_an_input_error(tmp_path: Path) -> None:
    segments, ctx = ingest(tmp_path)
    rows = [
        {**r, "text": r.get("text") or "tekst", "origin": r.get("origin") or "human"}
        for r in seg.read_segments(segments)
    ]
    labelled = tmp_path / "labelled"
    seg.write_segments(labelled, rows, seg.read_header(segments))
    with pytest.raises(StepInputError, match="no segment needs a label"):
        SegmentsCutStep().run(SegmentsCutParams(), {"segments": labelled}, {"data": tmp_path / "out"}, Meta(ctx))
