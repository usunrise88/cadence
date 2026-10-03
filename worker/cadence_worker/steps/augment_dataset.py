"""``augment_dataset@1`` — the robustness axis of an eval (docs/spec/03-pipelines-defaults.md "Augmentation"; phase 3
stream R): apply an augmentation profile to every utterance of a golden dataset, deterministically, and write a new
``dataset`` artifact the eval's transcribe and score steps read in place of the golden one.

Consumes ``data`` (a ``dataset`` artifact), ``profile`` (an ``augment_profile`` artifact the control plane renders
from the project's ``augment/<name>.yaml`` at a commit, :mod:`cadence_worker.augment`) and, optionally, ``noise`` (a
noise bank: a ``dataset`` artifact of purpose ``noise``) when the profile has a ``noise`` transform. Produces ``data``:

    dataset.json      the golden header with ``purpose: augmented`` (never registered as a dataset version: the
                      control plane's dataset hook skips it), recomputed hours and
                      ``augmentation: {kind, profile, seed, hash, applied: {stage: utterances}, unavailable: [codec]}``
    manifest.jsonl    the same rows in the same order (text, language, speaker, call ids kept), each with its new
                      ``audio`` file, ``duration``, ``augment`` (the stages applied) and ``augmentedFrom`` (the golden
                      audio's hash, so a row can be traced back to its utterance)
    audio/            canonical 16-bit PCM WAV, mono, at each utterance's own sample rate

The draw of each utterance is seeded by the profile's seed and the golden audio's hash (order-independent). Runs on
the CPU in every runtime. Help: docs/help/steps/augment-dataset.md.
"""

from __future__ import annotations

import hashlib
import json
from collections import Counter
from collections.abc import Mapping
from pathlib import Path, PurePosixPath
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import audio
from cadence_worker import augment as aug
from cadence_worker.cas import hash_bytes, hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext

KIND = "augment_dataset@1"
DATASET_FORMAT = "cadence.dataset/1"
PURPOSE = "augmented"


class AugmentDatasetParams(BaseModel):
    """No parameters: the profile artifact carries the transforms and the seed."""


