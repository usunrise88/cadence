"""lid_classify — spoken language identification per utterance (docs/review/2026-10-03-phase-4-plan.md "Decisions
taken for phase 4" 7): the ``lid`` artifact the pseudo-label ensemble and ``manifest_filter`` compare with the
source's language. JSON lines ``{audio, language, confidence, top: [[language, p], …], model}`` in manifest order.

The classifier is the auxiliary the ``auxiliary`` parameter names (role ``lid``), by its payload's ``engine``:

- ``transformers-whisper``: Whisper's language token after the start token (``auxiliary/whisper-large-v3``), the
  distribution over its 99-100 languages from the first 30 s; loaded per job in fp16.
- ``speechbrain-ecapa``: SpeechBrain's VoxLingua107 ECAPA-TDNN (``auxiliary/lid-voxlingua107``). It needs speechbrain
  and torchaudio, which the nemo-speech image lacks: torchaudio publishes no build for the image's torch 2.12, and
  speechbrain 1.1 imports torchaudio at start (checked 2026-10-03). The step then fails with a message naming the
  fallback, and the bundled default is the Whisper classifier.

Help: docs/help/steps/lid-classify.md.
"""

from __future__ import annotations

import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any, ClassVar

import numpy as np
from pydantic import BaseModel

from cadence_nemo import whisper
from cadence_nemo.family import RUNTIME
from cadence_nemo.steps.common import card, device
from cadence_worker.members import Utterance, member_label, read_utterances, samples_16k, write_jsonl
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

ENGINE_WHISPER = "transformers-whisper"
ENGINE_ECAPA = "speechbrain-ecapa"


class LidParams(BaseModel):
    auxiliary: str = cadence_field(
        description="The language classifier (auxiliary/<name>, ver_… or @alias): an auxiliary with the lid role the "
        "project adopted",
        default_ref="packs.nemo.lid_auxiliary",
        registry_ref={"kind": "auxiliary", "role": "lid"},
    )
    top_k: int = cadence_field(default_ref="packs.nemo.lid_top_k")
    batch_size: int = cadence_field(default_ref="packs.nemo.lid_batch_size")
    cuda_context_reserve_mb: int = cadence_field(default_ref="packs.nemo.cuda_context_reserve_mb")


def _ecapa(path: Path, work: Path, dev: str) -> Any:
    try:
        from speechbrain.inference.classifiers import EncoderClassifier
    except ImportError as e:
        raise StepInputError(
            "this runtime has no speechbrain/torchaudio (the nemo-speech image cannot install torchaudio for its "
            "torch); classify with auxiliary/whisper-large-v3 (the default), or run the step in a runtime that has them"
        ) from e
    return EncoderClassifier.from_hparams(source=str(path), savedir=str(work / "speechbrain"), run_opts={"device": dev})


def _ecapa_rows(clf: Any, clips: list[np.ndarray[Any, np.dtype[np.float32]]], k: int) -> list[list[tuple[str, float]]]:
    import torch

    n = max(len(c) for c in clips)
    wavs = torch.zeros(len(clips), n)
    for i, c in enumerate(clips):
        wavs[i, : len(c)] = torch.from_numpy(c)
    lens = torch.tensor([len(c) / n for c in clips])
    with torch.inference_mode():
        logp = clf.classify_batch(wavs, lens)[0].float().cpu().numpy().astype(np.float64)
    labels = [str(clf.hparams.label_encoder.decode_ndim(i)).split(":", 1)[0].strip() for i in range(logp.shape[1])]
    return [whisper.rank_languages(whisper.softmax(row).tolist(), labels, k) for row in logp]


class LidClassifyStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"lid": "lid"}
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 8, "diskGb": 4, "jobKind": "data"}
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = LidParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = LidParams.model_validate(params.model_dump())
        aux = ctx.auxiliary("auxiliary")
        payload: Mapping[str, Any] = aux["payload"]
        repo, revision = str(payload.get("hfRepo") or ""), str(payload.get("revision") or "")
        engine = str(payload.get("engine") or "")
        if not repo or not revision or engine not in (ENGINE_WHISPER, ENGINE_ECAPA):
            raise StepInputError(
                f"{aux.get('name')}: this step loads engines {ENGINE_WHISPER} and {ENGINE_ECAPA} from pinned weights; "
                f"the payload names engine {engine!r}"
            )
        utts = read_utterances(inputs["data"])
        card(ctx, p.cuda_context_reserve_mb, allow_cpu=True)
        tempfile.tempdir = str(ctx.work_dir)
        ctx.progress(0.0, f"loading {repo}")
        model_ref = {
            "auxiliary": aux.get("name"),
            "versionId": aux.get("versionId"),
            "hfRepo": repo,
            "revision": revision,
            "engine": engine,
        }
        rows: list[dict[str, Any]] = []
        if engine == ENGINE_WHISPER:
            w = whisper.Whisper(repo, revision, device())

            def rank(batch: list[Utterance]) -> list[list[tuple[str, float]]]:
                return [d.top for d in w.detect([samples_16k(u.path) for u in batch], p.top_k)]
        else:
            clf = _ecapa(whisper.snapshot(repo, revision), ctx.work_dir, device())

            def rank(batch: list[Utterance]) -> list[list[tuple[str, float]]]:
                return _ecapa_rows(clf, [samples_16k(u.path) for u in batch], p.top_k)

        for i in range(0, len(utts), p.batch_size):
            batch = utts[i : i + p.batch_size]
            for u, top in zip(batch, rank(batch), strict=True):
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
                return
        write_jsonl(outputs["lid"], rows)
        ctx.set_meta("lid", {"classifier": member_label(aux), **model_ref, "utterances": len(rows)})
        ctx.log("languages identified", utterances=len(rows), engine=engine)
