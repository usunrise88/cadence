"""``latency_score@1`` — latency to final at real-time pace (R54; docs/spec/03-pipelines-defaults.md "Scorers and
metrics"; phase 3 stream R): the time from an utterance's end to the final that covers it, p50 and p95, from the
partial events of the ``hypotheses`` artifact (R42) and the utterance ends of a ``vad`` artifact (a frame-VAD step).

Consumes ``hypotheses`` (a streaming decode with partial events: ``audioOffsetMs``, ``emitMs``, ``text``), the
``dataset`` they decode and ``vad`` (JSON lines ``{audio, durationS, speech: [[start, end], …], speechEndS}``).
Produces ``scores``, a ``metric_scores`` artifact (:mod:`cadence_worker.metric_scores`) with metric ``latency``:

    summary   pace, utteranceEnd: vad, vad: {kind, model, revision}, utterances, measured, p50Ms, p95Ms, meanMs, maxMs,
              earlyFinals, noSpeech, emptyFinals, profile
    rows      {index, audio, speechEndMs, finalPartial, finalAtMs, latencyMs}

Per utterance: the *final* is the first partial from which the text no longer changes (it equals the final text);
latency = the time that partial is emitted - the speech end the VAD found. A final emitted before the VAD's end (the
VAD's hangover after the last word) counts as 0 and is counted in ``earlyFinals``.

Emit times at real-time pace. A decode run at real-time pace (``decoding.pace == "realtime"``) emits each partial at
``emitMs`` after its stream started, used as is. A file decode runs as fast as the card allows (``emitMs`` is the wall
time since the batch's decode started, the batch's streams decoded together); its emit times at real-time pace are
simulated from its own compute times: chunk k's audio is available at ``audioOffsetMs[k]`` and takes
``emitMs[k] - emitMs[k-1]`` to decode, so ``emit[k] = max(audioOffsetMs[k], emit[k-1]) + (emitMs[k] - emitMs[k-1])``
(a decoder slower than real time queues). ``pace`` in the summary says which (``realtime`` | ``simulated``).
Help: docs/help/steps/latency-score.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import metric_scores
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import read_hypotheses, read_references

SCORER = "latency_score@1"
METRIC = "latency"


class LatencyScoreParams(BaseModel):
    """No parameters: the partial events and the VAD carry everything."""


def read_vad(path: Path) -> tuple[dict[str, dict[str, Any]], dict[str, Any]]:
    """VAD rows by audio hash, and the header line (``{"vad": {kind, model, revision}}``) when present."""
    rows: dict[str, dict[str, Any]] = {}
    header: dict[str, Any] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"cannot read the vad input: {e}") from e
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"vad line {n} is not JSON") from e
        if isinstance(row, dict) and isinstance(row.get("vad"), dict) and "audio" not in row:
            header = row["vad"]
            continue
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str):
            raise StepInputError(f"vad line {n} lacks audio")
        rows[row["audio"]] = row
    return rows, header


def paced_emits(partials: Sequence[Mapping[str, Any]], realtime: bool) -> list[float]:
    """The emit time of each partial at real-time pace, in ms from the stream's start."""
    out: list[float] = []
    prev_wall = 0.0
    prev_emit = 0.0
    for p in partials:
        wall = float(p.get("emitMs") or 0.0)
        if realtime:
            out.append(wall)
            continue
        offset = float(p.get("audioOffsetMs") or 0.0)
        compute = max(0.0, wall - prev_wall)
        emit = max(offset, prev_emit) + compute
        out.append(emit)
        prev_wall, prev_emit = wall, emit
    return out


def final_index(partials: Sequence[Mapping[str, Any]], final: str) -> int:
    """The first partial from which the text equals the final text and stays so."""
    want = final.split()
    k = len(partials)
    while k > 0 and str(partials[k - 1].get("text") or "").split() == want:
        k -= 1
    return min(k, len(partials) - 1)


