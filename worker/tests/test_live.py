"""The live channel's worker half (cadence_worker.live, R48) over a fake socket and a fake decoder, the streaming
resampler and the phone line (cadence_worker.resample), and the harness's live environment and scratch sweep."""

from __future__ import annotations

import io
import json
import os
import time
import wave
from pathlib import Path
from typing import Any

import numpy as np
import pytest
from scipy import signal

from cadence_worker import live
from cadence_worker.protocol_gen import Lease
from cadence_worker.resample import StreamingResampler, Telephony, resample_poly, telephone
from cadence_worker.serve import lease_env, sweep_scratch

# ---------------------------------------------------------------- the training resampler, streamed


@pytest.mark.parametrize("src", [44100, 48000, 16000, 8000])
def test_streaming_resampler_equals_the_whole_signal(src: int) -> None:
    rng = np.random.default_rng(src)
    x = rng.standard_normal(src * 2).astype(np.float32) * 0.1
    whole = resample_poly(x, src, 16000)
    g = np.gcd(src, 16000)
    assert np.allclose(whole, signal.resample_poly(x.astype(np.float64), 16000 // g, src // g), atol=1e-6)
    r = StreamingResampler(src, 16000)
    parts, i = [], 0
    while i < x.size:
        n = int(rng.integers(1, src // 10))
        parts.append(r.push(x[i : i + n]))
        i += n
    parts.append(r.flush())
    streamed = np.concatenate(parts)
    assert streamed.size == whole.size
    assert np.allclose(streamed, whole, atol=1e-5)


def test_telephony_streamed_equals_the_training_chain() -> None:
    rng = np.random.default_rng(7)
    x = (np.sin(2 * np.pi * 440 * np.arange(16000) / 16000) * 0.3 + rng.standard_normal(16000) * 0.01).astype(
        np.float32
    )
    for codec in ("ulaw", "alaw", "none"):
        t = Telephony(codec)
        parts = [t.push(x[i : i + 320]) for i in range(0, x.size, 320)]
        parts.append(t.flush())
        streamed = np.concatenate(parts)
        whole = telephone(x, codec)
        assert streamed.size == whole.size == x.size
        assert np.allclose(streamed, whole, atol=1e-4), codec
        spec = np.abs(np.fft.rfft(streamed)) ** 2
        assert spec[4300:].sum() < 1e-3 * spec.sum(), "band-limited to the 8 kHz line"
    assert "μ-law" in Telephony("ulaw").chain
    with pytest.raises(ValueError, match="codec"):
        Telephony("amr")  # type: ignore[arg-type]


# ---------------------------------------------------------------- fakes


class FakeChannel:
    """Messages to deliver in order; recv advances a fake clock by the timeout when nothing is queued."""

    def __init__(self, messages: list[str | bytes], *, close_when_empty: bool = True) -> None:
        self.inbox = list(messages)
        self.sent: list[dict[str, Any]] = []
        self.closed: tuple[int, str] | None = None
        self.close_when_empty = close_when_empty
        self.now = 0.0
        self.waits: list[float] = []

    def clock(self) -> float:
        return self.now

    def recv(self, timeout: float) -> str | bytes | None:
        if self.inbox:
            return self.inbox.pop(0)
        if self.close_when_empty and timeout > 0:
            raise live.ChannelClosedError("closed")
        self.waits.append(timeout)
        self.now += timeout
        return None

    def send(self, text: str) -> None:
        self.sent.append(json.loads(text))

    def close(self, code: int = 1000, reason: str = "") -> None:
        self.closed = (code, reason)

    def types(self) -> list[str]:
        return [m["type"] for m in self.sent]


class FakeDecoder:
    """Counts the 16 kHz samples it gets; a partial per 160 ms chunk, a final per finalize."""

    def __init__(self, target: str = "A", profile: str = "160ms") -> None:
        self.target = target
        self.profile = profile
        self.chunk_ms = 160
        self.language = "he-IL"
        self.load_s = 1.5
        self.decoder = "fake"
        self.boost: dict[str, Any] | None = None
        self.step_ms: list[float] = []
        self.samples = 0
        self.pending = 0
        self.segment = 0
        self.seq = 0
        self.audio: list[np.ndarray[Any, Any]] = []

    def push(self, x: np.ndarray[Any, Any]) -> list[dict[str, Any]]:
        self.audio.append(x)
        self.samples += x.size
        self.pending += x.size
        ev = []
        while self.pending >= 2560:
            self.pending -= 2560
            self.step_ms.append(1.0)
            self.seq += 1
            ev.append(
                {
                    "type": "partial",
                    "target": self.target,
                    "segment": self.segment,
                    "seq": self.seq,
                    "text": "w" * (self.samples // 2560),
                    "audioEnd": self.samples / 16000,
                }
            )
        return ev

    def finalize(self, reason: str) -> list[dict[str, Any]]:
        self.seq += 1
        self.pending = 0
        ev = {
            "type": "final",
            "target": self.target,
            "segment": self.segment,
            "seq": self.seq,
            "text": f"{self.samples}",
            "words": [{"word": f"{self.samples}", "start": 0.0, "end": self.samples / 16000, "confidence": 1.0}],
            "endpoint": reason,
            "audioEnd": self.samples / 16000,
            "space": True,
        }
        self.segment += 1
        return [ev]


def params(kind: str = "microphone", **kw: Any) -> live.LiveParams:
    doc: dict[str, Any] = {
        "session": "trs_test",
        "targets": [{"target": "A", "model": "model.0", "profile": "160ms", "language": "he-IL"}],
        "input": {"kind": kind},
        "pace": "fast",
    }
    doc.update(kw)
    return live.LiveParams.model_validate(doc)


def start(kind: str = "microphone", **inp: Any) -> str:
    return json.dumps({"type": "start", "input": {"kind": kind, **inp}})


def msg(kind: str, **kw: Any) -> str:
    return json.dumps({"type": kind, **kw})


def pcm16(x: np.ndarray[Any, Any]) -> bytes:
    return bytes(np.clip(np.round(x * 32768), -32768, 32767).astype("<i2").tobytes())


def wav_bytes(x: np.ndarray[Any, Any], rate: int, channels: int = 1) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(pcm16(x))
    return buf.getvalue()


def run(
    ch: FakeChannel, decoders: list[FakeDecoder], p: live.LiveParams, tmp: Path, **kw: Any
) -> dict[str, Any] | None:
    return live.serve(ch, decoders, p, work_dir=tmp, clock=ch.clock, **kw)


# ---------------------------------------------------------------- the session


def test_microphone_session_in_order(tmp_path: Path) -> None:
    rate = 48000
    tone = (np.sin(2 * np.pi * 300 * np.arange(rate) / rate) * 0.2).astype(np.float32)
    frames: list[str | bytes] = [pcm16(tone[i : i + 960]) for i in range(0, rate, 960)]  # 20 ms frames
    ch = FakeChannel(
        [
            start(sampleRate=rate, frameMs=20),
            *frames[:25],
            msg("keepalive", t=12.5),
            msg("finalize"),
            *frames[25:],
            msg("end"),
        ]
    )
    a, b = FakeDecoder("A"), FakeDecoder("B", "1120ms")
    summary = run(ch, [a, b], params(), tmp_path)
    types = ch.types()
    assert types[0] == "started"
    s = ch.sent[0]
    assert s["captureRate"] == rate
    assert [t["target"] for t in s["targets"]] == ["A", "B"]
    assert s["targets"][1]["profile"] == "1120ms"
    assert s["targets"][0]["chunkMs"] == 160
    assert "polyphase" in s["resampler"]
    assert "telephony" not in s
    # keepalive queues behind the audio sent before it: its pong follows that audio's partials
    pong = types.index("pong")
    assert ch.sent[pong] == {"type": "pong", "source": "worker", "t": 12.5}
    assert all(m["audioEnd"] <= 0.5 + 1e-6 for m in ch.sent[:pong] if m["type"] == "partial")
    finals = [m for m in ch.sent if m["type"] == "final"]
    assert [(f["target"], f["endpoint"]) for f in finals] == [
        ("A", "finalize"),
        ("B", "finalize"),
        ("A", "end"),
        ("B", "end"),
    ]
    assert a.samples == b.samples == 16000, "every target gets the same audio, resampled to 16 kHz, tail included"
    assert np.array_equal(np.concatenate(a.audio), np.concatenate(b.audio))
    assert types[-1] == "summary"
    assert summary == ch.sent[-1]
    assert summary["audioS"] == 1.0
    assert summary["targets"]["A"]["finals"] == 2
    assert summary["targets"]["B"]["profile"] == "1120ms"
    assert ch.closed == (1000, "summary sent")


def test_audio_before_start_and_unknown_messages_are_refused_softly(tmp_path: Path) -> None:
    ch = FakeChannel(
        [b"\x00\x00" * 160, msg("ping", t=1), msg("x"), msg("finalize"), start(sampleRate=16000), msg("end")]
    )
    run(ch, [FakeDecoder()], params(), tmp_path)
    errors = [m for m in ch.sent if m["type"] == "error"]
    assert len(errors) == 3
    assert all(not e["fatal"] for e in errors)
    assert errors[0]["problem"]["type"] == "https://cadence.local/help/errors/bad-request"
    assert {"type": "pong", "source": "worker", "t": 1} in ch.sent
    assert ch.types()[-1] == "summary"


def test_the_input_kind_must_match_the_session(tmp_path: Path) -> None:
    ch = FakeChannel([start("file"), msg("end")])
    assert run(ch, [FakeDecoder()], params("microphone"), tmp_path) is not None
    assert ch.sent[0]["type"] == "error"
    assert ch.sent[0]["fatal"] is True
    assert ch.sent[0]["problem"]["status"] == 422
    assert ch.closed is not None


def test_file_session_streams_and_deletes_the_upload(tmp_path: Path) -> None:
    rate = 22050
    x = (np.sin(2 * np.pi * 200 * np.arange(rate * 2) / rate) * 0.3).astype(np.float32)
    data = wav_bytes(x, rate)
    chunks: list[str | bytes] = [data[i : i + 4096] for i in range(0, len(data), 4096)]
    ch = FakeChannel([start("file", fileName="a.wav", fileBytes=len(data)), *chunks, msg("fileEnd")])
    d = FakeDecoder()
    summary = run(ch, [d], params("file"), tmp_path)
    assert ch.sent[0]["type"] == "started"
    assert d.samples == 32000
    assert [m["endpoint"] for m in ch.sent if m["type"] == "final"] == ["end"]
    assert summary is not None
    assert summary["audioS"] == 2.0
    assert ch.closed == (1000, "summary sent")
    assert list(tmp_path.iterdir()) == [], "the uploaded file is deleted"


def test_file_limits(tmp_path: Path) -> None:
    x = np.zeros(16000 * 3, dtype=np.float32)
    data = wav_bytes(x, 16000)
    ch = FakeChannel([start("file"), data, msg("fileEnd")])
    run(ch, [FakeDecoder()], params("file", maxFileSeconds=2), tmp_path)
    err = ch.sent[-1]
    assert err["type"] == "error"
    assert err["fatal"]
    assert err["problem"]["type"].endswith("/transcription-input-invalid")
    assert "at most 2 s" in err["problem"]["detail"]
    ch = FakeChannel([start("file"), data])
    run(ch, [FakeDecoder()], params("file", maxFileBytes=1000), tmp_path)
    assert ch.sent[-1]["problem"]["status"] == 413
    assert ch.closed is not None
    assert ch.closed[0] == 1009
    ch = FakeChannel([start("file"), b"not audio", msg("fileEnd")])
    run(ch, [FakeDecoder()], params("file"), tmp_path)
    assert "cannot decode" in ch.sent[-1]["problem"]["detail"]
    assert list(tmp_path.iterdir()) == []


def test_span_session_cuts_the_channel_and_plays_at_real_time(tmp_path: Path) -> None:
    rate = 8000
    left = np.full(rate * 2, 0.25, dtype=np.float32)
    right = np.full(rate * 2, -0.5, dtype=np.float32)
    inter = np.stack([left, right], axis=1).reshape(-1)
    (tmp_path / "utt.wav").write_bytes(wav_bytes(inter, rate, channels=2))
    work = tmp_path / "work"
    work.mkdir()
    p = params("span", input={"kind": "span", "start": 0.5, "end": 1.0, "channel": 1}, pace="realtime")
    ch = FakeChannel([start("span")], close_when_empty=False)
    d = FakeDecoder()
    run(ch, [d], p, work, audio_path=tmp_path / "utt.wav")
    assert ch.sent[0]["durationS"] == 0.5
    assert ch.sent[0]["captureRate"] == 8000
    assert d.samples == 8000
    audio = np.concatenate(d.audio)
    assert abs(float(np.median(audio)) + 0.5) < 1e-3, "channel 1"
    # real-time pace: about 0.5 s of waiting in 20 ms steps
    assert 0.45 <= ch.now <= 0.6
    assert ch.types()[-1] == "summary"


def test_telephony_session(tmp_path: Path) -> None:
    rate = 16000
    x = (np.sin(2 * np.pi * 500 * np.arange(rate) / rate) * 0.2).astype(np.float32)
    frames: list[str | bytes] = [pcm16(x[i : i + 320]) for i in range(0, rate, 320)]
    ch = FakeChannel([start(sampleRate=rate), *frames, msg("end")])
    d = FakeDecoder()
    run(ch, [d], params(telephony={"codec": "alaw", "sampleRate": 8000}), tmp_path)
    t = ch.sent[0]["telephony"]
    assert t["codec"] == "alaw"
    assert t["sampleRate"] == 8000
    assert "A-law" in t["chain"]
    assert d.samples == rate
    got = np.concatenate(d.audio)
    assert np.allclose(got, telephone(x, "alaw"), atol=1e-3)


def test_stop_and_closed_socket(tmp_path: Path) -> None:
    calls = {"n": 0}

    def stop_after_two() -> bool:
        calls["n"] += 1
        return calls["n"] > 2

    ch = FakeChannel([start(sampleRate=16000)], close_when_empty=False)
    assert run(ch, [FakeDecoder()], params(), tmp_path, should_stop=stop_after_two) is None
    assert ch.sent[-1]["type"] == "error"
    assert ch.sent[-1]["problem"]["type"].endswith("/lease-ended")
    assert ch.closed is not None
    assert ch.closed[0] == 1001
    ch = FakeChannel([start(sampleRate=16000), b"\x00\x00" * 320])
    assert run(ch, [FakeDecoder()], params(), tmp_path) is None, "a socket closed without end"


def test_stats_and_summary_extras(tmp_path: Path) -> None:
    frames: list[str | bytes] = [b"\x00\x01" * 320 for _ in range(10)]
    ch = FakeChannel([start(sampleRate=16000), *frames], close_when_empty=False)
    ch.inbox.append(msg("end"))
    ticks = iter(range(1_000_000))

    def clock() -> float:
        return float(next(ticks))  # every reading is a second later

    live.serve(
        ch,
        [FakeDecoder()],
        params(),
        work_dir=tmp_path,
        clock=clock,
        gpu=lambda: {"maxAllocatedMb": 3700},
        load={"totalS": 22.1},
    )
    stats = [m for m in ch.sent if m["type"] == "stats"]
    assert stats
    assert stats[-1]["source"] == "worker"
    assert stats[-1]["audioS"] == 0.2
    s = ch.sent[-1]
    assert s["gpu"] == {"maxAllocatedMb": 3700}
    assert s["load"] == {"totalS": 22.1}


def test_check_targets() -> None:
    p = params(targets=[{"target": "A", "model": "model.0", "profile": "160ms", "language": "he-IL"}] * 2)
    with pytest.raises(Exception, match="named twice"):
        live.check_targets(p, {"model.0": Path("x")})
    with pytest.raises(Exception, match="no input"):
        live.check_targets(params(), {})
    with pytest.raises(Exception, match="one to three"):
        live.check_targets(params(targets=[]), {})
    with pytest.raises(Exception, match="audio input"):
        live.check_targets(params("span"), {"model.0": Path("x")})


# ---------------------------------------------------------------- the harness


def lease_of(job_kind: str) -> Lease:
    spec: Any = {"resources": {"gpu": True, "jobKind": job_kind}}
    return {
        "id": "lse_1",
        "jobId": "job_9",
        "spec": spec,
        "card": {"index": 0, "memoryCapMb": 6000},
        "heartbeatSeconds": 10,
    }


def test_lease_env_dials_the_relay_for_interactive_jobs() -> None:
    assert lease_env(lease_of("interactive"), "http://control-plane:8080") == {
        "CADENCE_LIVE_URL": "ws://control-plane:8080/api/worker-live/job_9"
    }
    assert lease_env(lease_of("eval"), "http://cp") == {}
    assert live.live_url("https://cadence.example/", "job_1") == "wss://cadence.example/api/worker-live/job_1"


def test_scratch_sweep_removes_only_stale_unowned_lease_dirs(tmp_path: Path) -> None:
    for name in ("lse_old", "lse_active", "lse_new", "other"):
        (tmp_path / name).mkdir()
    old = time.time() - 7200
    for name in ("lse_old", "lse_active", "other"):
        os.utime(tmp_path / name, (old, old))
    removed = sweep_scratch(tmp_path, {"lse_active"}, 3600)
    assert removed == ["lse_old"]
    assert sorted(p.name for p in tmp_path.iterdir()) == ["lse_active", "lse_new", "other"]
    assert sweep_scratch(tmp_path / "missing", set(), 1) == []


def test_dial_and_serve_over_a_real_websocket(tmp_path: Path) -> None:
    """The transport: live.dial sends the lease's token, and a session runs over a loopback WebSocket relay."""
    import threading

    from websockets.sync.server import ServerConnection, serve

    got: dict[str, Any] = {}

    def relay(ws: ServerConnection) -> None:
        got["token"] = ws.request.headers.get("Cadence-Live-Token") if ws.request else None
        got["path"] = ws.request.path if ws.request else None
        ws.send(start(sampleRate=16000))
        for _ in range(10):
            ws.send(b"\x00\x00" * 320)
        ws.send(msg("keepalive", t=3))
        ws.send(msg("end"))
        got["down"] = []
        try:
            while True:
                got["down"].append(json.loads(ws.recv(timeout=5)))
        except Exception:
            pass

    with serve(relay, "127.0.0.1", 0) as server:
        port = server.socket.getsockname()[1]
        t = threading.Thread(target=server.serve_forever, daemon=True)
        t.start()
        ch = live.dial(live.live_url(f"http://127.0.0.1:{port}", "job_7"), "tok-1")
        summary = live.serve(ch, [FakeDecoder()], params(), work_dir=tmp_path)
        ch.close()
        server.shutdown()
        t.join(timeout=5)
    assert got["token"] == "tok-1"
    assert got["path"] == "/api/worker-live/job_7"
    assert summary is not None
    assert summary["audioS"] == 0.2
    types = [m["type"] for m in got["down"]]
    assert types[0] == "started"
    assert "pong" in types
    assert types[-1] == "summary"
