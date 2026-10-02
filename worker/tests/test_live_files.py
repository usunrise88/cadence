"""A file session's upload is decoded defensively (cadence_worker.live.decode_file): probed first and refused when its
container is not an accepted audio one, its declared duration is over the limit or its rate too high; then decoded from
the local file only, cut at the limit + 1 s and capped in output size. A crafted long-silence file (a few KB that
expand to hours) and an HLS playlist are the two attacks the tests replay — with ffmpeg when it is installed, and
against a scripted ffprobe/ffmpeg otherwise, so the command lines are checked everywhere."""

from __future__ import annotations

import json
import shutil
import struct
import subprocess
import wave
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_worker import audio as audio_io
from cadence_worker import live

HAVE_FFMPEG = shutil.which("ffmpeg") is not None and shutil.which("ffprobe") is not None


def silent_wav(path: Path, seconds: float, rate: int = 16000) -> Path:
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(b"\0\0" * int(seconds * rate))
    return path


def lying_wav(path: Path, declared_s: int, rate: int = 16000) -> Path:
    """A WAV whose header declares hours of audio while the file holds a few samples."""
    data = declared_s * rate * 2
    head = (
        b"RIFF" + struct.pack("<I", 36 + data) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16)
    )
    path.write_bytes(head + b"data" + struct.pack("<I", data) + b"\0" * 64)
    return path


# ---------------------------------------------------------------- without ffmpeg


