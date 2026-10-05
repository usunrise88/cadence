"""nemotron_serve — the serve role of the Nemotron family: stream audio to a model on the staging server
(docs/spec/06-platform.md "Staging serving", 03 "Export, parity and benchmark"; spike E1).

The lease names the staging target, its endpoint and the model's versioned name (internal/serving). The step copies
the deployable's model directory into the ``serving`` volume, loads it on the server (unless it is loaded already),
checks its memory against the reservation, and then:

- **batch** (parity, benchmark, shadow replay): streams every utterance of the ``data`` input at ``concurrency``
  concurrent streams, at real-time pace (a chunk is sent when its audio has arrived) or as fast as the server answers,
  for one pass or for ``seconds`` (each stream cycling through the utterances). It writes ``hypotheses`` (one row per
  utterance, its first complete decode, with the token ids the parity check compares) and ``serving_timings`` (per
  chunk: audio end, available, sent and answered; the level's card telemetry and the server's counters);
- **relay** (a transcription session whose targets are deployments, R47): serves the live channel like
  ``nemotron_live``, every lane a stream of the served model.

The client computes the features (``cadence_nemo.serving``: the pipeline decoder's buffers); endpointing, text and the
locale tag stay outside the server, as E1 found. Help: docs/help/steps/nemotron-serve.md.
"""

from __future__ import annotations

import hashlib
import json
import os
import threading
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar, Literal

import numpy as np
from pydantic import BaseModel

from cadence_nemo import lang, serving
from cadence_nemo.family import NAME, RUNTIME
from cadence_nemo.mixdata import read_dataset_dir
from cadence_nemo.steps.transcribe import read_clip
from cadence_worker import live
from cadence_worker.cas import hash_file
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext
from cadence_worker.telemetry import Telemetry

SERVE_SOURCE = 'docs/spec/06-platform.md "Staging serving"; docs/spikes/E1-onnx-triton.md'


class ServeParams(live.LiveParams):
    """Batch and relay share one schema: the live channel's parameters (set by the control plane for relay mode) and
    the stream client's."""

    mode: Literal["batch", "relay"] = cadence_field(
        "batch",
        description="batch: stream the data input's utterances and write hypotheses and timings; relay: serve a "
        "transcription session's live channel (the control plane sets it for deployment targets)",
        source=SERVE_SOURCE,
        range={"values": ["batch", "relay"]},
    )
    target: str = cadence_field(default_ref="serving.default_target")
    concurrency: int = cadence_field(
        1,
        description="Concurrent streams in batch mode (parity runs one; a benchmark level runs its stream count)",
        source="R31 (benchmark levels deploy.benchmark_streams); E1: 256 real-time streams per card at 80 ms",
        range={"min": 1, "max": 512},
    )
    profile: str = cadence_field(
        "",
        description="The latency profile to stream at; empty takes the deployable's (an export is one profile)",
        source=SERVE_SOURCE,
        range="any",
    )
    seconds: float = cadence_field(
        0.0,
        description="Batch mode: stream for this long, each stream cycling through the utterances; 0 decodes every "
        "utterance once",
        source="R31 (deploy.benchmark_seconds_per_level)",
        range={"min": 0, "max": 3600},
    )
    partials: bool = cadence_field(
        True,
        description="Record each utterance's partials (text, audio offset, emit time) in its hypothesis row",
        source=SERVE_SOURCE,
        range="any",
    )
    target_lang: str = cadence_field(default_ref="packs.nemo.target_lang")
    stop_history_eou_ms: int = cadence_field(default_ref="packs.nemo.live_stop_history_eou_ms")
    load_timeout_s: int = cadence_field(default_ref="serving.load_timeout_s")
    over_cap_slack_mb: int = cadence_field(default_ref="serving.over_cap_slack_mb")


def decoding_config(dep: serving.Deployable, key: str, eou_ms: int, server: str, version: str) -> dict[str, Any]:
    """What a served hypotheses row records: the served engine (its export), not the reference decoder."""
    return {
        "profile": dep.profile,
        "decoder": serving.DECODER,
        "format": dep.format,
        "server": {"kind": server, "version": version},
        "targetLang": key,
        "stripLangTags": True,
        "stopHistoryEouMs": eou_ms,
        "wordTimestamps": "served-chunks",
    }


def decoding_hash(decoding: Mapping[str, Any]) -> str:
    return "sha256:" + hashlib.sha256(json.dumps(decoding, sort_keys=True).encode()).hexdigest()


@dataclass
class Clip:
    audio_hash: str
    chunks: list[serving.Chunk]
    seconds: float


