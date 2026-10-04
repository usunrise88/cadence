"""``latency_score@3`` — latency to final at real-time pace and emission delay (R54; docs/spec/03-pipelines-defaults.md
"Scorers and metrics"; phase 3 stream R, phase 4 stream L): the time from an utterance's end to the final that covers
it, p50 and p95, from the partial events of the ``hypotheses`` artifact (R42) and the utterance ends of a ``vad``
artifact (a frame-VAD step); and the time from each reference word's aligned end to its first appearance in a
partial, PR50 and PR90 (Yu et al., FastEmit, ICASSP 2021), from an ``alignment`` of the references
(``align_reference``).

Consumes ``hypotheses`` (a streaming decode with partial events: ``audioOffsetMs``, ``emitMs``, ``text``), the
``dataset`` they decode and ``vad`` (JSON lines ``{audio, durationS, speech: [[start, end], …], speechEndS}``);
optionally ``normalizer`` (the golden set's scoring normalizer) and ``alignment`` (word timings of the references,
:mod:`cadence_worker.reference_alignment`), which emission delay needs. Produces ``scores``, a ``metric_scores``
artifact (:mod:`cadence_worker.metric_scores`) with metric ``latency``:

    summary   pace, utteranceEnd: vad, vad: {kind, model, revision}, utterances, measured, p50Ms, p95Ms, meanMs, maxMs,
              earlyFinals, noSpeech, emptyFinals, profile,
              emission: {available, reason?, aligner?, utterances, alignedUtterances, words, matchedWords,
                         pr50Ms, pr90Ms, meanMs, earlyWords}
    rows      {index, audio, speechEndMs, finalPartial, finalAtMs, latencyMs, emission?: {words, pr50Ms, pr90Ms}}

Emission delay (version 3). Reference and hypothesis words are compared after the scoring normalizer, each reference
word carrying the aligned end of the whitespace token it came from. A reference word the final hypothesis matches
(an ``=`` of the word alignment, as in WER) is *emitted* by the first partial from which the final's word at that
position stays in place to the end; its delay is that partial's emit time at real-time pace (as for latency to final)
minus the word's aligned end. A word emitted before its aligned end has a negative delay, kept as is and counted in
``earlyWords``; substituted, deleted and inserted words have no delay. PR50 and PR90 are the 50th and 90th
percentiles over every matched word of the golden set. Without an alignment, a normalizer, or aligned references
(the aligner does not cover the language), emission delay is unavailable with the reason — never estimated.

Per utterance: the *final* is the first partial from which the text no longer changes (it equals the final text);
latency = the time that partial is emitted - the speech end the VAD found. A final emitted before the VAD's end (the
VAD's hangover after the last word) counts as 0 and is counted in ``earlyFinals``.

Emit times at real-time pace. A decode run at real-time pace (``decoding.pace == "realtime"``) emits each partial at
``emitMs`` after its stream started, used as is. A file decode runs as fast as the card allows; its emit times at
real-time pace are simulated from its own compute (:func:`paced_emits`): from the row's ``steps`` — every chunk the
stream stepped, with the audio it made available and its compute (the batch step's wall time over the streams it
stepped, so independent of the batch size) — when the partials name their chunk (``step``); else, for hypotheses
written before them, from the partials' ``emitMs`` (version 1's method, which charged a partial the silent chunks
before it and the batch's other streams). A decoder slower than real time queues. ``pace`` in the summary says
``realtime`` | ``simulated``, ``timing`` what a simulation used (``steps`` | ``events``).
Help: docs/help/steps/latency-score.md.
"""

from __future__ import annotations

import json
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import metric_scores
from cadence_worker import reference_alignment as ra
from cadence_worker.align import align
from cadence_worker.normalize import Normalizer, NormalizerError
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import Reference, read_hypotheses, read_references

SCORER = "latency_score@3"
METRIC = "latency"


class LatencyScoreParams(BaseModel):
    """No parameters: the partial events, the VAD and the alignment carry everything."""


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


def chunk_steps(raw: Any, partials: Sequence[Mapping[str, Any]]) -> list[tuple[float, float]] | None:
    """A hypotheses row's ``steps`` ([audio available ms, compute ms] per chunk) when every partial names its chunk
    (``step``); None when the row predates them or they do not fit."""
    if not isinstance(raw, list) or not raw:
        return None
    try:
        steps = [(float(s[0]), max(0.0, float(s[1]))) for s in raw]
    except (TypeError, ValueError, IndexError):
        return None
    for p in partials:
        k = p.get("step")
        if not isinstance(k, int) or isinstance(k, bool) or not 0 <= k < len(steps):
            return None
    return steps