def test_without_ffmpeg_the_declared_duration_is_checked_before_reading(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(live, "_ffmpeg_tools", lambda: None)
    read: list[Path] = []
    monkeypatch.setattr(audio_io, "read", lambda p: read.append(p))
    with pytest.raises(live.FileRefusedError, match="7200 s of audio; a session takes at most 900 s"):
        live.decode_file(lying_wav(tmp_path / "bomb.wav", 7200), tmp_path, 900)
    assert read == [], "the file was read before its duration was checked"
    assert live.file_seconds(silent_wav(tmp_path / "ok.wav", 1.5)) == pytest.approx(1.5)
    (tmp_path / "junk.bin").write_bytes(b"not audio")
    assert live.file_seconds(tmp_path / "junk.bin") is None


# ---------------------------------------------------------------- a scripted ffprobe and ffmpeg


class Tools:
    """Answers ffprobe with a canned probe and plays ffmpeg by writing a short WAV to its output path."""

    def __init__(self, probe: dict[str, Any] | None, probe_rc: int = 0, stderr: str = "") -> None:
        self.probe, self.probe_rc, self.stderr = probe, probe_rc, stderr
        self.calls: list[list[str]] = []

    def run(self, cmd: list[str], **_: Any) -> subprocess.CompletedProcess[str]:
        self.calls.append(cmd)
        if cmd[0] == "ffprobe":
            return subprocess.CompletedProcess(cmd, self.probe_rc, json.dumps(self.probe or {}), self.stderr)
        silent_wav(Path(cmd[-1]), 0.5, 22050)
        return subprocess.CompletedProcess(cmd, 0, "", "")


def scripted(monkeypatch: pytest.MonkeyPatch, tools: Tools) -> None:
    monkeypatch.setattr(live, "_ffmpeg_tools", lambda: ("ffmpeg", "ffprobe"))
    monkeypatch.setattr(subprocess, "run", tools.run)


def test_a_long_declared_duration_is_refused_before_decoding(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = Tools({"format": {"format_name": "flac", "duration": "7200.000000"}, "streams": [{"sample_rate": "8000"}]})
    scripted(monkeypatch, tools)
    upload = tmp_path / "up.bin"
    upload.write_bytes(b"x")
    with pytest.raises(live.FileRefusedError, match="7200 s"):
        live.decode_file(upload, tmp_path, 900)
    assert [c[0] for c in tools.calls] == ["ffprobe"], "ffmpeg ran on a file whose duration was over the limit"
    probe = tools.calls[0]
    assert probe[probe.index("-protocol_whitelist") + 1] == "file"
    assert probe[probe.index("-format_whitelist") + 1] == ",".join(live.AUDIO_FORMATS)
    assert probe[-1] == "file:" + str(upload)


def test_a_playlist_is_refused_by_the_format_whitelist(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = Tools(None, 1, "[hls @ 0x1] Format not on whitelist 'wav,flac'\nfile:up.bin: Invalid argument")
    scripted(monkeypatch, tools)
    upload = tmp_path / "up.bin"
    upload.write_bytes(b"#EXTM3U\n")
    with pytest.raises(audio_io.AudioError, match="accepted container"):
        live.decode_file(upload, tmp_path, 900)
    assert len(tools.calls) == 1


def test_too_high_a_rate_is_refused(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    scripted(
        monkeypatch, Tools({"format": {"format_name": "wav", "duration": "1"}, "streams": [{"sample_rate": "384000"}]})
    )
    (tmp_path / "up.bin").write_bytes(b"x")
    with pytest.raises(live.FileRefusedError, match="384000 Hz"):
        live.decode_file(tmp_path / "up.bin", tmp_path, 900)


def test_the_decode_is_local_cut_and_capped(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = Tools({"format": {"format_name": "mov,mp4,m4a,3gp,3g2,mj2"}, "streams": [{"sample_rate": "22050"}]})
    scripted(monkeypatch, tools)
    upload = tmp_path / "up.m4a"
    upload.write_bytes(b"x")
    x, rate = live.decode_file(upload, tmp_path, 900)
    assert rate == 22050
    assert x.size == 11025
    ffmpeg = tools.calls[1]
    assert ffmpeg[0] == "ffmpeg"
    assert ffmpeg[ffmpeg.index("-protocol_whitelist") + 1] == "file"
    assert ffmpeg[ffmpeg.index("-format_whitelist") + 1] == ",".join(live.AUDIO_FORMATS)
    assert ffmpeg[ffmpeg.index("-i") + 1] == "file:" + str(upload)
    assert ffmpeg[ffmpeg.index("-t") + 1] == "901", "no duration declared: the decode is cut at the limit + 1 s"
    assert int(ffmpeg[ffmpeg.index("-fs") + 1]) == 901 * 22050 * 4 + 4096
    assert not list(tmp_path.glob("*.wav")), "the decoded WAV is deleted"


# ---------------------------------------------------------------- the real ffmpeg


@pytest.mark.skipif(not HAVE_FFMPEG, reason="ffmpeg and ffprobe are not installed")
def test_real_long_silence_file_is_refused_fast(tmp_path: Path) -> None:
    bomb = tmp_path / "silence.flac"
    subprocess.run(
        [
            "ffmpeg",
            "-nostdin",
            "-v",
            "error",
            "-f",
            "lavfi",
            "-i",
            "anullsrc=r=8000:cl=mono",
            "-t",
            "7200",
            "-c:a",
            "flac",
            str(bomb),
        ],
        check=True,
        timeout=120,
    )
    assert bomb.stat().st_size < 2_000_000, "two hours of silence compress to almost nothing"
    with pytest.raises(live.FileRefusedError, match="7200 s"):
        live.decode_file(bomb, tmp_path, 900)


@pytest.mark.skipif(not HAVE_FFMPEG, reason="ffmpeg and ffprobe are not installed")
def test_real_hls_playlist_is_refused(tmp_path: Path) -> None:
    silent_wav(tmp_path / "seg.wav", 1.0)
    playlist = tmp_path / "up.bin"
    playlist.write_text("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nseg.wav\n#EXT-X-ENDLIST\n", encoding="utf-8")
    with pytest.raises(audio_io.AudioError, match="accepted container"):
        live.decode_file(playlist, tmp_path, 900)


@pytest.mark.skipif(not HAVE_FFMPEG, reason="ffmpeg and ffprobe are not installed")
def test_real_decode_of_an_accepted_file(tmp_path: Path) -> None:
    rate = 22050
    tone = (np.sin(2 * np.pi * 220 * np.arange(rate) / rate) * 0.3 * 32767).astype("<i2")
    src = tmp_path / "a.wav"
    with wave.open(str(src), "wb") as w:
        w.setnchannels(2)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(np.repeat(tone, 2).tobytes())
    x, got = live.decode_file(src, tmp_path, 900)
    assert got == rate
    assert x.size == rate
    assert np.max(np.abs(x)) == pytest.approx(0.3, abs=0.01)
