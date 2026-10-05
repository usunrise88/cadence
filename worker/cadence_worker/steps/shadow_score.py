"""``shadow_score@1`` — the runtime-neutral judge of a night's shadow replay (phase 5 · D4; docs/spec/03-pipelines-
defaults.md "Export, parity and benchmark (phase 5)", Shadow replay).

The shadow-replay pipeline indexes a night's calls from the deployment's calls mount (``sdp_ingest``), cuts the
callers' segments (``segments_cut``) and decodes them twice through the staging server: ``candidate`` (the shadow
deployment's export) and ``current`` (the slot's production version, or the project's baseline before any
production). This step compares the two decodes segment by segment. It consumes the night's ``segments``
(``cadence.segments/1``: each segment's hash, URI, time range and role; ``files.jsonl`` gives each call file's
duration) and both ``hypotheses`` (rows keyed by the segment's audio hash, as ``segments_cut`` names its dataset rows).
It produces ``report``, a ``shadow_report`` directory:

    report.json     {schema: cadence.shadow/1, scorer, normalize, calls, hours, utterances,
                    missing: {candidate, current}, divergence: {wer, ci: [lo, hi], level, samples, words, errors},
                    confidence: {candidate, current}, callList: [{call, duration, segments, wer, errors, words}],
                    worst: [segment rows, most errors first]}
    segments.jsonl  one row per compared segment: {audio, uri, call, start, end, duration, wer, errors, words,
                    candidate, current, candidateConfidence?, currentConfidence?}

``divergence.wer`` is the word error rate of the candidate's text with the current model's as the reference, over every
segment both decoded, after ``normalize`` (``basic``: NFC, case folded, punctuation stripped, whitespace collapsed).
Its interval is a percentile bootstrap that resamples whole calls (R54: segments of one call are not independent).
A call counts once toward ``hours``, by its file's duration (``files.jsonl``, else its last segment's end), when at
least one of its segments was compared.

The texts are derived from production audio: the control plane keeps the report under the captured-sample retention
class (``deploy.shadow_artifact_retention_days``) and no LLM judge reads it before PII redaction (R28, R29). Help:
docs/help/steps/shadow-score.md.
"""

from __future__ import annotations

import json
import math
import unicodedata
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar, Literal

import numpy as np
from pydantic import BaseModel

from cadence_worker import segments as seg
from cadence_worker.align import align
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import read_hypotheses

SCHEMA = "cadence.shadow/1"
SCORER = "shadow_score@1"


class ShadowScoreParams(BaseModel):
    worst_segments: int = cadence_field(default_ref="deploy.shadow_worst_segments")
    bootstrap_samples: int = cadence_field(
        default_ref="eval.bootstrap_samples",
        description="Bootstrap resamples (by call) behind the divergence's interval",
    )
    confidence: float = cadence_field(
        default_ref="eval.confidence", description="Coverage of the divergence's interval"
    )
    seed: int = cadence_field(
        default_ref="eval.bootstrap_seed",
        description="Seed of the bootstrap resampling, so a night's report is reproducible",
    )
    normalize: Literal["basic", "none"] = cadence_field(
        "basic",
        description=(
            "basic compares words after NFC, case folding and stripping punctuation (the two models may differ only in"
            " casing or punctuation, which is not divergence); none compares the texts as written"
        ),
        source="Cadence recommendation",
        range={"values": ["basic", "none"]},
    )


def normalise(text: str, mode: str) -> list[str]:
    if mode == "none":
        return text.split()
    t = unicodedata.normalize("NFC", text).casefold()
    t = "".join(" " if unicodedata.category(ch).startswith("P") else ch for ch in t)
    return t.split()


def call_of(row: Mapping[str, Any]) -> str:
    """The call a segment belongs to: its source file's mount URI without fragment."""
    f = row.get("file")
    if isinstance(f, str) and f:
        return f
    return str(row.get("uri") or "").split("#", 1)[0]


def confidence_of(row: Mapping[str, Any] | None) -> float | None:
    if row is None:
        return None
    c = row.get("confidence")
    if isinstance(c, int | float) and not isinstance(c, bool) and math.isfinite(float(c)):
        return float(c)
    return None


def rate(errors: float, words: float) -> float:
    if words > 0:
        return errors / words
    return 1.0 if errors > 0 else 0.0


def file_durations(root: Path) -> dict[str, float]:
    f = root / seg.FILES
    out: dict[str, float] = {}
    if not f.is_file():
        return out
    for line in f.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if isinstance(row, dict) and isinstance(row.get("uri"), str) and isinstance(row.get("duration"), int | float):
            out[row["uri"]] = float(row["duration"])
    return out


@dataclass
class Call:
    call: str
    duration: float = 0.0
    segments: int = 0
    errors: int = 0
    words: int = 0
    rows: list[dict[str, Any]] = field(default_factory=list)


def bootstrap(calls: Sequence[Call], samples: int, level: float, seed: int) -> list[float]:
    """Percentile interval of the pooled error rate, resampling calls with replacement."""
    if not calls:
        return [0.0, 0.0]
    errs = np.array([c.errors for c in calls], dtype=np.float64)
    words = np.array([c.words for c in calls], dtype=np.float64)
    rng = np.random.default_rng(seed)
    idx = rng.integers(0, len(calls), size=(samples, len(calls)))
    e, w = errs[idx].sum(axis=1), words[idx].sum(axis=1)
    rates = np.where(w > 0, e / np.where(w > 0, w, 1.0), np.where(e > 0, 1.0, 0.0))
    alpha = (1.0 - level) / 2.0
    lo, hi = np.quantile(rates, [alpha, 1.0 - alpha])
    return [float(lo), float(hi)]


