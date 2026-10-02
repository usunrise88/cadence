"""The worker's half of the live channel (R48, docs/spec/06-platform.md "Media"; spike A5): a family's ``live`` role
kind loads every target, dials the control plane's relay (``/api/worker-live/{jobId}``) and serves one transcription
session over it with :func:`serve`.

The relay forwards the browser's frames byte for byte, so this module speaks the contract's ``LiveClientMessage``
(``start`` first, then ``fileEnd``, ``finalize``, ``keepalive``, ``end``; audio in binary frames) and answers with
``LiveServerMessage`` (``started``, ``partial``, ``final``, ``stats``, ``pong``, ``error``, ``summary``). It is
runtime-neutral: the model sits behind :class:`Decoder` (one per target; the NeMo pack's wraps NeMo's cache-aware
streaming pipeline, the toy pack's its GRU) and the transport behind :class:`Channel` (a WebSocket in a lease, a list in
tests).

Every input reaches the decoders as 16 kHz float audio through the training resampler (:mod:`cadence_worker.resample`,
streaming polyphase) and, when the session simulates a phone line, through :class:`~cadence_worker.resample.Telephony`:

- ``microphone``: binary frames of 16-bit little-endian mono PCM at ``start.input.sampleRate``;
- ``file``: the file's bytes, kept in the step's work directory until the socket closes; ``fileEnd`` decodes them
  (ffprobe and ffmpeg when present — an accepted audio container read from the local file only, its declared duration
  checked before decoding, the decode cut and size-capped; else WAV/soundfile after the header's duration is checked),
  channel 0, at most ``maxFileSeconds``, and streams them at the session's
  pace (``realtime``: 20 ms of audio per 20 ms; ``fast``: as fast as the decoders go);
- ``span``: the utterance's audio (the step's ``audio`` input) cut to [start, end] on its channel, streamed like a file
  right after ``start``.

``finalize`` is a segment boundary: every decoder pads its right context with silence and forces an end of utterance;
the next audio opens a new decoder stream. ``end`` (or the end of a file or span) flushes the resampler and the phone
line, finalizes every target with endpoint ``end``, sends ``summary`` and closes with 1000. Nothing is written but the
uploaded file, which is deleted when the session ends.
"""

from __future__ import annotations

import contextlib
import json
import os
import shutil
import subprocess
import tempfile
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Literal, Protocol

import numpy as np
from numpy.typing import NDArray
from pydantic import BaseModel, ConfigDict, Field

from cadence_worker import audio as audio_io
from cadence_worker.resample import RESAMPLER, StreamingResampler, Telephony, resample_poly
from cadence_worker.steps.base import StepInputError, cadence_field

SR = 16000
PACE_FRAME_S = 0.02  # a paced file advances 20 ms at a time
FAST_FRAME_S = 0.2  # an unpaced one in bigger steps
STATS_EVERY_S = 1.0
RECV_TIMEOUT_S = 0.5  # how long a wait for a message may last before should_stop() is checked again
PROBLEM_BASE = "https://cadence.local/help/errors/"
URL_ENV = "CADENCE_LIVE_URL"
TOKEN_ENV = "CADENCE_LIVE_TOKEN"
TOKEN_HEADER = "Cadence-Live-Token"
MAX_MESSAGE_BYTES = 1 << 20

Event = dict[str, Any]
Audio = NDArray[np.float32]


class ChannelClosedError(Exception):
    """The other side closed the socket."""


class Channel(Protocol):
    def recv(self, timeout: float) -> str | bytes | None:
        """The next message, or None when ``timeout`` seconds pass first; ChannelClosedError when the socket closed."""
        ...

    def send(self, text: str) -> None: ...

    def close(self, code: int = 1000, reason: str = "") -> None: ...


