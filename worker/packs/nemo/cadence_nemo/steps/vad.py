"""frame_vad — utterance ends for latency to final (R54; phase 3 stream R): NVIDIA's Frame-VAD Multilingual MarbleNet
v2.0 (``nvidia/Frame_VAD_Multilingual_MarbleNet_v2.0``, 91.5 K parameters, a speech probability per 20 ms frame at
16 kHz) over every utterance of a dataset, thresholded into speech segments.

Licence (R26, checked 2026-10-02): the model card's "License/Terms of Use" reads "GOVERNING TERMS: Your use of this
model is governed by the NVIDIA Open Model License Agreement" and "This model is ready for commercial use"; the
agreement (version of October 24, 2025, https://www.nvidia.com/en-us/agreements/enterprise-software/nvidia-open-model-license/)
grants a "perpetual, worldwide, non-exclusive, no-charge, royalty-free, revocable license to … use … the Model" and
states "NVIDIA claims no ownership rights in outputs". It passes R26 (commercial use, outputs ours); redistributing the
model would need the agreement and the notice "Licensed by NVIDIA Corporation under the NVIDIA Open Model License" —
Cadence does not redistribute it (the worker downloads it into its Hugging Face cache). The card lists Chinese,
English, French, German, Russian and Spanish; Hebrew and the other replay locales are outside its training languages,
which the latency summary carries as the VAD's model and revision.

Consumes ``data`` (a ``dataset`` artifact); produces ``vad``: JSON lines, a header line ``{"vad": {kind, model,
revision, frameMs, onset, offset, minSpeechMs, minSilenceMs}}`` then one line per utterance in manifest order
``{audio, durationS, speech: [[start, end], …], speechEndS}`` (seconds; ``speechEndS`` null when no speech was found).
Runs on the CPU (the model is tiny); the NeMo import is the reason it lives in this pack. Help:
docs/help/steps/frame-vad.md.
"""

from __future__ import annotations

import json
import os
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_nemo.family import RUNTIME, SAMPLE_RATE
from cadence_nemo.mixdata import read_dataset_dir
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

KIND = "frame_vad@1"
FRAME_MS = 20
FORCE_FULL_UNPICKLE = "TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"  # read by torch.load at each call (PyTorch 2.6+)


class VadParams(BaseModel):
    model: str = cadence_field(default_ref="packs.nemo.vad_model")
    revision: str = cadence_field(default_ref="packs.nemo.vad_revision")
    onset: float = cadence_field(default_ref="packs.nemo.vad_onset")
    offset: float = cadence_field(default_ref="packs.nemo.vad_offset")
    min_speech_ms: int = cadence_field(default_ref="packs.nemo.vad_min_speech_ms")
    min_silence_ms: int = cadence_field(default_ref="packs.nemo.vad_min_silence_ms")


def segments(
    probs: Sequence[float], onset: float, offset: float, min_speech_ms: int, min_silence_ms: int
) -> list[tuple[float, float]]:
    """Speech segments (seconds) from per-frame speech probabilities: hysteresis (speech starts at ``onset`` and
    ends below ``offset``), gaps shorter than ``min_silence_ms`` closed, segments shorter than ``min_speech_ms``
    dropped."""
    raw: list[list[int]] = []
    start: int | None = None
    for i, p in enumerate(probs):
        if start is None and p >= onset:
            start = i
        elif start is not None and p < offset:
            raw.append([start, i])
            start = None
    if start is not None:
        raw.append([start, len(probs)])
    merged: list[list[int]] = []
    for s, e in raw:
        if merged and (s - merged[-1][1]) * FRAME_MS < min_silence_ms:
            merged[-1][1] = e
        else:
            merged.append([s, e])
    return [
        (round(s * FRAME_MS / 1000, 3), round(e * FRAME_MS / 1000, 3))
        for s, e in merged
        if (e - s) * FRAME_MS >= min_speech_ms
    ]