def compare(
    lines: Sequence[Mapping[str, Any]],
    candidate: Mapping[str, Mapping[str, Any]],
    current: Mapping[str, Mapping[str, Any]],
    durations: Mapping[str, float],
    p: ShadowScoreParams,
) -> dict[str, Any]:
    calls: dict[str, Call] = {}
    missing = {"candidate": 0, "current": 0}
    rows: list[dict[str, Any]] = []
    seen: set[str] = set()
    for line in lines:
        h = line.get("hash")
        if not isinstance(h, str) or h in seen:
            continue
        cand, cur = candidate.get(h), current.get(h)
        if cand is None and cur is None:
            continue  # not cut for decoding (the bot's scripted turns, a human transcript)
        seen.add(h)
        if cand is None or cur is None:
            missing["candidate" if cand is None else "current"] += 1
            continue
        ct, rt = str(cand.get("text") or ""), str(cur.get("text") or "")
        ref, hyp = normalise(rt, p.normalize), normalise(ct, p.normalize)
        errors = align(ref, hyp).errors
        name = call_of(line)
        start, end = float(line.get("start") or 0.0), float(line.get("end") or 0.0)
        row: dict[str, Any] = {
            "audio": h,
            "uri": str(line.get("uri") or ""),
            "call": name,
            "start": start,
            "end": end,
            "duration": float(line.get("duration") or round(end - start, 6)),
            "wer": rate(errors, len(ref)),
            "errors": errors,
            "words": len(ref),
            "candidate": ct,
            "current": rt,
        }
        if (cc := confidence_of(cand)) is not None:
            row["candidateConfidence"] = cc
        if (rc := confidence_of(cur)) is not None:
            row["currentConfidence"] = rc
        rows.append(row)
        c = calls.setdefault(name, Call(name))
        c.segments += 1
        c.errors += errors
        c.words += len(ref)
        c.duration = max(c.duration, end)
    if not rows:
        raise StepInputError(
            f"no segment was decoded by both models (candidate lacks {missing['candidate']}, current lacks "
            f"{missing['current']}); check the serve steps' outputs"
        )
    for c in calls.values():
        if c.call in durations:
            c.duration = durations[c.call]
    ordered = sorted(calls.values(), key=lambda c: c.call)
    errors = sum(c.errors for c in ordered)
    words = sum(c.words for c in ordered)
    cconf = [r["candidateConfidence"] for r in rows if "candidateConfidence" in r]
    rconf = [r["currentConfidence"] for r in rows if "currentConfidence" in r]
    worst = sorted((r for r in rows if r["errors"] > 0), key=lambda r: (-r["errors"], -r["wer"], r["audio"]))
    return {
        "calls": len(ordered),
        "hours": sum(c.duration for c in ordered) / 3600.0,
        "utterances": len(rows),
        "missing": missing,
        "divergence": {
            "wer": rate(errors, words),
            "ci": bootstrap(ordered, p.bootstrap_samples, p.confidence, p.seed),
            "level": p.confidence,
            "samples": p.bootstrap_samples,
            "words": words,
            "errors": errors,
        },
        "confidence": {
            "candidate": sum(cconf) / len(cconf) if cconf else None,
            "current": sum(rconf) / len(rconf) if rconf else None,
        },
        "callList": [
            {
                "call": c.call,
                "duration": c.duration,
                "segments": c.segments,
                "wer": rate(c.errors, c.words),
                "errors": c.errors,
                "words": c.words,
            }
            for c in ordered
        ],
        "worst": worst[: p.worst_segments],
        "rows": rows,
    }


class ShadowScoreStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {
        "segments": "segments",
        "candidate": "hypotheses",
        "current": "hypotheses",
    }
    produces: ClassVar[Mapping[str, str]] = {"report": "shadow_report"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "shadow"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = ShadowScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ShadowScoreParams.model_validate(params.model_dump())
        for name in ("segments", "candidate", "current"):
            if name not in inputs:
                raise StepInputError(f"shadow_score needs its {name} input")
        _, lines = seg.read(inputs["segments"])
        res = compare(
            lines,
            read_hypotheses(inputs["candidate"]),
            read_hypotheses(inputs["current"]),
            file_durations(inputs["segments"]),
            p,
        )
        rows = res.pop("rows")
        out = outputs["report"]
        out.mkdir(parents=True, exist_ok=True)
        report = {"schema": SCHEMA, "scorer": SCORER, "normalize": p.normalize, **res}
        (out / "report.json").write_text(
            json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        with (out / "segments.jsonl").open("w", encoding="utf-8") as f:
            for r in rows:
                f.write(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n")
        div = res["divergence"]
        ctx.final_metric("divergence_wer", div["wer"])
        ctx.final_metric("shadow_hours", res["hours"])
        ctx.set_meta(
            "report",
            {
                "schema": SCHEMA,
                "calls": res["calls"],
                "hours": res["hours"],
                "utterances": res["utterances"],
                "divergence": div["wer"],
                "ci": div["ci"],
            },
        )
        ctx.progress(
            1.0,
            f"shadow: {res['calls']} calls, {res['hours']:.2f} h, {res['utterances']} segments, divergence "
            f"{div['wer'] * 100:.2f} % WER [{div['ci'][0] * 100:.2f}, {div['ci'][1] * 100:.2f}]",
        )
