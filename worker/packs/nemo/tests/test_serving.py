"""nemotron_serve and its stream client without a server: the lease, the deployable, the install, the buffers, the
text, the memory check, and batch and relay mode against an in-process fake of the step graph's server (the
serving fixture's semantics: one token per chunk, ``100 x prompt + n``). ``test_against_triton`` runs the same
fixture on a real Triton (``CADENCE_TEST_TRITON_URL``, marker ``serving``)."""

from __future__ import annotations

import json
import os
import shutil
import threading
import wave
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import numpy as np
import pytest

from cadence_nemo import serving
from cadence_nemo.family import FAMILY, NAME
from cadence_nemo.steps.serve import ServeParams, ServeStep
from cadence_worker import errors
from cadence_worker.cas import Store
from cadence_worker.steps.base import (
    ServingOverCap,
    ServingUnavailable,
    StepInputError,
    TargetDoesNotServe,
    descriptor,
    missing_metadata,
)
from cadence_worker.steps.context import Card, StepContext
from cadence_worker.steps.dataset_import import DatasetImportParams, records, write_dataset

FIXTURE = Path(__file__).resolve().parent / "fixtures" / "serving"
CLIPS = Path(__file__).resolve().parents[1] / "cadence_nemo" / "fixtures"


# ---------------------------------------------------------------- a fake server of the fixture's step graph


class FakeTriton:
    """Health, model control and binary-tensor inference of the fixture graph, with per-sequence state."""

    def __init__(self) -> None:
        self.ready: set[str] = set()
        self.loads: list[str] = []
        self.unloads: list[str] = []
        self.state: dict[int, int] = {}
        self.requests = 0
        self.lock = threading.Lock()
        fake = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:
                return None

            def reply(self, code: int, body: bytes = b"", headers: dict[str, str] | None = None) -> None:
                self.send_response(code)
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self) -> None:
                parts = self.path.strip("/").split("/")
                if self.path == "/v2/health/ready":
                    return self.reply(200)
                if len(parts) == 4 and parts[:2] == ["v2", "models"] and parts[3] == "ready":
                    return self.reply(200 if parts[2] in fake.ready else 400)
                if len(parts) == 4 and parts[3] == "stats":
                    return self.reply(
                        200,
                        json.dumps({"model_stats": [{"name": parts[2], "inference_count": fake.requests}]}).encode(),
                    )
                return self.reply(404)

            def do_POST(self) -> None:
                n = int(self.headers.get("Content-Length") or 0)
                body = self.rfile.read(n)
                parts = self.path.strip("/").split("/")
                if parts[:3] == ["v2", "repository", "models"] and parts[4] == "load":
                    with fake.lock:
                        fake.loads.append(parts[3])
                        fake.ready.add(parts[3])
                    return self.reply(200)
                if parts[:3] == ["v2", "repository", "models"] and parts[4] == "unload":
                    with fake.lock:
                        fake.unloads.append(parts[3])
                        fake.ready.discard(parts[3])
                    return self.reply(200)
                if parts[:2] == ["v2", "models"] and parts[3] == "infer":
                    return self.infer(parts[2], body)
                return self.reply(404)

            def infer(self, model: str, body: bytes) -> None:
                if model not in fake.ready:
                    return self.reply(400, b'{"error":"not ready"}')
                hlen = int(self.headers["Inference-Header-Content-Length"])
                head = json.loads(body[:hlen])
                p = head["parameters"]
                sizes = [int(i["parameters"]["binary_data_size"]) for i in head["inputs"]]
                offs = np.cumsum([hlen, *sizes])
                prompt = int(np.frombuffer(body[offs[2] : offs[3]], np.int64)[0])
                seq = int(p["sequence_id"])
                with fake.lock:
                    fake.requests += 1
                    count = 1 if p.get("sequence_start") else fake.state.get(seq, 0) + 1
                    fake.state[seq] = count
                    if p.get("sequence_end"):
                        fake.state.pop(seq, None)
                toks = np.array([100 * prompt + count, -1], np.int32).tobytes()
                out = json.dumps(
                    {
                        "model_name": model,
                        "outputs": [
                            {
                                "name": "tokens",
                                "datatype": "INT32",
                                "shape": [1, 2],
                                "parameters": {"binary_data_size": len(toks)},
                            }
                        ],
                    }
                ).encode()
                self.reply(
                    200,
                    out + toks,
                    {"Inference-Header-Content-Length": str(len(out)), "Content-Type": "application/octet-stream"},
                )

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def fake() -> Iterator[FakeTriton]:
    f = FakeTriton()
    yield f
    f.close()