def score(
    refs: Sequence[Any], hyps: Mapping[str, Mapping[str, Any]], vad: Mapping[str, Mapping[str, Any]]
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    rows: list[dict[str, Any]] = []
    latencies: list[float] = []
    early = no_speech = empty = no_partials = 0
    paces: set[str] = set()
    profile = None
    for i, r in enumerate(refs):
        h = hyps.get(r.audio)
        if h is None:
            raise StepInputError(f"utterance {i} ({r.audio}) has no hypothesis")
        raw = h.get("decoding")
        decoding: Mapping[str, Any] = raw if isinstance(raw, dict) else {}
        profile = profile or decoding.get("profile")
        partials = h.get("partials")
        if not isinstance(partials, list) or not partials:
            no_partials += 1
            continue
        v = vad.get(r.audio)
        if v is None:
            raise StepInputError(f"utterance {i} ({r.audio}) has no VAD row; run the VAD on the same dataset")
        end_s = v.get("speechEndS")
        if end_s is None:
            no_speech += 1
            continue
        final = str(h.get("text") or "")
        if not final.split():
            empty += 1
            continue
        realtime = decoding.get("pace") == "realtime"
        paces.add("realtime" if realtime else "simulated")
        emits = paced_emits(partials, realtime)
        k = final_index(partials, final)
        end_ms = float(end_s) * 1000
        lat = emits[k] - end_ms
        if lat < 0:
            early += 1
            lat = 0.0
        latencies.append(lat)
        rows.append(
            {
                "index": i,
                "audio": r.audio,
                "speechEndMs": round(end_ms, 1),
                "finalPartial": k,
                "finalAtMs": round(emits[k], 1),
                "latencyMs": round(lat, 1),
            }
        )
    if no_partials == len(refs):
        raise StepInputError("the hypotheses carry no partial events: latency to final needs a streaming decode")

    def r1(v: float | None) -> float | None:
        return None if v is None else round(v, 1)

    summary: dict[str, Any] = {
        "schema": metric_scores.SCHEMA,
        "scorer": SCORER,
        "metric": METRIC,
        "available": bool(latencies),
        "pace": "mixed" if len(paces) > 1 else (next(iter(paces)) if paces else "simulated"),
        "utteranceEnd": "vad",
        "profile": profile,
        "utterances": len(refs),
        "measured": len(latencies),
        "p50Ms": r1(metric_scores.percentile(latencies, 50)),
        "p95Ms": r1(metric_scores.percentile(latencies, 95)),
        "meanMs": r1(sum(latencies) / len(latencies)) if latencies else None,
        "maxMs": r1(max(latencies)) if latencies else None,
        "earlyFinals": early,
        "noSpeech": no_speech,
        "emptyFinals": empty,
        "noPartials": no_partials,
    }
    if not latencies:
        summary["reason"] = "no utterance had speech (VAD) and a non-empty final"
    return summary, rows


class LatencyScoreStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "data": "dataset", "vad": "vad"}
    produces: ClassVar[Mapping[str, str]] = {"scores": "metric_scores"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = LatencyScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        for name in self.consumes:
            if name not in inputs:
                raise StepInputError(f"latency_score needs its {name} input")
        refs = read_references(inputs["data"])
        hyps = read_hypotheses(inputs["hypotheses"])
        vad, header = read_vad(inputs["vad"])
        summary, rows = score(refs, hyps, vad)
        if header:
            summary["vad"] = header
        metric_scores.write(outputs["scores"], summary, rows)
        for name, key in (("latency_to_final_p50_ms", "p50Ms"), ("latency_to_final_p95_ms", "p95Ms")):
            if summary[key] is not None:
                ctx.final_metric(name, summary[key])
        ctx.set_meta(
            "scores",
            {k: summary[k] for k in ("schema", "scorer", "metric", "available", "pace", "measured", "p50Ms", "p95Ms")},
        )
        ctx.progress(1.0, f"latency to final p50 {summary['p50Ms']} ms, p95 {summary['p95Ms']} ms")
