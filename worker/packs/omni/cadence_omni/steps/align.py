"""``align_reference@1`` — word timings of a dataset's reference texts (docs/review/2026-10-03-phase-4-plan.md decision
8; R51, R54): CTC emissions of the auxiliary aligner (``auxiliary/omniasr-ctc-1b``) forced through each reference
with torchaudio's ``forced_align`` (or the NumPy Viterbi of :mod:`cadence_worker.ctc_align`), written as an
``alignment`` artifact (:mod:`cadence_worker.reference_alignment`). The golden set whose dataset it aligned carries the
result: emission delay (``latency_score@3``) and the audio view's reference track read it.

An utterance stays unaligned, with its reason and no times, when its language is not one the aligner's payload lists,
it is longer than ``max_duration_s``, it has no text or the audio is too short for its text. When no utterance can be
aligned the model is never loaded. Help: docs/help/steps/align-reference.md.
"""

from __future__ import annotations

import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_omni import RUNTIME
from cadence_omni.aligner import UnalignedError, align_utterance
from cadence_worker import reference_alignment as ra
from cadence_worker.members import primary, read_utterances, samples_16k
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

ROLE = "align"


class AlignReferenceParams(BaseModel):
    aligner: str = cadence_field(
        description="The aligner auxiliary (auxiliary/<name>, ver_… or @alias): a CTC model the project adopted with "
        "the align role",
        default_ref="packs.omni.align_auxiliary",
        registry_ref={"kind": "auxiliary", "role": ROLE},
    )
    max_duration_s: float = cadence_field(default_ref="packs.omni.align_max_duration_s")
    dtype: Literal["bfloat16", "float32"] = cadence_field(default_ref="packs.omni.align_dtype")


def covers(languages: Any, language: str) -> bool:
    """Whether an auxiliary's ``languages`` (primary subtags, or ``*``) include an utterance's language."""
    if not isinstance(languages, list):
        return False
    if "*" in languages:
        return True
    return bool(language) and primary(language) in {primary(str(x)) for x in languages}


def why_not(u_language: str, duration: float, text: str, languages: Any, label: str, max_s: float) -> str | None:
    """Why an utterance is left unaligned before the model sees it, or None."""
    if not text.split():
        return "the reference text is empty"
    if not u_language:
        return "the utterance names no language"
    if not covers(languages, u_language):
        return f"{label} does not cover {u_language}"
    if duration > max_s:
        return f"longer than {max_s:g} s (packs.omni.align_max_duration_s)"
    return None


class AlignReferenceStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"alignment": "alignment"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 5, "jobKind": "data"}
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = AlignReferenceParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = AlignReferenceParams.model_validate(params.model_dump())
        aux = ctx.auxiliary("aligner")
        payload: Mapping[str, Any] = aux["payload"]
        if ROLE not in (payload.get("roles") or []):
            raise StepInputError(f"{aux.get('name')} has no {ROLE} role")
        repo, revision = str(payload.get("hfRepo") or ""), str(payload.get("revision") or "")
        if not repo or not revision:
            raise StepInputError(
                f"{aux.get('name')} names no weights (hfRepo, revision); it is not a model this step loads"
            )
        label = str(aux.get("name") or repo)
        utts = read_utterances(inputs["data"])
        reasons = [
            why_not(u.language, u.duration, u.text, payload.get("languages"), label, p.max_duration_s) for u in utts
        ]
        aligner = {
            "auxiliary": aux.get("name"),
            "versionId": aux.get("versionId"),
            "hfRepo": repo,
            "revision": revision,
        }
        rows: list[dict[str, Any]] = []
        method, frame_ms = "", None
        model = forced = None
        if any(r is None for r in reasons):
            from cadence_omni import omniasr

            tempfile.tempdir = str(ctx.work_dir)
            ctx.progress(0.0, f"loading {repo}@{revision[:8]}")
            device = omniasr.device()
            if device == "cpu":
                ctx.log("no card: aligning on the CPU (slow)", level="warn")
            model = omniasr.OmniCtc(omniasr.snapshot(repo, revision), device, p.dtype)
            method, forced = omniasr.forced_aligner()
            ctx.log("aligner loaded", model=repo, revision=revision, method=method, device=device)
        for n, (u, reason) in enumerate(zip(utts, reasons, strict=True)):
            row: dict[str, Any] = {"audio": u.hash, "text": u.text, "language": u.language}
            if reason is None and model is not None and forced is not None:
                try:
                    res = align_utterance(model, samples_16k(u.path), u.text, forced)
                    row.update({"aligned": True, "words": res.words, "skipped": res.skipped})
                    frame_ms = frame_ms or round(res.frame_s * 1000, 3)
                except UnalignedError as e:
                    reason = str(e)
            if reason is not None:
                row.update({"aligned": False, "reason": reason})
            rows.append(row)
            if (n + 1) % 25 == 0 or n + 1 == len(utts):
                ctx.progress((n + 1) / len(utts), f"{n + 1}/{len(utts)} utterances")
            if ctx.should_stop():
                return  # cancelled: the harness releases the lease without outputs
        aligned = sum(1 for r in rows if r["aligned"])
        words = sum(len(r.get("words") or []) for r in rows)
        header = {
            "aligner": aligner,
            "method": method or None,
            "frameMs": frame_ms,
            "utterances": len(rows),
            "aligned": aligned,
            "unaligned": len(rows) - aligned,
            "words": words,
        }
        ra.write(outputs["alignment"], header, rows)
        reasons_seen = sorted({str(r["reason"]) for r in rows if not r["aligned"]})
        ctx.set_meta(
            "alignment",
            {
                "format": ra.FORMAT,
                "aligner": aligner,
                "method": method or None,
                "utterances": len(rows),
                "aligned": aligned,
                "unaligned": len(rows) - aligned,
                "words": words,
                **({"reasons": reasons_seen[:5]} if reasons_seen else {}),
            },
        )
        ctx.final_metric("aligned_utterances", aligned)
        ctx.log("references aligned", utterances=len(rows), aligned=aligned, words=words)