def lease_env(url: str, model: str = "cadence-0123456789abcdef", **extra: str) -> dict[str, str]:
    env = {
        serving.ENV_TARGET: "dtg_1",
        serving.ENV_ENDPOINT: url,
        serving.ENV_SERVER: "triton",
        serving.ENV_SERVER_VERSION: "26.08",
        serving.ENV_MODEL: model,
        serving.ENV_MODELS: json.dumps({"deployable": model}),
    }
    env.update(extra)
    return env


def dataset(tmp: Path, n: int = 3) -> Path:
    """The first n fixture clips imported as a dataset (he-IL)."""
    src = tmp / "clips"
    src.mkdir()
    lines = (CLIPS / "metadata.csv").read_text(encoding="utf-8").splitlines()
    (src / "metadata.csv").write_text("\n".join(lines[: n + 1]) + "\n", encoding="utf-8")
    for row in lines[1 : n + 1]:
        name = row.split(",")[0]
        shutil.copy(CLIPS / name, src / name)
    params = DatasetImportParams(
        format="folder-csv",
        path=str(src),
        source_name="fleurs-he-fixtures",
        licence="CC-BY-4.0",
        locale="he-IL",
        split_rule="source",
    )
    out = tmp / "dataset"
    write_dataset(params, records(params), out)
    return out


def clip_seconds(n: int) -> list[float]:
    lines = (CLIPS / "metadata.csv").read_text(encoding="utf-8").splitlines()[1 : n + 1]
    out = []
    for row in lines:
        with wave.open(str(CLIPS / row.split(",")[0])) as w:
            out.append(w.getnframes() / w.getframerate())
    return out


# ---------------------------------------------------------------- the lease and the deployable


def test_lease_from_env_and_check() -> None:
    lease = serving.ServingLease.from_env(lease_env("http://triton:8000/"))
    assert lease.endpoint == "http://triton:8000"
    assert lease.model_for("deployable") == "cadence-0123456789abcdef"
    lease.check()
    with pytest.raises(TargetDoesNotServe, match="delivery target"):
        serving.ServingLease.from_env({serving.ENV_REFUSED: "target-does-not-serve: era is a delivery target"}).check()
    with pytest.raises(ServingUnavailable, match="no staging endpoint"):
        serving.ServingLease.from_env({}).check()
    with pytest.raises(TargetDoesNotServe, match="server kind"):
        serving.ServingLease.from_env(lease_env("http://x:1", **{serving.ENV_SERVER: "vllm"})).check()
    assert serving.physical_card({"CUDA_VISIBLE_DEVICES": "3"}) == 3
    assert serving.physical_card({"CUDA_VISIBLE_DEVICES": ""}) is None


def test_errors_carry_their_help_articles() -> None:
    assert errors.classify(ServingUnavailable("down")) == {
        "type": "step",
        "message": "serving-unavailable: down",
        "retryable": True,
    }
    assert errors.classify(ServingOverCap("big"))["message"] == "serving-over-cap: big"
    assert errors.classify(TargetDoesNotServe("no"))["type"] == "input"