def vad_row(audio: str, duration: float, segs: Sequence[tuple[float, float]]) -> dict[str, Any]:
    """One utterance's row; segments end no later than the audio (the last frame may run past it)."""
    d = round(duration, 3)
    capped = [[s, min(e, d)] for s, e in segs]
    return {"audio": audio, "durationS": d, "speech": capped, "speechEndS": capped[-1][1] if capped else None}


def load_model(repo: str, revision: str) -> Any:
    """The VAD model at its pinned revision from the Hugging Face cache (a read-only cache works), else downloaded."""
    import nemo.collections.asr as nemo_asr  # from the NeMo Speech image

    from cadence_nemo.checkpoint import download_base

    filename = repo.rsplit("/", 1)[-1].lower() + ".nemo"
    path = download_base({"hfRepo": repo, "revision": revision, "checkpointFile": filename})
    # The .nemo's state dict was pickled by an older PyTorch whose tensors the weights-only unpickler refuses; the file
    # is NVIDIA's at a pinned commit (the Hub checks its LFS hash), so it is loaded with full unpickling, for this call
    # only. strict=False: the checkpoint has no "loss.weight" (a class-weight buffer of the training loss, unused here).
    old = os.environ.get(FORCE_FULL_UNPICKLE)
    os.environ[FORCE_FULL_UNPICKLE] = "1"
    try:
        model = nemo_asr.models.EncDecFrameClassificationModel.restore_from(str(path), map_location="cpu", strict=False)
    finally:
        if old is None:
            os.environ.pop(FORCE_FULL_UNPICKLE, None)
        else:
            os.environ[FORCE_FULL_UNPICKLE] = old
    model.eval()
    return model


def speech_probs(model: Any, audio: Path) -> tuple[list[float], float]:
    """Per-frame speech probabilities of one file (mono, 16 kHz) and its duration in seconds."""
    import numpy as np
    import torch

    from cadence_worker import audio as au

    a = au.canonical(au.read(audio), SAMPLE_RATE)
    x = torch.as_tensor(np.asarray(a.samples, dtype=np.float32))
    with torch.inference_mode():
        logits = model(input_signal=x.unsqueeze(0), input_signal_length=torch.tensor([x.numel()]))
    probs = torch.softmax(logits, dim=-1)[0, :, 1].tolist()
    return [float(p) for p in probs], a.duration


class FrameVadStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"vad": "vad"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "memoryGb": 2, "jobKind": "eval"}
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = VadParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = VadParams.model_validate(params.model_dump())
        if "data" not in inputs:
            raise StepInputError("frame_vad needs its data input (a dataset artifact)")
        if p.offset > p.onset:
            raise StepInputError(f"vad offset {p.offset} is above the onset {p.onset}")
        part = read_dataset_dir(inputs["data"])
        model = load_model(p.model, p.revision)
        header = {
            "kind": KIND,
            "model": p.model,
            "revision": p.revision,
            "frameMs": FRAME_MS,
            "onset": p.onset,
            "offset": p.offset,
            "minSpeechMs": p.min_speech_ms,
            "minSilenceMs": p.min_silence_ms,
        }
        speech = 0
        with outputs["vad"].open("w", encoding="utf-8") as f:
            f.write(json.dumps({"vad": header}, separators=(",", ":")) + "\n")
            for i, c in enumerate(part.clips):
                probs, duration = speech_probs(model, c.audio)
                segs = segments(probs, p.onset, p.offset, p.min_speech_ms, p.min_silence_ms)
                speech += bool(segs)
                f.write(json.dumps(vad_row(hash_file(c.audio), duration, segs), separators=(",", ":")) + "\n")
                if (i + 1) % 50 == 0:
                    ctx.progress((i + 1) / len(part.clips), f"{i + 1}/{len(part.clips)} utterances")
        ctx.set_meta("vad", {**header, "utterances": len(part.clips), "withSpeech": speech})
        ctx.progress(1.0, f"{speech} of {len(part.clips)} utterances have speech")