class Decoder(Protocol):
    """One target: a model at a latency profile and a language, decoding 16 kHz float audio as it arrives."""

    @property
    def target(self) -> str: ...

    @property
    def profile(self) -> str: ...

    @property
    def chunk_ms(self) -> int: ...

    @property
    def language(self) -> str: ...

    @property
    def load_s(self) -> float: ...

    @property
    def decoder(self) -> str:
        """The decoder's name, as an eval of the same transcribe kind records it."""
        ...

    @property
    def boost(self) -> Mapping[str, Any] | None:
        """{terms, weight} of the target's boost list, or None."""
        ...

    @property
    def step_ms(self) -> list[float]:
        """Wall time of every decoding step so far (the summary's stepMs figures)."""
        ...

    def push(self, x: Audio) -> list[Event]:
        """Partial and final events for the audio so far (a partial replaces the segment's previous one)."""
        ...

    def finalize(self, reason: str) -> list[Event]:
        """Close the current segment: pad the right context, emit its final (endpoint ``reason``)."""
        ...


# ---------------------------------------------------------------- the live step's parameters (from transcriptions.new)

LIVE_SOURCE = 'docs/spec/06-platform.md "Media" — set by the control plane from transcriptions.new (R47, R48)'


class LiveTargetParam(BaseModel):
    model_config = ConfigDict(extra="forbid")
    target: Literal["A", "B", "C"]
    model: str = Field(description="The input that holds the target's weights: model.<n> (a checkpoint) or base.<n>")
    profile: str
    language: str
    boost: str | None = Field(default=None, description="The input that holds its boost list (boost.<n>), or none")


class LiveInputParam(BaseModel):
    model_config = ConfigDict(extra="forbid")
    kind: Literal["microphone", "file", "span"]
    start: float | None = Field(default=None, ge=0)
    end: float | None = Field(default=None, ge=0)
    channel: int | None = Field(default=None, ge=0, le=15)


class LiveTelephonyParam(BaseModel):
    model_config = ConfigDict(extra="forbid")
    codec: Literal["ulaw", "alaw", "none"] = "ulaw"
    sampleRate: int = Field(default=8000, ge=4000, le=16000)  # noqa: N815 - the contract's name


class LiveParams(BaseModel):
    """What every live role kind takes; a pack's kind adds its own parameters."""

    session: str = cadence_field(
        "", description="The transcription session (trs_…) the job serves", source=LIVE_SOURCE, range="any"
    )
    targets: list[LiveTargetParam] = cadence_field(
        default=[],
        description="One to three targets (lanes A, B, C): the model input, latency profile, language and boost list",
        source=LIVE_SOURCE,
        range="any",
    )
    input: LiveInputParam = cadence_field(
        default=LiveInputParam(kind="microphone"),
        description="microphone, file, or span (with start, end and channel of the audio input)",
        source=LIVE_SOURCE,
        range="any",
    )
    telephony: LiveTelephonyParam | None = cadence_field(
        default=None,
        description="Phone-line simulation (16 kHz → 8 kHz → codec → 16 kHz, polyphase), or none",
        source=LIVE_SOURCE,
        range="any",
    )
    pace: Literal["realtime", "fast"] = cadence_field(
        "realtime",
        description="File and span inputs: real-time pace or as fast as the card allows",
        source=LIVE_SOURCE,
        range={"values": ["realtime", "fast"]},
    )
    maxFileSeconds: int = cadence_field(  # noqa: N815 - the control plane's name
        900,
        description="Longest file a session accepts, in seconds of audio",
        source="defaults.yaml transcriptions.max_file_minutes (set by the control plane)",
        range={"min": 1, "max": 14400},
    )
    maxFileBytes: int = cadence_field(  # noqa: N815 - the control plane's name
        300 * 1024 * 1024,
        description="Largest file a session accepts, in bytes",
        source="defaults.yaml transcriptions.max_file_mb (set by the control plane)",
        range={"min": 1, "max": 4 * 1024 * 1024 * 1024},
    )
    frameMs: int = cadence_field(  # noqa: N815 - the control plane's name
        20,
        description="Longest microphone frame the page sends",
        source="defaults.yaml transcriptions.frame_ms (set by the control plane)",
        range={"min": 10, "max": 80},
    )