def test_read_deployable_and_geometry() -> None:
    dep = serving.read_deployable(FIXTURE)
    g = dep.geometry
    assert dep.family == NAME
    assert dep.profile == "80ms"
    assert dep.memory_mb == 512
    assert dep.model_dir == FIXTURE / "model"
    assert g.chunk_frames == (1, 8)
    assert g.pre_encode == (0, 9)
    assert g.buffer_frames == 17
    assert g.n_mels == 8
    assert g.hop == 160
    assert g.chunk_ms == 80
    assert g.token_slots == 2
    assert g.prompt("he") == ("he-IL", 3)
    with pytest.raises(StepInputError, match=r"no readable deployable\.json"):
        serving.read_deployable(FIXTURE / "model")


def test_install_names_the_model_and_is_reused(tmp_path: Path) -> None:
    dep = serving.read_deployable(FIXTURE)
    d = serving.install(dep, tmp_path, "cadence-abc")
    assert 'name: "cadence-abc"' in (d / "config.pbtxt").read_text()
    assert (d / "1" / "model.onnx").is_file()
    assert "fixture_step" not in (d / "config.pbtxt").read_text().split("\n", 2)[1]
    (d / "config.pbtxt").write_text("kept")
    assert serving.install(dep, tmp_path, "cadence-abc") == d
    assert (d / "config.pbtxt").read_text() == "kept"
    assert [p.name for p in tmp_path.iterdir()] == ["cadence-abc"]


# ---------------------------------------------------------------- buffers, text, memory


def test_buffers_follow_the_pipeline_decoder() -> None:
    g = serving.read_deployable(FIXTURE).geometry
    make = serving.featurizer_factory(g)
    x = np.random.default_rng(1).standard_normal(16000).astype(np.float32) * 0.1
    whole = serving.utterance_chunks(g, make, x)
    assert whole[0].first
    assert whole[0].length == 1
    assert all(not c.first for c in whole[1:])
    assert all(c.features.shape == (8, 17) for c in whole)
    assert whole[-1].last
    assert sum(c.real for c in whole) == 16000
    assert all(c.length == 17 for c in whole[1:-1])
    # Pushed in pieces as a live stream, the same buffers.
    f = make()
    ch = serving.Chunker(g, f)
    got: list[serving.Chunk] = []
    for i in range(0, 16000, 320):
        f.push(x[i : i + 320])
        got += ch.chunks(final=False)
    f.finish()
    got += ch.chunks(final=True)
    assert len(got) == len(whole)
    for a, b in zip(got, whole, strict=True):
        assert (a.length, a.first, a.last, a.real) == (b.length, b.first, b.last, b.real)
        np.testing.assert_allclose(a.features, b.features)


def test_text_and_words() -> None:
    vocab = ["▁he", "llo", "▁wor", "ld", "▁<he-IL>", "<blank>"]
    assert serving.detokenize([0, 1, 2, 3, 4], vocab) == "hello world"
    words = serving.words_of(
        [0, 1, 2, 3, 4], [(0.0, 0.08), (0.08, 0.16), (0.16, 0.24), (0.24, 0.32), (0.32, 0.4)], vocab
    )
    assert words == [{"word": "hello", "start": 0.0, "end": 0.16}, {"word": "world", "start": 0.16, "end": 0.32}]


def test_ensure_loaded_refuses_a_model_over_its_reservation(fake: FakeTriton) -> None:
    control = serving.Control(fake.url)
    used = iter([1000, 3000])
    with pytest.raises(ServingOverCap, match="took 2000 MB"):
        serving.ensure_loaded(control, "m", reservation_mb=1024, slack_mb=512, timeout_s=5, used_mb=lambda: next(used))
    assert fake.unloads == ["m"]
    used = iter([1000, 2400])
    out = serving.ensure_loaded(
        control, "m", reservation_mb=1024, slack_mb=512, timeout_s=5, used_mb=lambda: next(used)
    )
    assert out["loaded"]
    assert out["tookMb"] == 1400
    # A model the server has ready is not loaded again (another lease brought it).
    assert serving.ensure_loaded(control, "m", reservation_mb=1, slack_mb=0, timeout_s=5, used_mb=lambda: None) == {
        "model": "m",
        "loaded": False,
    }
    assert fake.loads == ["m", "m"]