def paced_emits(
    partials: Sequence[Mapping[str, Any]], realtime: bool, steps: Sequence[tuple[float, float]] | None = None
) -> list[float]:
    """The emit time of each partial at real-time pace, in ms from the stream's start.

    With the decode's chunk steps (every chunk the stream stepped, silent ones included), chunk k's audio is available
    at ``steps[k][0]`` and takes ``steps[k][1]`` to decode; a chunk waits for the one before it, so
    ``done[k] = max(available[k], done[k-1]) + compute[k]``, and a partial is emitted when the chunk that produced it
    is done. Without them (hypotheses of an older decode), the partials' own ``emitMs`` stand in: the wall time between
    two partials is the second one's compute, which also charges it the silent chunks between them and, in a batched
    decode, the other streams' compute (an overstatement)."""
    if realtime:
        return [float(p.get("emitMs") or 0.0) for p in partials]
    if steps is not None:
        done: list[float] = []
        prev = 0.0
        for available, compute in steps:
            prev = max(available, prev) + compute
            done.append(prev)
        return [done[int(p["step"])] for p in partials]
    out: list[float] = []
    prev_wall = 0.0
    prev_emit = 0.0
    for p in partials:
        wall = float(p.get("emitMs") or 0.0)
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
    timings: set[str] = set()
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
        steps = None if realtime else chunk_steps(h.get("steps"), partials)
        if not realtime:
            timings.add("steps" if steps is not None else "events")
        emits = paced_emits(partials, realtime, steps)
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

    def r1(v: float | None) -> float | None:
        return None if v is None else round(v, 1)

    summary: dict[str, Any] = {
        "schema": metric_scores.SCHEMA,
        "scorer": SCORER,
        "metric": METRIC,
        "available": bool(latencies),
        "pace": "mixed" if len(paces) > 1 else (next(iter(paces)) if paces else "simulated"),
        # what a simulated pace was computed from: the decode's chunk steps, or (older hypotheses) its partials' times
        "timing": "mixed" if len(timings) > 1 else (next(iter(timings)) if timings else None),
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
    if no_partials == len(refs):
        summary["reason"] = "the hypotheses carry no partial events: latency to final needs a streaming decode"
    elif not latencies:
        summary["reason"] = "no utterance had speech (VAD) and a non-empty final"
    return summary, rows


def reference_word_ends(text: str, row: Mapping[str, Any], norm: Normalizer) -> list[tuple[str, float | None]] | None:
    """The normalized reference words with the aligned end (seconds) of the whitespace token each came from (None for
    a token the aligner did not time); None when normalizing token by token does not give the words the whole text
    gives (a normalizer mapping that spans tokens), since then no word can be tied to a token."""
    times = ra.timed_words(row)
    out: list[tuple[str, float | None]] = []
    for i, token in enumerate(text.split()):
        t = times.get(i)
        out.extend((w, t[1] if t else None) for w in norm.words(token))
    if [w for w, _ in out] != norm.words(text):
        return None
    return out


def first_stable(partial_words: Sequence[Sequence[str]], j: int, word: str) -> int:
    """The first partial from which word ``j`` is ``word`` and stays so to the last partial (the last one when no
    partial keeps it: the final emits it)."""
    k = len(partial_words)
    while k > 0 and len(partial_words[k - 1]) > j and partial_words[k - 1][j] == word:
        k -= 1
    return min(k, len(partial_words) - 1)


def emission(
    refs: Sequence[Reference],
    hyps: Mapping[str, Mapping[str, Any]],
    norm: Normalizer | None,
    alignment: tuple[dict[str, Any], dict[str, dict[str, Any]]] | None,
) -> tuple[dict[str, Any], dict[int, dict[str, Any]]]:
    """Emission delay over the golden set (the module docstring): the summary and, by dataset index, each measured
    utterance's own numbers."""
    out: dict[str, Any] = {"available": False, "utterances": len(refs), "alignedUtterances": 0, "words": 0}
    if alignment is None:
        out["reason"] = "the golden set has no aligned references (run align_reference on its dataset)"
        return out, {}
    if norm is None:
        out["reason"] = "no scoring normalizer was wired to compare words"
        return out, {}
    header, rows = alignment
    aligner = header.get("aligner")
    if isinstance(aligner, dict):
        out["aligner"] = aligner
    delays: list[float] = []
    per: dict[int, dict[str, Any]] = {}
    matched = early = mismatched = unaligned = no_partials = 0
    for i, r in enumerate(refs):
        a = rows.get(r.audio)
        if a is None or a.get("aligned") is not True:
            unaligned += 1
            continue
        words = reference_word_ends(r.text, a, norm) if a.get("text") == r.text else None
        if words is None:
            mismatched += 1  # aligned for another reference text, or not tied to tokens by the normalizer
            continue
        out["alignedUtterances"] += 1
        out["words"] += len(words)
        h = hyps.get(r.audio)
        partials = h.get("partials") if h else None
        if h is None or not isinstance(partials, list) or not partials:
            no_partials += 1
            continue
        raw = h.get("decoding")
        realtime = isinstance(raw, dict) and raw.get("pace") == "realtime"
        emits = paced_emits(partials, realtime, None if realtime else chunk_steps(h.get("steps"), partials))
        final = norm.words(str(h.get("text") or ""))
        pw = [norm.words(str(p.get("text") or "")) for p in partials]
        mine: list[float] = []
        ri = hj = 0
        for op, _, _ in align([w for w, _ in words], final).ops:
            end = words[ri][1] if op == "=" else None
            if end is not None:
                mine.append(emits[first_stable(pw, hj, final[hj])] - end * 1000)
            if op in ("=", "S", "D"):
                ri += 1
            if op in ("=", "S", "I"):
                hj += 1
        matched += len(mine)
        early += sum(1 for d in mine if d < 0)
        delays.extend(mine)
        if mine:
            per[i] = {
                "words": len(mine),
                "pr50Ms": round(metric_scores.percentile(mine, 50) or 0.0, 1),
                "pr90Ms": round(metric_scores.percentile(mine, 90) or 0.0, 1),
            }

    def r1(v: float | None) -> float | None:
        return None if v is None else round(v, 1)

    out.update(
        {
            "available": bool(delays),
            "matchedWords": matched,
            "pr50Ms": r1(metric_scores.percentile(delays, 50)),
            "pr90Ms": r1(metric_scores.percentile(delays, 90)),
            "meanMs": r1(sum(delays) / len(delays)) if delays else None,
            "earlyWords": early,
            "unalignedUtterances": unaligned,
            "mismatchedUtterances": mismatched,
        }
    )
    if not delays:
        if out["alignedUtterances"] == 0:
            why = sorted({str(a["reason"]) for a in rows.values() if a.get("aligned") is not True and a.get("reason")})
            out["reason"] = "no reference of the golden set is aligned" + (f" ({'; '.join(why[:3])})" if why else "")
        elif no_partials == out["alignedUtterances"]:
            out["reason"] = "the hypotheses carry no partial events: emission delay needs a streaming decode"
        else:
            out["reason"] = "no aligned reference word was matched by the final hypothesis"
    return out, per


class LatencyScoreStep:
    version: ClassVar[str] = "3"
    consumes: ClassVar[Mapping[str, str]] = {
        "hypotheses": "hypotheses",
        "data": "dataset",
        "vad": "vad",
        "normalizer": "normalizer",
        "alignment": "alignment",
    }
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"normalizer", "alignment"})
    produces: ClassVar[Mapping[str, str]] = {"scores": "metric_scores"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = LatencyScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        for name in self.consumes:
            if name not in inputs and name not in self.optional_inputs:
                raise StepInputError(f"latency_score needs its {name} input")
        refs = read_references(inputs["data"])
        hyps = read_hypotheses(inputs["hypotheses"])
        vad, header = read_vad(inputs["vad"])
        norm = None
        if "normalizer" in inputs:
            try:
                norm = Normalizer.from_file(inputs["normalizer"])
            except NormalizerError as e:
                raise StepInputError(str(e)) from e
        alignment = ra.read(inputs["alignment"]) if "alignment" in inputs else None
        summary, rows = score(refs, hyps, vad)
        if header:
            summary["vad"] = header
        em, per = emission(refs, hyps, norm, alignment)
        summary["emission"] = em
        by_index = {row["index"]: row for row in rows}
        for i, numbers in per.items():
            by_index.setdefault(i, {"index": i, "audio": refs[i].audio})["emission"] = numbers
        rows = [by_index[i] for i in sorted(by_index)]
        metric_scores.write(outputs["scores"], summary, rows)
        for name, key in (("latency_to_final_p50_ms", "p50Ms"), ("latency_to_final_p95_ms", "p95Ms")):
            if summary[key] is not None:
                ctx.final_metric(name, summary[key])
        for name, key in (("emission_delay_pr50_ms", "pr50Ms"), ("emission_delay_pr90_ms", "pr90Ms")):
            if em.get(key) is not None:
                ctx.final_metric(name, em[key])
        meta = {
            k: summary[k] for k in ("schema", "scorer", "metric", "available", "pace", "measured", "p50Ms", "p95Ms")
        }
        meta["emission"] = {k: em.get(k) for k in ("available", "pr50Ms", "pr90Ms", "matchedWords")}
        ctx.set_meta("scores", meta)
        em_text = f"PR50 {em['pr50Ms']} ms, PR90 {em['pr90Ms']} ms" if em["available"] else "n/a"
        ctx.progress(
            1.0, f"latency to final p50 {summary['p50Ms']} ms, p95 {summary['p95Ms']} ms; emission delay {em_text}"
        )