def check_targets(p: LiveParams, inputs: Mapping[str, Path]) -> None:
    """Refuse a session the control plane could not have meant: no target, more than three, a lane twice, a missing
    model input."""
    if not 1 <= len(p.targets) <= 3:
        raise StepInputError(f"a session has one to three targets, not {len(p.targets)}")
    names = [t.target for t in p.targets]
    if len(set(names)) != len(names):
        raise StepInputError(f"a lane is named twice: {names}")
    for t in p.targets:
        if t.model not in inputs:
            raise StepInputError(f"target {t.target}: no input {t.model!r}")
        if t.boost and t.boost not in inputs:
            raise StepInputError(f"target {t.target}: no input {t.boost!r}")
    if p.input.kind == "span" and "audio" not in inputs:
        raise StepInputError("a span session needs the audio input")


# ---------------------------------------------------------------- helpers


def problem(slug: str, title: str, status: int, detail: str) -> dict[str, Any]:
    return {"type": PROBLEM_BASE + slug, "title": title, "status": status, "detail": detail}


def error(slug: str, title: str, status: int, detail: str, fatal: bool) -> Event:
    return {"type": "error", "problem": problem(slug, title, status, detail), "fatal": fatal}


def pcm16_to_float(b: bytes) -> Audio:
    n = len(b) // 2
    return np.frombuffer(b[: n * 2], dtype="<i2").astype(np.float32) / 32768.0


def pct(xs: Sequence[float], p: float) -> float | None:
    return round(float(np.percentile(np.asarray(xs, dtype=np.float64), p)), 2) if xs else None


def message_type(raw: str) -> tuple[str, dict[str, Any]]:
    """The message's ``type`` (read tolerantly: a client may send ``"type": "x"`` with a space) and its body."""
    try:
        doc = json.loads(raw)
    except ValueError:
        return "", {}
    if not isinstance(doc, dict):
        return "", {}
    return str(doc.get("type") or "").strip(), doc


# The containers a file session accepts (ffmpeg demuxer names, also its -format_whitelist): an upload is a person's
# recording, so playlists (hls), concatenation lists, image or video-only formats and network protocols never reach a
# demuxer that would follow them. mov covers mp4/m4a (AAC or ALAC), matroska covers webm (Opus), ogg covers Opus/Vorbis.
AUDIO_FORMATS = ("wav", "flac", "mp3", "ogg", "mov", "mp4", "m4a", "aac", "matroska", "webm")
MAX_FILE_RATE = 192000  # the highest sample rate a file session decodes (and the output size cap assumes)
FFMPEG_TIMEOUT_S = 300


class FileRefusedError(StepInputError):
    """An upload refused before decoding: too long, too high a rate."""


def _ffmpeg_tools() -> tuple[str, str] | None:
    ffmpeg, ffprobe = shutil.which("ffmpeg"), shutil.which("ffprobe")
    return (ffmpeg, ffprobe) if ffmpeg and ffprobe else None


def probe_file(ffprobe: str, path: Path) -> tuple[str, float | None, int | None]:
    """The container, duration (None when the container does not say) and first audio stream's sample rate of an
    upload, read by ffprobe on the local file only and among AUDIO_FORMATS only."""
    cmd = [ffprobe, "-v", "error", "-protocol_whitelist", "file", "-format_whitelist", ",".join(AUDIO_FORMATS)]
    cmd += ["-select_streams", "a:0", "-show_entries", "format=format_name,duration:stream=sample_rate", "-of", "json"]
    cmd += ["file:" + str(path)]
    res = subprocess.run(cmd, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60, check=False)
    if res.returncode != 0:
        detail = res.stderr.strip()[-300:]
        if "not on whitelist" in detail:
            raise audio_io.AudioError(f"not an audio file of an accepted container ({', '.join(AUDIO_FORMATS)})")
        raise audio_io.AudioError(detail or "ffprobe could not read the file")
    try:
        doc = json.loads(res.stdout or "{}")
    except ValueError as e:
        raise audio_io.AudioError("ffprobe answered no JSON") from e
    fmt = doc.get("format") or {}
    streams = doc.get("streams") or []
    if not streams:
        raise audio_io.AudioError("the file holds no audio stream")
    duration: float | None
    try:
        duration = float(str(fmt["duration"]))
    except (KeyError, TypeError, ValueError):
        duration = None
    try:
        rate: int | None = int(str(streams[0]["sample_rate"]))
    except (KeyError, TypeError, ValueError):
        rate = None
    return str(fmt.get("format_name") or ""), duration, rate


