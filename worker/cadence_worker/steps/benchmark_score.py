"""``benchmark_score@1`` — the runtime-neutral latency judge of phase 5 (R30, R31; docs/spec/03-pipelines-defaults.md
"Export, parity and benchmark (phase 5)"; spike E1, docs/spikes/E1-onnx-triton.md "Triton").

Consumes ``timings`` — one ``serving_timings`` artifact per concurrency level (``timings.0``, ``timings.1``, …: the
family's serve step at real-time pace, ``cadence.serving-timings/1``) — and produces ``report``, a
``benchmark_report`` (``cadence.benchmark/1``, one JSON file):

    {schema, scorer, profile, chunkMs, budgetMs, targetStreams, maxForeignUtilPct,
     levels: [{streams, chunks, finals, chunkLatencyMs: {p50, p95, p99}, timeToFinalMs: {p50, p95, p99}, rtf, errors,
               servingMemoryMb, foreignUtilPct, contended, withinBudget, server?}],
     maxStreamsWithinBudget, contended, cardClass, server: {kind, version},
     verdict: passed|failed|inconclusive, reasons}

A chunk's latency runs from the moment its audio was complete (``availableMs``) to its result (``doneMs``); the last
chunk of an utterance gives its time to final. Rows before the level's warm-up (``warmupMs``) are not counted. RTF is
the mean chunk latency over the chunk length (the share of real time a stream waits for the server). A level is
within the budget when its p95 chunk latency is at most ``budget_ms`` and no stream failed; it is contended when
processes outside Cadence's server used more than ``max_foreign_util_pct`` of the card on average.
``maxStreamsWithinBudget`` (streams per card) is the largest level whose every lower level is within the budget too.
The verdict is taken at ``target_streams``: contended → inconclusive, within the budget → passed, else failed; a run
without that level is inconclusive. Help: docs/help/steps/benchmark-score.md.
"""

from __future__ import annotations

import json
import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

SCHEMA = "cadence.benchmark/1"
TIMINGS_SCHEMA = "cadence.serving-timings/1"
SCORER = "benchmark_score@1"


class BenchmarkScoreParams(BaseModel):
    budget_ms: float = cadence_field(default_ref="deploy.latency_budget_over_chunk_ms")
    target_streams: int = cadence_field(default_ref="deploy.target_concurrency")
    max_foreign_util_pct: float = cadence_field(default_ref="deploy.benchmark_max_foreign_util_pct")


@dataclass
class Level:
    header: dict[str, Any]
    latencies: list[float] = field(default_factory=list)
    finals: list[float] = field(default_factory=list)
    errors: int = 0
    foreign: list[float] = field(default_factory=list)
    memory: list[int] = field(default_factory=list)
    server: dict[str, Any] | None = None

    @property
    def streams(self) -> int:
        return int(self.header.get("concurrency") or 0)


def percentiles(values: Sequence[float]) -> dict[str, float | None]:
    """p50, p95 and p99 by linear interpolation between order statistics (numpy's default)."""
    if not values:
        return {"p50": None, "p95": None, "p99": None}
    v = sorted(values)

    def at(q: float) -> float:
        pos = (len(v) - 1) * q
        lo, hi = math.floor(pos), math.ceil(pos)
        return v[lo] + (v[hi] - v[lo]) * (pos - lo)

    return {"p50": round(at(0.50), 3), "p95": round(at(0.95), 3), "p99": round(at(0.99), 3)}


def read_level(path: Path) -> Level:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"cannot read serving timings {path.name}: {e}") from e
    rows: list[dict[str, Any]] = []
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"serving timings {path.name} line {n} is not JSON") from e
        if not isinstance(row, dict):
            raise StepInputError(f"serving timings {path.name} line {n} is not an object")
        rows.append(row)
    if not rows or rows[0].get("schema") != TIMINGS_SCHEMA:
        raise StepInputError(f"serving timings {path.name} does not start with a {TIMINGS_SCHEMA} header")
    lv = Level(header=rows[0])
    warm = float(rows[0].get("warmupMs") or 0.0)
    for row in rows[1:]:
        match row.get("type"):
            case "chunk":
                try:
                    avail, done = float(row["availableMs"]), float(row["doneMs"])
                except (KeyError, TypeError, ValueError) as e:
                    raise StepInputError(f"serving timings {path.name}: a chunk row lacks availableMs or doneMs") from e
                if avail < warm:
                    continue
                lat = max(0.0, done - avail)
                lv.latencies.append(lat)
                if row.get("last") is True:
                    lv.finals.append(lat)
            case "telemetry":
                if isinstance(row.get("foreignUtilPct"), int | float):
                    lv.foreign.append(float(row["foreignUtilPct"]))
                if isinstance(row.get("memoryUsedMb"), int | float):
                    lv.memory.append(int(row["memoryUsedMb"]))
            case "server":
                lv.server = {k: v for k, v in row.items() if k != "type"}
            case "error":
                lv.errors += 1
    if lv.streams < 1:
        raise StepInputError(f"serving timings {path.name}: the header names no concurrency")
    return lv


