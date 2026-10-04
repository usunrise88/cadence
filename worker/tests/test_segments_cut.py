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
from cadence_worker.segments import needs_label
from cadence_worker.steps.base import StepInputError, missing_metadata
from cadence_worker.steps.segments_cut import PURPOSE, SegmentsCutParams, SegmentsCutStep


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
    _, rows = seg.read_segments(segments)
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
    header, rows = seg.read_segments(segments)
    rows = [{**r, "text": r.get("text") or "tekst", "origin": r.get("origin") or "human"} for r in rows]
    labelled = tmp_path / "labelled"
    seg.write(labelled, header, rows, files_from=segments)
    with pytest.raises(StepInputError, match="no segment needs a label"):
        SegmentsCutStep().run(SegmentsCutParams(), {"segments": labelled}, {"data": tmp_path / "out"}, Meta(ctx))


def test_a_repeated_segment_is_cut_once(tmp_path: Path) -> None:
    segments, ctx = ingest(tmp_path)
    header, rows = seg.read_segments(segments)
    first = next(r for r in rows if needs_label(r))
    doubled = tmp_path / "doubled"
    seg.write(doubled, header, [*rows, dict(first)], files_from=segments)
    _, again = seg.read_segments(doubled)
    assert len(again) == len(rows) + 1, "the reader keeps a repeated hash"
    out = tmp_path / "data"
    SegmentsCutStep().run(SegmentsCutParams(), {"segments": doubled}, {"data": out}, Meta(ctx))
    hashes = [u.hash for u in read_utterances(out)]
    assert len(hashes) == len(set(hashes)) == sum(1 for r in rows if needs_label(r))


def test_the_reader_refuses_a_row_without_a_hash(tmp_path: Path) -> None:
    segments, _ = ingest(tmp_path)
    header, rows = seg.read_segments(segments)
    bad = tmp_path / "bad"
    seg.write(bad, header, [{k: v for k, v in rows[0].items() if k != "hash"}])
    with pytest.raises(StepInputError, match="lacks hash"):
        seg.read_segments(bad)