def file_seconds(path: Path) -> float | None:
    """The duration a file's header declares, without decoding it (WAV, or anything soundfile reads); None when it
    cannot tell."""
    with path.open("rb") as f:
        head = f.read(12)
        if head[:4] == b"RIFF" and head[8:12] == b"WAVE":
            byte_rate, data = 0, None
            while True:
                chunk = f.read(8)
                if len(chunk) < 8:
                    break
                cid, size = chunk[:4], int.from_bytes(chunk[4:], "little")
                if cid == b"fmt ":
                    body = f.read(size)
                    byte_rate = int.from_bytes(body[8:12], "little") if len(body) >= 12 else 0
                    f.seek(size % 2, os.SEEK_CUR)
                    continue
                if cid == b"data":
                    data = size
                    break
                f.seek(size + size % 2, os.SEEK_CUR)
            return data / byte_rate if data is not None and byte_rate > 0 else None
    try:
        import soundfile  # type: ignore[import-untyped,import-not-found,unused-ignore]

        return float(soundfile.info(str(path)).duration)
    except Exception:
        return None


def decode_file(path: Path, work: Path, max_seconds: int) -> tuple[Audio, int]:
    """A file's channel 0 as float samples at its own rate (the training resampler takes it from there). With ffmpeg:
    the file is probed first — a local file in an accepted audio container (AUDIO_FORMATS), at most MAX_FILE_RATE, whose
    declared duration is at most ``max_seconds`` — then decoded from the local file only, cut at ``max_seconds`` + 1 s
    and capped in output size, so a tiny file that expands to hours (a decompression bomb) or a playlist that points
    elsewhere is refused before it costs anything. Without ffmpeg: the WAV reader (soundfile for FLAC/OGG/MP3 when
    installed), after the header's duration is checked."""
    tools = _ffmpeg_tools()
    if tools is None:
        declared = file_seconds(path)
        if declared is not None and declared > max_seconds:
            raise FileRefusedError(f"the file holds {declared:.0f} s of audio; a session takes at most {max_seconds} s")
        a = audio_io.read(path)
        return channel_of(a, 0), a.sample_rate
    ffmpeg, ffprobe = tools
    _, duration, rate = probe_file(ffprobe, path)
    if duration is not None and duration > max_seconds:
        raise FileRefusedError(f"the file holds {duration:.0f} s of audio; a session takes at most {max_seconds} s")
    if rate is not None and rate > MAX_FILE_RATE:
        raise FileRefusedError(f"the file's sample rate is {rate} Hz; a session takes at most {MAX_FILE_RATE} Hz")
    limit_s = max_seconds + 1
    out = work / (path.name + ".wav")
    max_bytes = limit_s * (rate or MAX_FILE_RATE) * 4 + 4096  # float32 mono and the header
    cmd = [ffmpeg, "-nostdin", "-v", "error", "-y", "-protocol_whitelist", "file"]
    cmd += [
        "-format_whitelist",
        ",".join(AUDIO_FORMATS),
        "-i",
        "file:" + str(path),
        "-map",
        "0:a:0",
        "-t",
        str(limit_s),
    ]
    cmd += ["-af", "pan=mono|c0=c0", "-c:a", "pcm_f32le", "-fs", str(max_bytes), "-f", "wav", str(out)]
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=FFMPEG_TIMEOUT_S, check=False)
        if res.returncode != 0:
            raise audio_io.AudioError(res.stderr.strip()[-500:] or "ffmpeg could not decode the file")
        a = audio_io.read(out)
    finally:
        out.unlink(missing_ok=True)
    return channel_of(a, 0), a.sample_rate


def channel_of(a: audio_io.Audio, channel: int) -> Audio:
    if channel >= a.channels:
        raise StepInputError(f"the audio has {a.channels} channel(s); channel {channel} does not exist")
    x = np.asarray(a.samples, dtype=np.float32)
    if a.channels > 1:
        x = x[: x.size - x.size % a.channels].reshape(-1, a.channels)[:, channel]
    return np.ascontiguousarray(x, dtype=np.float32)