@dataclass
class Level:
    """One batch run: its rows, timing lines and counts, filled by the stream threads."""

    rows: dict[int, dict[str, Any]] = field(default_factory=dict)
    lines: list[dict[str, Any]] = field(default_factory=list)
    audio_s: float = 0.0
    errors: list[str] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)


def stream_clips(
    level: Level,
    sid: int,
    order: list[int],
    clips: list[Clip],
    *,
    make_stream: Callable[[], serving.ServedStream],
    pace: str,
    t_zero: float,
    until: float | None,
    partials: bool,
    row_of: Callable[[Clip, serving.ServedStream, list[dict[str, Any]], list[list[float]]], dict[str, Any]],
) -> None:
    """One stream: its clips one after another, one server sequence each, paced from ``t_zero`` (perf_counter)."""
    i = 0
    while True:
        if until is None and i >= len(order):
            return
        if until is not None and time.perf_counter() >= until:
            return
        ci = order[i % len(order)]
        i += 1
        clip = clips[ci]
        s = make_stream()
        s.sequence = serving.next_sequence()
        start = time.perf_counter()
        avail = start
        chunks: list[list[float]] = []
        events: list[dict[str, Any]] = []
        parts: list[dict[str, Any]] = []
        audio_ms = 0.0
        try:
            for ch in clip.chunks:
                avail += ch.real / serving.SR
                audio_ms += ch.real / serving.SR * 1000
                now = time.perf_counter()
                if pace == "realtime" and avail > now:
                    time.sleep(avail - now)
                sent = time.perf_counter()
                ev, _ = s.send(ch)
                done = time.perf_counter()
                ref = avail if pace == "realtime" else sent
                chunks.append(
                    [
                        round(audio_ms, 1),
                        round((ref - t_zero) * 1000, 2),
                        round((sent - t_zero) * 1000, 2),
                        round((done - t_zero) * 1000, 2),
                    ]
                )
                events += ev
                if partials:
                    parts += [
                        {
                            "text": e["text"],
                            "audioOffsetMs": round(audio_ms),
                            "emitMs": round(audio_ms + (done - ref) * 1000),
                        }
                        for e in ev
                        if e["type"] == "partial"
                    ]
            events.append(s.final("end"))
        except Exception as e:
            with level.lock:
                level.errors.append(f"{type(e).__name__}: {e}"[:500])
            if until is None:
                raise
            continue
        finals = [e for e in events if e["type"] == "final"]
        line = {
            "type": "utterance",
            "stream": sid,
            "audio": clip.audio_hash,
            "chunks": chunks,
            "finalAvailMs": chunks[-1][1] if chunks else None,
            "finalDoneMs": chunks[-1][3] if chunks else None,
        }
        with level.lock:
            level.lines.append(line)
            level.audio_s += clip.seconds
            if ci not in level.rows:
                level.rows[ci] = row_of(clip, s, finals, chunks) | ({"partials": parts} if partials else {})


def sample_telemetry(level: Level, index: int | None, stop: threading.Event, t_zero: float, every: float = 1.0) -> None:
    """The card's telemetry during the level, once a second (the benchmark's contention check reads it)."""
    tel = Telemetry()
    while not stop.wait(every):
        for c in tel.cards():
            if index is not None and c.get("index") == index:
                with level.lock:
                    level.lines.append(
                        {
                            "type": "telemetry",
                            "tMs": round((time.perf_counter() - t_zero) * 1000),
                            "utilization": c.get("utilization"),
                            "memoryUsedMb": c.get("memoryUsedMb"),
                        }
                    )


class ServeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"deployable": "deployable", "data": "dataset", "audio": "audio"}
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"data", "audio"})
    produces: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "serving_timings": "serving_timings"}
    optional_outputs: ClassVar[frozenset[str]] = frozenset({"hypotheses", "serving_timings"})
    # The reservation stands for the served model on the card (serving.model_memory_gb unless the deployable states
    # one); the client itself runs on the CPU.
    resources: ClassVar[StepResources] = {"gpu": True, "gpus": 1, "memoryGb": 9, "diskGb": 6, "jobKind": "eval"}
    role: ClassVar[str] = "serve"
    runtime: ClassVar[str] = RUNTIME
    Params: ClassVar[type[BaseModel]] = ServeParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ServeParams.model_validate(params.model_dump())
        lease = serving.ServingLease.from_env()
        lease.check()
        deps = {name: serving.read_deployable(path) for name, path in inputs.items() if name.startswith("deployable")}
        if not deps:
            raise StepInputError("the step needs a deployable input")
        for name, dep in deps.items():
            if dep.family and dep.family != NAME:
                raise StepInputError(f"input {name} is a deployable of family {dep.family}, not {NAME}")
        root = Path(os.environ.get(serving.ENV_DIR, serving.DEFAULT_DIR))
        control = serving.Control(lease.endpoint)
        index = serving.physical_card() if ctx.card is not None else None
        loads: dict[str, Any] = {}
        ctx.progress(0.0, "loading the model on the staging server")
        for name, dep in deps.items():
            model = lease.model_for(name)
            serving.install(dep, root, model)
            loads[name] = serving.ensure_loaded(
                control,
                model,
                reservation_mb=dep.memory_mb or ctx.memory_cap_mb,
                slack_mb=p.over_cap_slack_mb,
                timeout_s=p.load_timeout_s,
                used_mb=lambda: serving.card_used_mb(index),
            )
        ctx.log(
            "served models ready", target=lease.target, server=f"{lease.server} {lease.server_version}", loads=loads
        )
        if ctx.should_stop():
            return
        if p.mode == "relay":
            self.relay(p, inputs, deps, lease, ctx, loads)
        else:
            self.batch(p, inputs, deps, lease, outputs, ctx, control, index)

    # ------------------------------------------------------------ relay

    def relay(
        self,
        p: ServeParams,
        inputs: Mapping[str, Path],
        deps: Mapping[str, serving.Deployable],
        lease: serving.ServingLease,
        ctx: StepContext,
        loads: Mapping[str, Any],
    ) -> None:
        live.check_targets(p, inputs)
        streams: list[serving.ServedStream] = []
        for t in p.targets:
            dep = deps.get(t.model)
            if dep is None:
                raise StepInputError(f"target {t.target}: {t.model!r} is not a deployable input")
            if t.profile and dep.profile and t.profile != dep.profile:
                raise StepInputError(f"target {t.target}: the deployable serves {dep.profile}, not {t.profile}")
            geo = dep.geometry
            _, prompt = geo.prompt(t.language)
            model = lease.model_for(t.model)
            load_s = float((loads.get(t.model) or {}).get("loadS") or 0.0)
            streams.append(
                serving.ServedStream(
                    target=t.target,
                    client=serving.StepClient(lease.endpoint, model),
                    geo=geo,
                    make=serving.featurizer_factory(geo),
                    prompt=prompt,
                    profile=dep.profile or t.profile,
                    language=t.language,
                    eou_ms=p.stop_history_eou_ms,
                    load_s=load_s,
                )
            )
        ctx.progress(0.5, "waiting for the session")
        channel = live.dial()
        ctx.progress(1.0, "live")
        try:
            summary = live.serve(
                channel, streams, p, work_dir=ctx.work_dir, should_stop=ctx.should_stop, audio_path=inputs.get("audio")
            )
        finally:
            channel.close(1000, "the job ended")
            for s in streams:
                s.client.close()
        ctx.log("session ended", session=p.session, audioS=(summary or {}).get("audioS"))

    # ------------------------------------------------------------ batch

    def batch(
        self,
        p: ServeParams,
        inputs: Mapping[str, Path],
        deps: Mapping[str, serving.Deployable],
        lease: serving.ServingLease,
        outputs: Mapping[str, Path],
        ctx: StepContext,
        control: serving.Control,
        index: int | None,
    ) -> None:
        if len(deps) != 1:
            raise StepInputError("batch mode streams one deployable")
        if "data" not in inputs:
            raise StepInputError("batch mode needs the data input (a dataset)")
        ((name, dep),) = deps.items()
        if p.profile and dep.profile and p.profile != dep.profile:
            raise StepInputError(f"the deployable serves {dep.profile}, not {p.profile}: export that profile")
        model = lease.model_for(name)
        geo = dep.geometry
        part = read_dataset_dir(inputs["data"])
        langs = {c.language for c in part.clips}
        keys = {lang.resolve_prompt_key(p.target_lang or lg, geo.prompts) for lg in langs}
        if len(keys) > 1:
            raise StepInputError(f"the dataset mixes languages {sorted(langs)}; stream one language per step")
        key = next(iter(keys)) if keys else lang.resolve_prompt_key(p.target_lang, geo.prompts)
        prompt = geo.prompts[key]
        make = serving.featurizer_factory(geo)
        ctx.progress(0.05, "computing the features")
        clips: list[Clip] = []
        for c in part.clips:
            x = read_clip(c.audio)
            clips.append(Clip(hash_file(c.audio), serving.utterance_chunks(geo, make, x), x.size / serving.SR))
        if not clips:
            raise StepInputError("the dataset has no utterances")
        decoding = decoding_config(dep, key, p.stop_history_eou_ms, lease.server, lease.server_version)
        dhash = decoding_hash(decoding)

        def row_of(
            clip: Clip, s: serving.ServedStream, finals: list[dict[str, Any]], chunks: list[list[float]]
        ) -> dict[str, Any]:
            words = [w for f in finals for w in f.get("words") or []]
            return {
                "audio": clip.audio_hash,
                "text": serving.join_text(finals),
                "words": words,
                "tokens": s.tokens,
                "decoding": decoding,
                "decodingHash": dhash,
                "family": NAME,
                "weightsHash": dep.weights_hash,
                "steps": [[c[0], round(c[3] - c[1], 2)] for c in chunks],
            }

        clients: list[serving.StepClient] = []

        def make_stream() -> serving.ServedStream:
            cl = serving.StepClient(lease.endpoint, model)
            clients.append(cl)
            return serving.ServedStream(
                target="A",
                client=cl,
                geo=geo,
                make=make,
                prompt=prompt,
                profile=dep.profile,
                language=key,
                eou_ms=p.stop_history_eou_ms,
            )

        n = len(clips)
        conc = min(p.concurrency, n) if p.seconds <= 0 else p.concurrency
        level = Level()
        before = control.stats(model)
        t_zero = time.perf_counter()
        until = t_zero + p.seconds if p.seconds > 0 else None
        stop = threading.Event()
        sampler = threading.Thread(target=sample_telemetry, args=(level, index, stop, t_zero), daemon=True)
        sampler.start()
        threads: list[threading.Thread] = []
        failures: list[BaseException] = []

        def run_stream(sid: int) -> None:
            order = list(range(sid, n, conc)) if until is None else [(sid * 7 + k) % n for k in range(n)]
            try:
                stream_clips(
                    level,
                    sid,
                    order,
                    clips,
                    make_stream=make_stream,
                    pace=p.pace,
                    t_zero=t_zero,
                    until=until,
                    partials=p.partials,
                    row_of=row_of,
                )
            except BaseException as e:
                failures.append(e)

        ctx.progress(0.1, f"streaming at {conc} concurrent streams ({p.pace})")
        for sid in range(conc):
            th = threading.Thread(target=run_stream, args=(sid,), daemon=True)
            th.start()
            threads.append(th)
        while any(th.is_alive() for th in threads):
            for th in threads:
                th.join(timeout=1.0)
            with level.lock:
                done = len(level.rows)
            ctx.progress(0.1 + 0.85 * min(1.0, done / n), f"{done}/{n} utterances")
        stop.set()
        sampler.join(timeout=2.0)
        for cl in clients:
            cl.close()
        if failures:
            raise failures[0]
        wall = time.perf_counter() - t_zero
        after = control.stats(model)
        missing = n - len(level.rows)
        if until is None and missing:
            raise StepInputError(f"{missing} utterances were not decoded")
        summary = {
            "type": "level",
            "concurrency": conc,
            "pace": p.pace,
            "seconds": round(wall, 2),
            "utterances": len(level.lines),
            "audioS": round(level.audio_s, 2),
            "errors": len(level.errors),
            "firstErrors": level.errors[:5],
            "model": model,
            "target": lease.target,
            "server": {"kind": lease.server, "version": lease.server_version},
            "profile": dep.profile,
            "chunkMs": geo.chunk_ms,
            "serverStats": {"before": before, "after": after},
        }
        with outputs["hypotheses"].open("w", encoding="utf-8") as f:
            for ci in sorted(level.rows):
                f.write(json.dumps(level.rows[ci], ensure_ascii=False, separators=(",", ":")) + "\n")
        with outputs["serving_timings"].open("w", encoding="utf-8") as f:
            for line in [*level.lines, summary]:
                f.write(json.dumps(line, separators=(",", ":")) + "\n")
        meta = {
            "family": NAME,
            "profile": dep.profile,
            "weightsHash": dep.weights_hash,
            "decodingHash": dhash,
            "decoder": serving.DECODER,
            "utterances": len(level.rows),
            "language": key,
            "concurrency": conc,
            "pace": p.pace,
            "target": lease.target,
            "server": f"{lease.server} {lease.server_version}",
        }
        ctx.set_meta("hypotheses", meta)
        ctx.set_meta("serving_timings", meta | {"seconds": summary["seconds"], "errors": summary["errors"]})
        lat = [c[3] - c[1] for ln in level.lines if ln.get("type") == "utterance" for c in ln["chunks"]]
        if lat:
            ctx.metric("chunk_latency_p95_ms", float(np.percentile(np.asarray(lat), 95)))
        ctx.log(
            "served",
            utterances=len(level.rows),
            streams=conc,
            pace=p.pace,
            seconds=summary["seconds"],
            errors=summary["errors"],
        )
