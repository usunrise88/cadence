"""``lid_classify@2`` — spoken language identification per utterance with SpeechBrain's VoxLingua107 ECAPA-TDNN
(``auxiliary/lid-voxlingua107``, engine ``speechbrain-ecapa``; docs/review/2026-10-03-phase-4-plan.md "Decisions taken
for phase 4" 7). The ``lid`` artifact keeps the shape ``lid_classify@1`` wrote (the NeMo pack's Whisper language
token, no longer published): JSON lines ``{audio, language, confidence, top: [[language, p], …], expected, model}``
in manifest order, which the pseudo-label ensemble and ``manifest_filter`` compare with the source's language.

It runs in the omni runtime because speechbrain needs torchaudio, which has no build for the nemo-speech image's
torch; the omni image's torch 2.8 and torchaudio 2.8 serve it (R45's one-off allowance; a step kind lives in one
runtime, R40, hence a version of its own). Help: docs/help/steps/lid-classify.md.
"""

from __future__ import annotations

import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_omni import RUNTIME, ecapa
from cadence_worker.members import member_label, read_utterances, samples_16k, write_jsonl
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

ROLE = "lid"
ENGINE = "speechbrain-ecapa"


class LidParams(BaseModel):
    auxiliary: str = cadence_field(
        description="The language classifier (auxiliary/<name>, ver_… or @alias): an auxiliary with the lid role and "
        "engine speechbrain-ecapa the project adopted",
        default_ref="packs.omni.lid_auxiliary",
        registry_ref={"kind": "auxiliary", "role": ROLE},
    )
    top_k: int = cadence_field(default_ref="packs.omni.lid_top_k")
    batch_size: int = cadence_field(default_ref="packs.omni.lid_batch_size")


def device() -> str:
    import torch

    return "cuda:0" if torch.cuda.is_available() else "cpu"


class LidClassifyStep:
    version: ClassVar[str] = "2"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"lid": "lid"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 4, "diskGb": 1, "jobKind": "data"}
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = LidParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = LidParams.model_validate(params.model_dump())
        aux = ctx.auxiliary("auxiliary")
        payload: Mapping[str, Any] = aux["payload"]
        if ROLE not in (payload.get("roles") or []):
            raise StepInputError(f"{aux.get('name')} has no {ROLE} role")
        repo, revision = str(payload.get("hfRepo") or ""), str(payload.get("revision") or "")
        engine = str(payload.get("engine") or "")
        if engine != ENGINE or not repo or not revision:
            raise StepInputError(
                f"{aux.get('name')}: lid_classify@2 loads engine {ENGINE} from pinned weights (hfRepo, revision); the "
                f"payload names engine {engine!r} — Whisper's own detection comes from the whisper_transcribe member"
            )
        utts = read_utterances(inputs["data"])
        tempfile.tempdir = str(ctx.work_dir)
        model_ref = {
            "auxiliary": aux.get("name"),
            "versionId": aux.get("versionId"),
            "hfRepo": repo,
            "revision": revision,
            "engine": engine,
        }
        rows: list[dict[str, Any]] = []
        if utts:
            ctx.progress(0.0, f"loading {repo}@{revision[:8]}")
            dev = device()
            if dev == "cpu":
                ctx.log("no card: classifying on the CPU", level="warn")
            clf = ecapa.Ecapa(ecapa.snapshot(repo, revision), ctx.work_dir, dev)
            ctx.log("classifier loaded", model=repo, revision=revision, device=dev, languages=len(clf.codes))
            # An OOM retry (or a manual one) runs at a smaller batch_scale: the batch shrinks with it.
            size = max(1, int(p.batch_size * ctx.batch_scale))
            for i in range(0, len(utts), size):
                batch = utts[i : i + size]
                for u, top in zip(batch, clf.classify([samples_16k(u.path) for u in batch], p.top_k), strict=True):
                    rows.append(
                        {
                            "audio": u.hash,
                            "language": top[0][0],
                            "confidence": round(top[0][1], 4),
                            "top": [[lang, round(prob, 4)] for lang, prob in top],
                            "expected": u.language,
                            "model": model_ref,
                        }
                    )
                ctx.progress(len(rows) / len(utts), f"{len(rows)}/{len(utts)} utterances")
                if ctx.should_stop():
                    return  # cancelled: the harness releases the lease without outputs
        write_jsonl(outputs["lid"], rows)
        ctx.set_meta("lid", {"classifier": member_label(aux), **model_ref, "utterances": len(rows)})
        ctx.log("languages identified", utterances=len(rows), engine=engine)