def level_report(lv: Level, chunk_ms: float, budget_ms: float, max_foreign: float) -> dict[str, Any]:
    lat = percentiles(lv.latencies)
    foreign = round(sum(lv.foreign) / len(lv.foreign), 2) if lv.foreign else None
    p95 = lat["p95"]
    out: dict[str, Any] = {
        "streams": lv.streams,
        "chunks": len(lv.latencies),
        "finals": len(lv.finals),
        "chunkLatencyMs": lat,
        "timeToFinalMs": percentiles(lv.finals),
        "rtf": round(sum(lv.latencies) / len(lv.latencies) / chunk_ms, 4) if lv.latencies and chunk_ms > 0 else None,
        "errors": lv.errors,
        "servingMemoryMb": max(lv.memory) if lv.memory else None,
        "foreignUtilPct": foreign,
        "contended": foreign is not None and foreign > max_foreign,
        "withinBudget": p95 is not None and p95 <= budget_ms and lv.errors == 0,
    }
    if lv.server is not None:
        out["server"] = lv.server
    return out


def judge(levels: Sequence[dict[str, Any]], target: int) -> tuple[int, str, list[str], bool]:
    """Streams per card, the verdict at the target concurrency, its reasons and whether that level was contended."""
    best = 0
    for lv in sorted(levels, key=lambda x: int(x["streams"])):
        if not lv["withinBudget"]:
            break
        best = int(lv["streams"])
    at = next((lv for lv in levels if int(lv["streams"]) == target), None)
    if at is None:
        return best, "inconclusive", [f"no level at the target concurrency {target}"], False
    if at["contended"]:
        return (
            best,
            "inconclusive",
            [f"processes outside Cadence used {at['foreignUtilPct']} % of the card at {target}"],
            True,
        )
    if at["withinBudget"]:
        return best, "passed", [], False
    why = f"p95 chunk latency {at['chunkLatencyMs']['p95']} ms at {target} streams"
    if at["errors"]:
        why += f", {at['errors']} stream error(s)"
    return best, "failed", [why], False


def timing_inputs(inputs: Mapping[str, Path], name: str = "timings") -> list[Path]:
    keyed = [(k, p) for k, p in inputs.items() if k == name or k.startswith(name + ".")]
    return [p for _, p in sorted(keyed, key=lambda kp: int(kp[0].rpartition(".")[2]) if "." in kp[0] else -1)]


class BenchmarkScoreStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"timings": "serving_timings"}
    produces: ClassVar[Mapping[str, str]] = {"report": "benchmark_report"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = BenchmarkScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = BenchmarkScoreParams.model_validate(params.model_dump())
        paths = timing_inputs(inputs)
        if not paths:
            raise StepInputError("benchmark_score needs the serving timings of at least one level (timings.0, …)")
        levels = sorted((read_level(f) for f in paths), key=lambda lv: lv.streams)
        if len({lv.streams for lv in levels}) != len(levels):
            raise StepInputError("two serving timings have the same concurrency")
        first = levels[0].header
        chunk_ms = float(first.get("chunkMs") or 0.0)
        rows = [level_report(lv, chunk_ms, p.budget_ms, p.max_foreign_util_pct) for lv in levels]
        best, result, reasons, contended = judge(rows, p.target_streams)
        server = first.get("server") if isinstance(first.get("server"), dict) else {}
        report: dict[str, Any] = {
            "schema": SCHEMA,
            "scorer": SCORER,
            "profile": first.get("profile"),
            "chunkMs": chunk_ms,
            "budgetMs": p.budget_ms,
            "targetStreams": p.target_streams,
            "maxForeignUtilPct": p.max_foreign_util_pct,
            "levels": rows,
            "maxStreamsWithinBudget": best,
            "contended": contended,
            "cardClass": first.get("cardClass") or "",
            "server": server,
            "target": first.get("target"),
            "model": first.get("model"),
            "verdict": result,
            "reasons": reasons,
        }
        outputs["report"].write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        at = next((r for r in rows if r["streams"] == p.target_streams), None)
        if at is not None and at["chunkLatencyMs"]["p95"] is not None:
            ctx.final_metric("p95_chunk_latency_ms", float(at["chunkLatencyMs"]["p95"]))
        ctx.final_metric("max_streams_within_budget", float(best))
        ctx.set_meta(
            "report",
            {"schema": SCHEMA, "verdict": result, "targetStreams": p.target_streams, "maxStreamsWithinBudget": best},
        )
        ctx.progress(
            1.0,
            f"benchmark {result}: {best} streams within {p.budget_ms:g} ms" + (f" ({reasons[0]})" if reasons else ""),
        )