# ---------------------------------------------------------------- the session


@dataclass
class _Session:
    channel: Channel
    decoders: Sequence[Decoder]
    params: LiveParams
    work: Path
    audio_path: Path | None
    clock: Callable[[], float]
    gpu: Callable[[], Mapping[str, Any]] | None
    load: Mapping[str, Any] | None
    started: bool = False
    res: StreamingResampler | None = None
    tel: Telephony | None = None
    capture_rate: int | None = None
    audio_in: int = 0  # 16 kHz samples given to the decoders
    compute_s: float = 0.0
    frame_ms: list[float] = field(default_factory=list)
    finals: dict[str, int] = field(default_factory=dict)
    last_stats: float = 0.0
    upload: Path | None = None
    upload_bytes: int = 0
    upload_fh: Any = None
    pending: Audio | None = None  # file or span audio at 16 kHz still to stream
    pos: int = 0
    feed_t0: float = 0.0
    fed_s: float = 0.0
    done: bool = False

    # -------------------------------------------------------------- output

    def send(self, ev: Event) -> None:
        if ev.get("type") == "final":
            t = str(ev.get("target"))
            self.finals[t] = self.finals.get(t, 0) + 1
        self.channel.send(json.dumps(ev, ensure_ascii=False, separators=(",", ":")))

    def send_all(self, evs: Sequence[Event]) -> None:
        for e in evs:
            self.send(e)

    def fail(self, slug: str, title: str, status: int, detail: str, code: int = 1011) -> None:
        self.send(error(slug, title, status, detail, fatal=True))
        self.channel.close(code, detail[:120])
        self.done = True

    # -------------------------------------------------------------- audio

    def feed(self, x: Audio) -> None:
        """16 kHz audio (after the phone line) to every decoder."""
        if x.size == 0:
            return
        t0 = time.perf_counter()
        evs: list[Event] = []
        for d in self.decoders:
            evs += d.push(x)
        dt = time.perf_counter() - t0
        self.compute_s += dt
        self.frame_ms.append(dt * 1000)
        self.audio_in += int(x.size)
        self.send_all(evs)

    def line(self, x: Audio) -> Audio:
        return self.tel.push(x) if self.tel is not None else x

    def tail(self) -> Audio:
        """What the resampler and the phone line still hold (the end of the audio)."""
        parts: list[Audio] = []
        if self.res is not None:
            parts.append(self.line(self.res.flush()))
            self.res = None
        if self.tel is not None:
            parts.append(self.tel.flush())
        return np.concatenate(parts).astype(np.float32) if parts else np.zeros(0, dtype=np.float32)

    def finalize(self, reason: str) -> None:
        t0 = time.perf_counter()
        evs: list[Event] = []
        for d in self.decoders:
            evs += d.finalize(reason)
        self.compute_s += time.perf_counter() - t0
        self.send_all(evs)

    def stats(self, force: bool = False) -> None:
        now = self.clock()
        if self.audio_in == 0 or (not force and now - self.last_stats < STATS_EVERY_S):
            return
        self.last_stats = now
        self.send(
            {
                "type": "stats",
                "source": "worker",
                "rtf": round(self.compute_s / max(self.audio_in / SR, 1e-9), 4),
                "audioS": round(self.audio_in / SR, 2),
            }
        )

    def summary(self) -> Event:
        targets: dict[str, Any] = {}
        for d in self.decoders:
            targets[d.target] = {
                "profile": d.profile,
                "steps": len(d.step_ms),
                "stepMsP50": pct(d.step_ms, 50),
                "stepMsP95": pct(d.step_ms, 95),
                "stepMsMax": pct(d.step_ms, 100),
                "finals": self.finals.get(d.target, 0),
            }
        ev: Event = {
            "type": "summary",
            "audioS": round(self.audio_in / SR, 2),
            "rtf": round(self.compute_s / max(self.audio_in / SR, 1e-9), 4),
            "targets": targets,
            "frameMsP50": pct(self.frame_ms, 50),
            "frameMsP95": pct(self.frame_ms, 95),
        }
        if self.gpu is not None:
            ev["gpu"] = dict(self.gpu())
        if self.load is not None:
            ev["load"] = dict(self.load)
        return {k: v for k, v in ev.items() if v is not None}

    def end(self) -> None:
        """Flush, finalize every target with endpoint end, summarise and close."""
        self.feed(self.tail())
        self.finalize("end")
        self.stats(force=True)
        self.send(self.summary())
        self.channel.close(1000, "summary sent")
        self.done = True

    # -------------------------------------------------------------- messages

    def on_start(self, doc: Mapping[str, Any]) -> None:
        if self.started:
            self.send(error("bad-request", "Bad request", 400, "the session already started", fatal=False))
            return
        inp = doc.get("input") if isinstance(doc.get("input"), dict) else {}
        assert isinstance(inp, dict)
        kind = str(inp.get("kind") or self.params.input.kind)
        if kind != self.params.input.kind:
            self.fail(
                "transcription-input-invalid",
                "Transcription input invalid",
                422,
                f"the session was opened for {self.params.input.kind} input, and start says {kind}",
            )
            return
        self.started = True
        if self.params.telephony is not None:
            t = self.params.telephony
            self.tel = Telephony(t.codec, t.sampleRate, SR)
        if kind == "microphone":
            rate = int(inp.get("sampleRate") or SR)
            if not 8000 <= rate <= 192000:
                self.fail("transcription-input-invalid", "Transcription input invalid", 422, f"sample rate {rate}")
                return
            self.capture_rate = rate
            self.res = StreamingResampler(rate, SR)
        started: Event = {
            "type": "started",
            "targets": [self.started_target(d) for d in self.decoders],
            "resampler": RESAMPLER,
            "pace": self.params.pace,
            "input": dict(inp) if inp else {"kind": kind},
        }
        if self.capture_rate:
            started["captureRate"] = self.capture_rate
        if self.tel is not None:
            started["telephony"] = {
                "chain": self.tel.chain,
                "codec": self.tel.codec,
                "sampleRate": self.tel.line_rate,
            }
        if kind == "span":
            try:
                x16, rate = self.read_span()
            except (StepInputError, audio_io.AudioError, OSError) as e:
                self.fail("transcription-input-invalid", "Transcription input invalid", 422, str(e))
                return
            started["captureRate"] = rate
            started["durationS"] = round(x16.size / SR, 3)
            self.send(started)
            self.begin_stream(x16)
            return
        if kind == "file":
            fd, name = tempfile.mkstemp(prefix="live-upload-", dir=self.work)
            self.upload = Path(name)
            self.upload_fh = os.fdopen(fd, "wb")
        self.send(started)

    @staticmethod
    def started_target(d: Decoder) -> Event:
        t: Event = {
            "target": d.target,
            "profile": d.profile,
            "chunkMs": int(d.chunk_ms),
            "language": d.language,
            "loadS": round(float(d.load_s), 2),
            "decoder": d.decoder,
        }
        if d.boost:
            t["boost"] = dict(d.boost)
        return t

    def read_span(self) -> tuple[Audio, int]:
        if self.audio_path is None:
            raise StepInputError("the session has no audio input for its span")
        a = audio_io.read(self.audio_path)
        inp = self.params.input
        x = channel_of(a, inp.channel or 0)
        start = round((inp.start or 0.0) * a.sample_rate)
        end = min(x.size, round(inp.end * a.sample_rate)) if inp.end is not None else x.size
        if start >= end:
            raise StepInputError(
                f"the span {inp.start}-{inp.end} s is empty or outside the audio ({x.size / a.sample_rate:.2f} s)"
            )
        x = x[start:end]
        if x.size / a.sample_rate > self.params.maxFileSeconds:
            raise StepInputError(f"the span is {x.size / a.sample_rate:.0f} s; at most {self.params.maxFileSeconds} s")
        return resample_poly(x, a.sample_rate, SR), a.sample_rate

    def on_binary(self, b: bytes) -> None:
        if not self.started:
            self.send(error("bad-request", "Bad request", 400, "audio before start; it was dropped", fatal=False))
            return
        kind = self.params.input.kind
        if kind == "microphone":
            assert self.res is not None
            self.feed(self.line(self.res.push(pcm16_to_float(b))))
            return
        if kind == "file" and self.upload_fh is not None:
            self.upload_bytes += len(b)
            if self.upload_bytes > self.params.maxFileBytes:
                self.fail(
                    "transcription-input-invalid",
                    "Transcription input invalid",
                    413,
                    f"the file is larger than {self.params.maxFileBytes} bytes",
                    code=1009,
                )
                return
            self.upload_fh.write(b)
            return
        self.send(error("bad-request", "Bad request", 400, f"no audio frames are taken for {kind} input", fatal=False))

    def on_file_end(self) -> None:
        if self.params.input.kind != "file" or self.upload is None or self.upload_fh is None:
            self.send(error("bad-request", "Bad request", 400, "fileEnd without a file", fatal=False))
            return
        if self.pending is not None:
            return
        self.upload_fh.close()
        self.upload_fh = None
        try:
            x, rate = decode_file(self.upload, self.work, self.params.maxFileSeconds)
        except FileRefusedError as e:
            self.fail("transcription-input-invalid", "Transcription input invalid", 422, str(e))
            return
        except (audio_io.AudioError, StepInputError, OSError, subprocess.SubprocessError) as e:
            self.fail("transcription-input-invalid", "Transcription input invalid", 422, f"cannot decode the file: {e}")
            return
        finally:
            self.upload.unlink(missing_ok=True)
        seconds = x.size / rate
        if seconds > self.params.maxFileSeconds:  # a container that did not declare its duration, cut by ffmpeg
            self.fail(
                "transcription-input-invalid",
                "Transcription input invalid",
                422,
                f"the file holds more than {self.params.maxFileSeconds} s of audio; a session takes at most "
                f"{self.params.maxFileSeconds} s",
            )
            return
        self.capture_rate = rate
        self.begin_stream(resample_poly(x, rate, SR))

    def begin_stream(self, x16: Audio) -> None:
        self.pending, self.pos = x16, 0
        self.feed_t0, self.fed_s = self.clock(), 0.0

    def streaming(self) -> bool:
        return self.pending is not None and not self.done

    def wait_s(self) -> float:
        """How long to wait for a message before the next piece of a file or span is due."""
        if not self.streaming():
            return RECV_TIMEOUT_S
        if self.params.pace == "fast":
            return 0.0
        return min(RECV_TIMEOUT_S, max(0.0, self.feed_t0 + self.fed_s - self.clock()))

    def stream_next(self) -> None:
        assert self.pending is not None
        if self.params.pace == "realtime" and self.clock() < self.feed_t0 + self.fed_s:
            return
        step = round((FAST_FRAME_S if self.params.pace == "fast" else PACE_FRAME_S) * SR)
        piece = self.pending[self.pos : self.pos + step]
        self.pos += piece.size
        self.fed_s += piece.size / SR
        self.feed(self.line(piece))
        if self.pos >= self.pending.size:
            self.pending = None
            self.end()

    def on_text(self, raw: str) -> None:
        kind, doc = message_type(raw)
        if kind == "start":
            self.on_start(doc)
        elif kind == "keepalive":
            pong: Event = {"type": "pong", "source": "worker"}
            if isinstance(doc.get("t"), int | float):
                pong["t"] = doc["t"]
            self.send(pong)
        elif kind == "ping":  # the relay answers pings; tolerate one that reached us
            self.send({"type": "pong", "source": "worker"} | ({"t": doc["t"]} if "t" in doc else {}))
        elif not self.started and kind in ("finalize", "fileEnd", "end"):
            if kind == "end":
                self.send(self.summary())
                self.channel.close(1000, "ended before start")
                self.done = True
            else:
                self.send(error("bad-request", "Bad request", 400, f"{kind} before start", fatal=False))
        elif kind == "finalize":
            self.finalize("finalize")
        elif kind == "fileEnd":
            self.on_file_end()
        elif kind == "end":
            self.end()
        else:
            self.send(error("bad-request", "Bad request", 400, f"unknown message type {kind!r}", fatal=False))

    def close_upload(self) -> None:
        if self.upload_fh is not None:
            with contextlib.suppress(OSError):
                self.upload_fh.close()
            self.upload_fh = None
        if self.upload is not None:
            self.upload.unlink(missing_ok=True)