def _manifest(root: Path, what: str) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    if not root.is_dir() or not (root / "manifest.jsonl").is_file():
        raise StepInputError(f"the {what} input is not a dataset artifact (dataset.json, manifest.jsonl, audio/)")
    try:
        header = json.loads((root / "dataset.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the {what} has no readable dataset.json: {e}") from e
    if not isinstance(header, dict) or header.get("format") != DATASET_FORMAT:
        raise StepInputError(f"the {what}'s dataset.json is not {DATASET_FORMAT}")
    rows: list[dict[str, Any]] = []
    for n, line in enumerate((root / "manifest.jsonl").read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"{what} manifest line {n} is not JSON") from e
        rel = row.get("audio") if isinstance(row, dict) else None
        if not isinstance(rel, str):
            raise StepInputError(f"{what} manifest line {n} has no audio")
        p = PurePosixPath(rel)
        if not p.parts or p.is_absolute() or ".." in p.parts or not root.joinpath(*p.parts).is_file():
            raise StepInputError(f"{what} manifest line {n}: audio {rel!r} is not a file inside the artifact")
        rows.append(row)
    return header, rows


def _samples(path: Path, rate: int | None = None) -> tuple[aug.Audio, int]:
    import numpy as np

    try:
        a = audio.read(path)
    except audio.AudioError as e:
        raise StepInputError(f"{path.name}: {e}") from e
    a = audio.to_mono(a)
    if rate is not None and a.sample_rate != rate:
        a = audio.resample(a, rate)
    return np.asarray(a.samples, dtype=np.float64), a.sample_rate


class AugmentDatasetStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"data": "dataset", "profile": "augment_profile", "noise": "dataset"}
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"noise"})
    produces: ClassVar[Mapping[str, str]] = {"data": "dataset"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "memoryGb": 2, "diskGb": 4, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = AugmentDatasetParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        import numpy as np

        for name in ("data", "profile"):
            if name not in inputs:
                raise StepInputError(f"augment_dataset needs its {name} input")
        try:
            profile = aug.parse_profile(json.loads(inputs["profile"].read_text(encoding="utf-8")))
        except (OSError, ValueError) as e:
            raise StepInputError(str(e)) from e
        header, rows = _manifest(inputs["data"], "data")
        if header.get("purpose") == "noise":
            raise StepInputError("the data input is a noise bank; augment a dataset of utterances")
        noise_t = profile.transforms.noise
        if noise_t is not None and noise_t.probability > 0 and "noise" not in inputs:
            raise StepInputError("the profile mixes noise but no noise bank is wired to the noise input")
        bank_rows: list[dict[str, Any]] = []
        if "noise" in inputs and noise_t is not None:
            _, bank_rows = _manifest(inputs["noise"], "noise bank")
        if profile.unavailable:
            ctx.log(
                f"codecs left out of the draw (not implemented by {KIND}): {', '.join(profile.unavailable)}",
                level="warn",
            )
        out = outputs["data"]
        (out / "audio").mkdir(parents=True, exist_ok=True)
        bank_cache: dict[int, list[aug.Audio]] = {}
        applied: Counter[str] = Counter()
        lines: list[dict[str, Any]] = []
        seconds = 0.0
        for i, row in enumerate(rows):
            src = inputs["data"].joinpath(*PurePosixPath(row["audio"]).parts)
            golden = hash_file(src)
            x, rate = _samples(src)
            bank: list[aug.Audio] = []
            if bank_rows:
                if rate not in bank_cache:
                    bank_cache[rate] = [
                        _samples(inputs["noise"].joinpath(*PurePosixPath(r["audio"]).parts), rate)[0] for r in bank_rows
                    ]
                bank = bank_cache[rate]
            y, stages = aug.apply(x, rate, profile, aug.utterance_rng(profile.seed, golden), bank)
            body = audio.wav_bytes(audio.Audio(samples=np.asarray(y), sample_rate=rate, channels=1))
            h = hashlib.sha256(body).hexdigest()
            rel = f"audio/{h[:2]}/{h}.wav"
            (out / rel).parent.mkdir(parents=True, exist_ok=True)
            (out / rel).write_bytes(body)
            duration = y.size / rate
            seconds += duration
            applied.update(s.split(":", 1)[0] for s in stages)
            lines.append({**row, "audio": rel, "duration": duration, "augment": stages, "augmentedFrom": golden})
            if (i + 1) % 50 == 0:
                ctx.progress((i + 1) / len(rows), f"{i + 1}/{len(rows)} utterances")
        phash = profile.hash
        new_header = {
            **header,
            "purpose": PURPOSE,
            "hours": seconds / 3600,
            "augmentation": {
                "kind": KIND,
                "profile": profile.name,
                "seed": profile.seed,
                "hash": phash,
                "applied": dict(sorted(applied.items())),
                "unavailable": profile.unavailable,
                "noiseBank": noise_t.bank if noise_t else "",
                "noiseClips": len(bank_rows),
            },
        }
        with (out / "manifest.jsonl").open("w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line, ensure_ascii=False, sort_keys=True) + "\n")
        (out / "dataset.json").write_text(
            json.dumps(new_header, ensure_ascii=False, indent=2, sort_keys=True) + "\n", "utf-8"
        )
        ctx.set_meta(
            "data",
            {
                "purpose": PURPOSE,
                "utterances": len(lines),
                "hours": round(seconds / 3600, 6),
                "profile": profile.name,
                "profileHash": phash,
                "seed": profile.seed,
                "applied": dict(sorted(applied.items())),
                "unavailable": profile.unavailable,
                "profileArtifact": hash_bytes(inputs["profile"].read_bytes()),
            },
        )
        ctx.progress(1.0, f"{len(lines)} utterances augmented with {profile.name}")
