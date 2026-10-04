"""whisper_transcribe — a Whisper pseudo-label member (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for
phase 4" 6): decode every utterance of a ``dataset`` with the auxiliary's Whisper weights, the language forced to the
utterance's, and write ``hypotheses`` keyed by audio hash with Whisper's own language identification beside the text
(the ensemble's second opinion on LID, decision 7).

The weights come from the auxiliary version the ``auxiliary`` parameter names (``auxiliary/whisper-large-v3``, or
``auxiliary/whisper-he-ivrit`` for Hebrew only), resolved by the control plane for the project: ``hfRepo`` at its pinned
``revision``, loaded for this job (R45's one-off allowance) through transformers in fp16, about 3.5 GB on the card for
large-v3. Rows: ``{audio, text, member, language, detectedLanguage, languageConfidence, model, decodingHash}``.
Help: docs/help/steps/whisper-transcribe.md.
"""

from __future__ import annotations

import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_nemo import whisper
from cadence_nemo.family import RUNTIME
from cadence_nemo.steps.common import card, device
from cadence_worker.members import decoding_hash, member_label, read_utterances, samples_16k, write_jsonl
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext
from cadence_worker.translit import transliterate


class WhisperParams(BaseModel):
    auxiliary: str = cadence_field(
        description="The auxiliary model to decode with (auxiliary/<name>, ver_… or @alias): a Whisper the project "
        "adopted, with the pseudolabel role",
        default_ref="packs.nemo.whisper_auxiliary",
        registry_ref={"kind": "auxiliary", "role": "pseudolabel"},
    )
    batch_size: int = cadence_field(default_ref="packs.nemo.whisper_batch_size")
    num_beams: int = cadence_field(default_ref="packs.nemo.whisper_num_beams")
    detect_language: bool = cadence_field(default_ref="packs.nemo.whisper_detect_language")
    target_lang: str = cadence_field(
        "",
        description="Decode every utterance in this language (BCP-47) instead of each utterance's own; empty uses the "
        "utterance's language",
        source="Cadence recommendation",
        range={"maxLength": 35},
    )
    transliterate: Literal["", "sr-Cyrl-Latn"] = cadence_field(
        "",
        description="Convert the text to another script (sr-Cyrl-Latn: Serbian Cyrillic to Gaj Latin, as "
        "dataset_import does); empty keeps Whisper's script",
        source="Cadence recommendation (the Serbian fine-tune on the test stand, 2026-10-01)",
        range={"values": ["", "sr-Cyrl-Latn"]},
    )
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")


class WhisperTranscribeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 4, "jobKind": "data"}
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = WhisperParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = WhisperParams.model_validate(params.model_dump())
        aux = ctx.auxiliary("auxiliary")
        payload: Mapping[str, Any] = aux["payload"]
        repo, revision = str(payload.get("hfRepo") or ""), str(payload.get("revision") or "")
        if not repo or not revision:
            raise StepInputError(
                f"{aux.get('name')} names no weights (hfRepo, revision); it is not a model this step loads"
            )
        utts = read_utterances(inputs["data"])
        member = member_label(aux)
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        tempfile.tempdir = str(ctx.work_dir)
        ctx.progress(0.0, f"loading {repo}")
        model = whisper.Whisper(repo, revision, device())
        decoding = {
            "decoder": "transformers-whisper",
            "model": repo,
            "revision": revision,
            "numBeams": p.num_beams,
            "task": "transcribe",
            "transliterate": p.transliterate,
        }
        dhash = decoding_hash(decoding)
        rows: list[dict[str, Any]] = []
        # An OOM retry (or a manual one) runs at a smaller batch_scale: the batch shrinks with it.
        for idx in whisper.batches([u.duration for u in utts], max(1, int(p.batch_size * ctx.batch_scale))):
            batch = [utts[i] for i in idx]
            clips = [samples_16k(u.path) for u in batch]
            langs = {whisper.whisper_language(p.target_lang or u.language, model.codes) for u in batch}
            detections = model.detect(clips) if p.detect_language else [None] * len(batch)
            # One forced language per generate call: a batch of mixed languages splits by language.
            texts: dict[int, str] = {}
            for lang in sorted(langs):
                sel = [
                    k
                    for k, u in enumerate(batch)
                    if whisper.whisper_language(p.target_lang or u.language, model.codes) == lang
                ]
                for k, t in zip(sel, model.transcribe([clips[k] for k in sel], lang, p.num_beams), strict=True):
                    texts[k] = transliterate(t, p.transliterate)
            for k, u in enumerate(batch):
                row: dict[str, Any] = {
                    "audio": u.hash,
                    "text": texts[k],
                    "member": member,
                    "language": whisper.whisper_language(p.target_lang or u.language, model.codes),
                    "model": {
                        "auxiliary": aux.get("name"),
                        "versionId": aux.get("versionId"),
                        "hfRepo": repo,
                        "revision": revision,
                    },
                    "decodingHash": dhash,
                }
                det = detections[k]
                if det is not None:
                    row["detectedLanguage"] = det.language
                    row["languageConfidence"] = round(det.confidence, 4)
                rows.append(row)
            ctx.progress(len(rows) / len(utts), f"{len(rows)}/{len(utts)} utterances")
            if ctx.should_stop():
                return  # cancelled: the harness releases the lease without outputs
        write_jsonl(outputs["hypotheses"], rows)
        ctx.set_meta(
            "hypotheses",
            {
                "member": member,
                "auxiliary": aux.get("name"),
                "versionId": aux.get("versionId"),
                "hfRepo": repo,
                "revision": revision,
                "decodingHash": dhash,
                "utterances": len(rows),
            },
        )
        ctx.log("whisper decoded", utterances=len(rows), member=member)