def serve(
    channel: Channel,
    decoders: Sequence[Decoder],
    params: LiveParams,
    *,
    work_dir: Path,
    should_stop: Callable[[], bool] = lambda: False,
    audio_path: Path | None = None,
    clock: Callable[[], float] = time.monotonic,
    gpu: Callable[[], Mapping[str, Any]] | None = None,
    load: Mapping[str, Any] | None = None,
) -> Event | None:
    """Serve one session on ``channel`` until it ends; returns the summary sent, or None when the session stopped
    without one (the socket closed, or the job was told to stop)."""
    s = _Session(channel, decoders, params, work_dir, audio_path, clock, gpu, load)
    summary: Event | None = None
    try:
        while not s.done:
            if should_stop():
                with contextlib.suppress(ChannelClosedError, OSError):
                    s.channel.send(
                        json.dumps(error("lease-ended", "Session stopped", 409, "the job was stopped", fatal=True))
                    )
                    s.channel.close(1001, "the job was stopped")
                return None
            try:
                msg = channel.recv(s.wait_s())
            except ChannelClosedError:
                return None
            if msg is None:
                if s.streaming():
                    s.stream_next()
                if not s.done:
                    s.stats()
                continue
            if isinstance(msg, bytes | bytearray | memoryview):
                s.on_binary(bytes(msg))
            else:
                s.on_text(msg)
            if not s.done:
                s.stats()
        summary = s.summary()
        return summary
    except ChannelClosedError:
        return None
    finally:
        s.close_upload()


