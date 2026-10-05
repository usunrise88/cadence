"""toy_serve — the serve role of toy-ctc (phase 5): decode a dataset with a ``deployable`` as a staging server would
serve it, and write ``hypotheses`` (with ``tokens``) and ``serving_timings`` (``cadence.serving-timings/1``). There is
no server: the model runs in process on the CPU and the streams share it through a simulated queue — each chunk's
compute is measured, a chunk waits for the ones before it, so latency grows with concurrency as on a real server.
The clock is simulated (header ``clock: simulated``), so a real-time level takes no wall time. It keeps the export →
serve → parity → benchmark seam honest in CI; the family's real serve kinds are streaming clients of a staging server.

Parameters are the serve role's: ``target`` (recorded), ``profile``, ``concurrency``, ``pace`` (``fast``: a chunk is
sent when the stream's previous result is back; ``realtime``: when its audio is complete), ``seconds`` (each stream
loops over its share of the dataset until the level has streamed this long; 0: every utterance once) and
``warmup_seconds`` (the start of the level not counted). Help: docs/help/steps/toy-serve.md.
"""

from __future__ import annotations

import hashlib
import heapq
import json
import time
from collections.abc import Mapping
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, ClassVar

import torch
from pydantic import BaseModel

from cadence_toy.data import read_dataset
from cadence_toy.family import NAME, RUNTIME, profile
from cadence_toy.model import FRAME_MS, GreedyDecoder, features, load_checkpoint, use_one_thread
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

TIMINGS_SCHEMA = "cadence.serving-timings/1"
OFFLINE_CHUNK_MS = 320  # how an offline profile is streamed to the simulated server


class ServeParams(BaseModel):
    target: str = cadence_field(
        "",
        description="The deployment target (dtg_…) the decode runs through; recorded only (the toy serves in process)",
        source="The serve role's parameters (docs/review/2026-10-05-phase-5-plan.md, D2 → D1)",
        range={"maxLength": 100},
    )
    profile: str = cadence_field(default_ref="packs.toy.profile")
    concurrency: int = cadence_field(
        1,
        description="Streams decoded at once",
        source="The serve role's parameters (docs/review/2026-10-05-phase-5-plan.md, D2 → D1)",
        range={"min": 1, "max": 1024},
    )
    pace: str = cadence_field(
        "fast",
        description="fast: a chunk goes once the previous result is back; realtime: once its audio is complete",
        source="The serve role's parameters (docs/review/2026-10-05-phase-5-plan.md, D2 → D1)",
        range={"values": ["fast", "realtime"]},
    )
    seconds: float = cadence_field(
        0.0,
        description="How long each stream keeps streaming (looping over its utterances); 0: every utterance once",
        source="Cadence recommendation (a generated benchmark passes deploy.benchmark_seconds_per_level)",
        range={"min": 0, "max": 3600},
    )
    warmup_seconds: float = cadence_field(
        0.0,
        description="The start of the level that is not counted",
        source="Cadence recommendation (a generated benchmark passes deploy.benchmark_warmup_seconds)",
        range={"min": 0, "max": 120},
    )


class ServeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"deployable": "deployable", "data": "dataset"}
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "timings": "serving_timings"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    role: ClassVar[str] = "serve"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = ServeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        use_one_thread()
        p = ServeParams.model_validate(params.model_dump())
        try:
            prof = profile(p.profile)
        except KeyError as e:
            raise StepInputError(f"toy-ctc has no latency profile {p.profile!r}") from e
        dep = inputs["deployable"]
        try:
            doc = json.loads((dep / "deployable.json").read_text(encoding="utf-8"))
        except (OSError, ValueError) as e:
            raise StepInputError(f"the deployable has no readable deployable.json: {e}") from e
        if doc.get("family") != NAME:
            raise StepInputError(f"the deployable is of family {doc.get('family')!r}, not {NAME}")
        model, tok = load_checkpoint(dep / str(doc["serving"]["modelDir"]))
        utts = read_dataset(inputs["data"])
        if not utts:
            raise StepInputError("the dataset has no utterances")
        chunk_ms = int(prof.get("chunkMs") or OFFLINE_CHUNK_MS)
        per_chunk = max(1, chunk_ms // FRAME_MS)
        # Every utterance's chunks with their measured compute (the toy's GRU carries its state across chunks).
        decoded: dict[str, tuple[GreedyDecoder, list[tuple[float, float]]]] = {}
        with torch.no_grad():
            for u in utts:
                if u.audio in decoded:
                    continue
                dec = GreedyDecoder(tok)
                feats = features(u.samples)
                h: torch.Tensor | None = None
                chunks: list[tuple[float, float]] = []  # (audio ms the chunk completes, compute ms)
                for start in range(0, feats.shape[0], per_chunk):
                    t0 = time.perf_counter()
                    logp, h = model(feats[start : start + per_chunk].unsqueeze(0), h)
                    dec.feed(logp[0])
                    chunks.append(
                        (
                            float((start + min(per_chunk, feats.shape[0] - start)) * FRAME_MS),
                            (time.perf_counter() - t0) * 1000,
                        )
                    )
                decoded[u.audio] = (dec, chunks)
        decoding: dict[str, Any] = {"profile": prof["name"], "decoder": "toy-served", "format": doc.get("format")}
        dhash = "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for u in utts:
                dec, _ = decoded[u.audio]
                row = {
                    "audio": u.audio,
                    "text": dec.text(),
                    "words": dec.words(),
                    "tokens": [e.token for e in dec.emitted],
                    "decoding": decoding,
                    "decodingHash": dhash,
                    "family": NAME,
                    "weightsHash": doc.get("weightsHash"),
                }
                f.write(json.dumps(row, separators=(",", ":")) + "\n")
        rows = simulate(utts=[u.audio for u in utts], decoded={k: v[1] for k, v in decoded.items()}, p=p)
        header = {
            "schema": TIMINGS_SCHEMA,
            "profile": prof["name"],
            "chunkMs": chunk_ms,
            "concurrency": p.concurrency,
            "pace": p.pace,
            "seconds": p.seconds,
            "warmupMs": p.warmup_seconds * 1000,
            "target": p.target,
            "server": {"kind": "toy", "version": "1"},
            "cardClass": "cpu",
            "model": f"{NAME}-{prof['name']}",
            "clock": "simulated",
            "startedAt": datetime.now(UTC).isoformat(timespec="seconds").replace("+00:00", "Z"),
        }
        with outputs["timings"].open("w", encoding="utf-8") as f:
            for r in [
                header,
                *rows,
                {"type": "telemetry", "atMs": 0.0, "utilizationPct": None, "memoryUsedMb": None, "foreignUtilPct": 0.0},
            ]:
                f.write(json.dumps(r, separators=(",", ":")) + "\n")
        meta = {"family": NAME, "profile": prof["name"], "decodingHash": dhash, "utterances": len(utts), "served": True}
        ctx.set_meta("hypotheses", meta)
        ctx.set_meta("timings", {"schema": TIMINGS_SCHEMA, "concurrency": p.concurrency, "pace": p.pace})
        ctx.progress(1.0, f"served {len(utts)} utterances at {p.concurrency} stream(s), {p.pace}")


def simulate(utts: list[str], decoded: Mapping[str, list[tuple[float, float]]], p: ServeParams) -> list[dict[str, Any]]:
    """The chunk rows of S streams sharing one simulated server: stream s plays utterances s, s+S, … (looping while
    ``seconds`` lasts); a chunk is ready at its audio time (realtime) or when the stream's previous result is back
    (fast), the server takes ready chunks in order, one at a time, each for its measured compute."""
    streams = p.concurrency
    horizon = p.seconds * 1000
    queue: list[tuple[float, int, int, int, int]] = []  # (ready ms, stream, sequence, utterance index, chunk)
    # Per stream: utterance index in its share, the clip's start time.
    share = [[i for i in range(len(utts)) if i % streams == s] or [s % len(utts)] for s in range(streams)]
    pos = [0] * streams
    seq = [0] * streams
    for s in range(streams):
        first0 = decoded[utts[share[s][0]]][0][0] if p.pace == "realtime" else 0.0
        heapq.heappush(queue, (first0, s, 0, share[s][0], 0))
    clip_start = [0.0] * streams
    server_free = 0.0
    rows: list[dict[str, Any]] = []
    while queue:
        ready, s, q, ui, k = heapq.heappop(queue)
        chunks = decoded[utts[ui]]
        audio_ms, compute = chunks[k]
        start = max(ready, server_free)
        done = start + compute
        server_free = done
        last = k == len(chunks) - 1
        rows.append(
            {
                "type": "chunk",
                "stream": s,
                "audio": utts[ui],
                "index": k,
                "availableMs": round(ready, 3),
                "doneMs": round(done, 3),
                "last": last,
            }
        )
        if not last:
            nxt = clip_start[s] + chunks[k + 1][0] if p.pace == "realtime" else done
            heapq.heappush(queue, (max(nxt, done) if p.pace == "fast" else nxt, s, q, ui, k + 1))
            continue
        pos[s] += 1
        if pos[s] >= len(share[s]):
            if horizon <= 0 or done >= horizon:
                continue
            pos[s] = 0
        if horizon > 0 and done >= horizon:
            continue
        seq[s] += 1
        clip_start[s] = max(done, clip_start[s] + audio_ms) if p.pace == "realtime" else done
        nui = share[s][pos[s]]
        first = clip_start[s] + decoded[utts[nui]][0][0] if p.pace == "realtime" else clip_start[s]
        heapq.heappush(queue, (first, s, seq[s], nui, 0))
    return rows
