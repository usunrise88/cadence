"""``manifest_filter@2`` — drop segments that would hurt training: wrong length, a transcript that does not fit its
audio (characters per second), no text, the wrong channel role, a disputed pseudo-label, a language the dataset does
not want or one that language identification disagrees with, too little speech, or another party talking over it.

Each dropped segment is counted under its first failing reason in the header's ``filtered`` (added to counts an
earlier filter left). Language identification is the segment's ``lid.language`` (``pseudolabel_ensemble`` writes it
on every row it has evidence for), compared with the segment's language by primary subtag or within one of
``lid_equivalents`` (version 2; version 1 compared primary subtags only). Help: docs/help/steps/manifest-filter.md.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import segments as seg
from cadence_worker.members import same_language
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import cadence_field

KIND = "manifest_filter@2"


class ManifestFilterParams(BaseModel):
    min_duration: float = cadence_field(default_ref="data.filter_min_duration_s")
    max_duration: float = cadence_field(default_ref="data.filter_max_duration_s")
    min_chars_per_second: float = cadence_field(default_ref="data.filter_min_chars_per_second")
    max_chars_per_second: float = cadence_field(default_ref="data.filter_max_chars_per_second")
    require_text: bool = cadence_field(
        True,
        description="Drop segments without a transcript (untranscribed audio goes through pseudo-labelling first)",
        source="Cadence recommendation",
        range={"values": [True, False]},
    )
    roles: list[str] = cadence_field(
        ["caller", "mono"],
        description="Channel roles kept; the caller's channel is the target, the bot's is synthetic speech",
        source="docs/review/2026-10-03-phase-4-plan.md (the caller's channel is the target)",
        range={"maxLength": 3},
    )
    drop_origins: list[str] = cadence_field(
        ["pseudo-label:disputed"],
        description="Transcript origins dropped; disputed pseudo-labels go to triage, never to training",
        source="docs/review/2026-10-03-phase-4-plan.md decision 6",
        range={"maxLength": 20},
    )
    languages: list[str] = cadence_field(
        [],
        description="Languages kept (sr matches sr-RS); empty keeps every language",
        source="Cadence recommendation",
        range={"maxLength": 50},
    )
    drop_lid_mismatch: bool = cadence_field(
        True,
        description="Drop segments whose language identification (lid.language) disagrees with their language",
        source="docs/spec/04-blocks.md Block 1 (mismatched-language segments)",
        range={"values": [True, False]},
    )
    lid_equivalents: list[list[str]] = cadence_field(default_ref="pseudolabel.lid_equivalents")
    min_speech_ratio: float = cadence_field(default_ref="data.filter_min_speech_ratio")
    max_crosstalk: float = cadence_field(default_ref="data.filter_max_crosstalk")


def primary(lang: str) -> str:
    return lang.strip().replace("_", "-").split("-")[0].lower()


def chars_per_second(text: str, duration: float) -> float:
    return len("".join(text.split())) / duration if duration > 0 else float("inf")


def reason(p: ManifestFilterParams, x: Mapping[str, Any]) -> str | None:
    """Why segment x is dropped, or None."""
    dur = float(x.get("duration", 0.0))
    text = x.get("text")
    if not p.min_duration <= dur <= p.max_duration:
        return "duration"
    if str(x.get("role", "mono")) not in p.roles:
        return "role"
    if x.get("origin") in p.drop_origins:
        return "origin"
    if not isinstance(text, str) or not text.strip():
        if p.require_text:
            return "empty_text"
    elif not p.min_chars_per_second <= chars_per_second(text, dur) <= p.max_chars_per_second:
        return "chars_per_second"
    lang = str(x.get("language") or "")
    if p.languages and primary(lang) not in {primary(v) for v in p.languages}:
        return "language"
    lid = x.get("lid")
    lid_lang = str(lid.get("language") or "") if isinstance(lid, Mapping) else ""
    if p.drop_lid_mismatch and lid_lang and lang and not same_language(lid_lang, lang, p.lid_equivalents):
        return "lid_mismatch"
    vad = x.get("vad")
    if isinstance(vad, Mapping) and float(vad.get("ratio", 1.0)) < p.min_speech_ratio:
        return "speech_ratio"
    if float(x.get("crosstalk", 0.0)) > p.max_crosstalk:
        return "crosstalk"
    return None


class ManifestFilterStep:
    version: ClassVar[str] = "2"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = ManifestFilterParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = ManifestFilterParams.model_validate(params.model_dump())
        header, lines = seg.read(inputs["segments"])
        filtered: dict[str, int] = {str(k): int(v) for k, v in (header.get("filtered") or {}).items()}
        kept = []
        for x in lines:
            why = reason(p, x)
            if why is None:
                kept.append(x)
            else:
                filtered[why] = filtered.get(why, 0) + 1
        h = seg.with_step(header, KIND)
        h["filtered"] = dict(sorted(filtered.items()))
        seg.write(outputs["segments"], h, kept, files_from=inputs["segments"])
        log = getattr(ctx, "log", None)
        if callable(log):
            log(f"kept {len(kept)} of {len(lines)} segments; dropped {h['filtered']}")