# ---------------------------------------------------------------- transport


class WebSocketChannel:
    """A :class:`Channel` over a ``websockets`` sync client connection."""

    def __init__(self, ws: Any) -> None:
        self.ws = ws

    def recv(self, timeout: float) -> str | bytes | None:
        from websockets.exceptions import ConnectionClosed

        try:
            msg = self.ws.recv(timeout=max(0.0, timeout))
        except TimeoutError:
            return None
        except ConnectionClosed as e:
            raise ChannelClosedError(str(e)) from e
        return msg if isinstance(msg, str | bytes) else bytes(msg)

    def send(self, text: str) -> None:
        from websockets.exceptions import ConnectionClosed

        try:
            self.ws.send(text)
        except ConnectionClosed as e:
            raise ChannelClosedError(str(e)) from e

    def close(self, code: int = 1000, reason: str = "") -> None:
        with contextlib.suppress(Exception):
            self.ws.close(code=code, reason=reason[:120])


def dial(url: str | None = None, token: str | None = None, open_timeout: float = 30.0) -> WebSocketChannel:
    """Dial the relay for this lease's job: ``CADENCE_LIVE_URL`` with the lease's ``CADENCE_LIVE_TOKEN``."""
    from websockets.sync.client import connect

    url = url or os.environ.get(URL_ENV, "")
    token = token or os.environ.get(TOKEN_ENV, "")
    if not url or not token:
        raise RuntimeError(f"a live job needs {URL_ENV} and {TOKEN_ENV} (the harness and the lease set them)")
    ws = connect(
        url,
        additional_headers={TOKEN_HEADER: token},
        max_size=MAX_MESSAGE_BYTES,
        open_timeout=open_timeout,
        compression=None,
    )
    return WebSocketChannel(ws)


def live_url(base_url: str, job_id: str) -> str:
    """The relay socket of job_id under the control plane's URL (http → ws, https → wss)."""
    base = base_url.rstrip("/")
    if base.startswith("https://"):
        base = "wss://" + base[len("https://") :]
    elif base.startswith("http://"):
        base = "ws://" + base[len("http://") :]
    return f"{base}/api/worker-live/{job_id}"
