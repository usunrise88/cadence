"""``speaker_disjoint_split@1`` — assign train, validation and test so that one voice never lands on two sides.

Segments are grouped by speaker; a segment without a speaker is grouped by its source file (one call is one caller),
or, with ``group_by: text``, by its transcript. A group's split follows a stable hash of its key, so a re-run assigns
the same; when validation ends up below ``min_validation_utterances``, whole groups move over from train, next in line
by that hash, never past half the segments (the rule of ``dataset_import``). Help:
docs/help/steps/speaker-disjoint-split.md.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_worker import segments as seg
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import cadence_field
from cadence_worker.steps.dataset_import import _fraction

KIND = "speaker_disjoint_split@1"


class SpeakerDisjointSplitParams(BaseModel):
    validation_share: float = cadence_field(default_ref="data.validation_share")
    test_share: float = cadence_field(
        0.0,
        description="Share of groups held out as the test split (golden sets come from annotation, so usually 0)",
        source="Cadence recommendation",
        range={"min": 0, "max": 0.5},
    )
    min_validation_utterances: int = cadence_field(default_ref="data.min_validation_utterances")
    group_by: Literal["speaker", "file", "text"] = cadence_field(
        "speaker",
        description="What is kept on one side: the speaker (else the source file), the source file, or the transcript",
        source="docs/spec/04-blocks.md Block 1 (split speaker- and source-disjoint)",
        range={"values": ["speaker", "file", "text"]},
    )


def group_key(x: Mapping[str, Any], by: str) -> str:
    if by == "speaker" and x.get("speaker"):
        return f"speaker:{x['speaker']}"
    if by == "text":
        return f"text:{str(x.get('text') or '').casefold()}"
    return f"file:{x.get('file') or x.get('uri')}"


def assign(lines: list[dict[str, Any]], p: SpeakerDisjointSplitParams) -> int:
    """Set split on every line; returns how many moved to validation by the top-up."""
    keys = [group_key(x, p.group_by) for x in lines]
    for x, k in zip(lines, keys, strict=True):
        f = _fraction(k)
        x["split"] = "test" if f < p.test_share else "validation" if f < p.test_share + p.validation_share else "train"
    have = sum(1 for x in lines if x["split"] == "validation")
    if p.validation_share <= 0 or have >= p.min_validation_utterances:
        return 0
    groups: dict[str, list[dict[str, Any]]] = {}
    for x, k in zip(lines, keys, strict=True):
        if x["split"] == "train":
            groups.setdefault(k, []).append(x)
    moved = 0
    for k in sorted(groups, key=lambda g: (_fraction(g), g)):
        if have >= p.min_validation_utterances:
            break
        members = groups[k]
        if 2 * (have + len(members)) > len(lines):
            continue
        for x in members:
            x["split"] = "validation"
        have += len(members)
        moved += len(members)
    return moved


class SpeakerDisjointSplitStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = SpeakerDisjointSplitParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = SpeakerDisjointSplitParams.model_validate(params.model_dump())
        header, lines = seg.read(inputs["segments"])
        moved = assign(lines, p)
        h = seg.with_step(header, KIND)
        h["splitRule"] = "speaker-disjoint"
        seg.write(outputs["segments"], h, lines, files_from=inputs["segments"])
        log = getattr(ctx, "log", None)
        if callable(log):
            counts = {s: sum(1 for x in lines if x["split"] == s) for s in ("train", "validation", "test")}
            log(f"split {counts}; validation topped up by {moved}")