# ---------------------------------------------------------------- the step


def test_kind_descriptor() -> None:
    d = descriptor("nemotron_serve", ServeStep)
    assert d.get("role") == "serve"
    assert d["resources"].get("gpu") is True
    assert d["resources"].get("memoryGb") == 9
    assert d["consumes"] == {"deployable": "deployable", "data": "dataset", "audio": "audio"}
    assert d["produces"] == {"hypotheses": "hypotheses", "serving_timings": "serving_timings"}
    assert not missing_metadata(ServeStep)
    p = ServeParams()
    assert p.target == "staging"
    assert p.mode == "batch"
    assert p.load_timeout_s == 300
    assert p.over_cap_slack_mb == 512
    assert FAMILY.descriptor["roles"]["serve"] == "nemotron_serve"


def run_batch(
    tmp: Path, fake: FakeTriton, monkeypatch: pytest.MonkeyPatch, **params: Any
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], list[dict[str, Any]]]:
    for k, v in lease_env(fake.url).items():
        monkeypatch.setenv(k, v)
    monkeypatch.setenv(serving.ENV_DIR, str(tmp / "serving"))
    monkeypatch.delenv("CUDA_VISIBLE_DEVICES", raising=False)
    events: list[dict[str, Any]] = []
    out = {"hypotheses": tmp / "hyp.jsonl", "serving_timings": tmp / "timings.jsonl"}
    ctx = StepContext(events.append, work_dir=tmp, card=Card(0, 9216), blob_path=Store(tmp / "cas").path)
    ServeStep().run(ServeParams(target_lang="he-IL", **params), {"deployable": FIXTURE, "data": dataset(tmp)}, out, ctx)
    rows = [json.loads(x) for x in out["hypotheses"].read_text().splitlines()]
    lines = [json.loads(x) for x in out["serving_timings"].read_text().splitlines()]
    return rows, lines, events


def test_batch_fast(tmp_path: Path, fake: FakeTriton, monkeypatch: pytest.MonkeyPatch) -> None:
    rows, lines, events = run_batch(tmp_path, fake, monkeypatch, pace="fast", concurrency=2)
    assert fake.loads == ["cadence-0123456789abcdef"]
    assert (tmp_path / "serving" / "cadence-0123456789abcdef" / "config.pbtxt").is_file()
    secs = clip_seconds(3)
    assert len(rows) == 3
    for row, s in zip(rows, secs, strict=True):
        n = len(row["steps"])
        # One server sequence per utterance: the prompt (he-IL = 3) and the chunk count, from 1, every chunk.
        assert row["tokens"] == [300 + k for k in range(1, n + 1)]
        assert row["text"].split() == [f"w{300 + k}" for k in range(1, n + 1)]
        assert abs(n - (1 + s * 1000 // 80)) <= 2
        assert row["family"] == NAME
        assert row["weightsHash"] == "fixture"
        assert row["decoding"]["decoder"] == serving.DECODER
        assert row["decoding"]["targetLang"] == "he-IL"
        assert row["partials"]
        assert row["partials"][-1]["text"] == row["text"]
    level = lines[-1]
    assert level["type"] == "level"
    assert level["concurrency"] == 2
    assert level["utterances"] == 3
    assert level["errors"] == 0
    assert level["server"] == {"kind": "triton", "version": "26.08"}
    utt = [x for x in lines if x["type"] == "utterance"]
    assert {u["stream"] for u in utt} == {0, 1}
    assert all(len(c) == 4 for u in utt for c in u["chunks"])
    meta = [e for e in events if e.get("e") == "meta"]
    assert any(m["output"] == "hypotheses" and m["meta"]["concurrency"] == 2 for m in meta)


def test_batch_realtime_paces_chunks(tmp_path: Path, fake: FakeTriton, monkeypatch: pytest.MonkeyPatch) -> None:
    _, lines, _ = run_batch(tmp_path, fake, monkeypatch, pace="realtime", concurrency=3)
    for u in (x for x in lines if x["type"] == "utterance"):
        avail = [c[1] for c in u["chunks"]]
        sent = [c[2] for c in u["chunks"]]
        # A chunk leaves when its audio has arrived: never before, and the audio arrives at real time.
        assert all(s >= a - 1 for a, s in zip(avail, sent, strict=True))
        assert avail[-1] - avail[0] >= u["chunks"][-1][0] - u["chunks"][0][0] - 50


def test_relay_lanes_are_served_streams(tmp_path: Path, fake: FakeTriton) -> None:
    g = serving.read_deployable(FIXTURE).geometry
    fake.ready.add("m")
    s = serving.ServedStream(
        target="A",
        client=serving.StepClient(fake.url, "m"),
        geo=g,
        make=serving.featurizer_factory(g),
        prompt=1,
        profile="80ms",
        language="en-US",
    )
    x = np.zeros(16000, np.float32)
    ev = []
    for i in range(0, 16000, 320):  # 20 ms frames, as the page sends them
        ev += s.push(x[i : i + 320])
    ev += s.finalize("end")
    partials = [e for e in ev if e["type"] == "partial"]
    finals = [e for e in ev if e["type"] == "final"]
    assert partials
    assert finals[-1]["endpoint"] == "end"
    assert finals[-1]["audioEnd"] == 1.0
    assert finals[-1]["text"].split()[0] == "w101"
    assert len(s.step_ms) == len(s.tokens)
    s.client.close()


def test_a_server_that_does_not_answer(tmp_path: Path) -> None:
    with pytest.raises(ServingUnavailable, match="does not answer"):
        serving.Control("http://127.0.0.1:9").ready()
    g = serving.read_deployable(FIXTURE).geometry
    c = serving.StepClient("http://127.0.0.1:9", "m", timeout=1)
    with pytest.raises(ServingUnavailable):
        c.infer(1, True, False, np.zeros((g.n_mels, g.buffer_frames), np.float32), 1, 1)


@pytest.mark.serving
def test_against_triton(tmp_path: Path) -> None:
    """The fixture on a real Triton: model control, the sequence batcher's implicit state per stream, the prompt.
    Needs CADENCE_TEST_TRITON_URL and CADENCE_SERVING_DIR (the server's repository, writable here)."""
    url, root = os.environ.get("CADENCE_TEST_TRITON_URL"), os.environ.get(serving.ENV_DIR)
    if not url or not root:
        pytest.skip("no Triton (CADENCE_TEST_TRITON_URL, CADENCE_SERVING_DIR)")
    dep = serving.read_deployable(FIXTURE)
    name = "cadence-fixture" + os.urandom(3).hex()
    serving.install(dep, Path(root), name)
    control = serving.Control(url)
    assert control.ready()
    out = serving.ensure_loaded(control, name, reservation_mb=None, slack_mb=0, timeout_s=60, used_mb=lambda: None)
    assert out["loaded"]
    g = dep.geometry
    make = serving.featurizer_factory(g)
    x = np.random.default_rng(2).standard_normal(16000 * 2).astype(np.float32) * 0.05
    chunks = serving.utterance_chunks(g, make, x)
    results: dict[int, list[int]] = {}

    def stream(i: int) -> None:
        s = serving.ServedStream(
            target="A",
            client=serving.StepClient(url, name),
            geo=g,
            make=make,
            prompt=i % 4,
            profile="80ms",
            language="en-US",
        )
        s.sequence = serving.next_sequence()
        for ch in chunks:
            s.send(ch)
        results[i] = s.tokens
        s.client.close()

    threads = [threading.Thread(target=stream, args=(i,)) for i in range(16)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    for i in range(16):
        assert results[i] == [100 * (i % 4) + k for k in range(1, len(chunks) + 1)], i
    stats = control.stats(name)
    control.unload(name)
    assert stats.get("name") == name
