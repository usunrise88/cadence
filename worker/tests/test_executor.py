from __future__ import annotations

import json
import threading
from pathlib import Path
from typing import Any

import pytest
from helpers import lease, runner

from cadence_worker.cas import Store
from cadence_worker.executor import LeaseRunner, MemorySink
from cadence_worker.protocol_gen import ArtifactRef, MetricPoint
from cadence_worker.tracing import Span, parse


def text_input(store: Store, text: str) -> ArtifactRef:
    return {"hash": store.put_bytes(text.encode()), "type": "text"}


def test_published_outputs_are_stored_sent_in_order_and_removed(tmp_path: Path) -> None:
    r, sink, store = runner(tmp_path, lease("Publisher"))
    out = r.run()
    assert out["state"] == "done", out
    assert [p["name"] for p in sink.published] == ["checkpoint", "checkpoint"]
    first, second = (p["artifact"] for p in sink.published)
    assert first["type"] == second["type"] == "checkpoint"
    assert first["meta"] == {"step": 10, "valWer": 0.5, "layout": "dir"}
    assert second["meta"] == {"step": 20, "valWer": 0.4, "layout": "file"}
    assert sink.published[0].get("metrics") == {"val_wer": 0.5}
    assert "metrics" not in sink.published[1]
    assert store.path(second["hash"]).read_text() == "weights at 20"
    assert [f.path for f in store.read_manifest(first["hash"])] == ["weights.bin"]
    # The final outputs are still the release's; the bad publication is a warning in the log.
    assert store.path(out["outputs"]["checkpoint"]["hash"]).read_text() == "weights at 30"
    assert any(line["level"] == "warn" and "hypotheses" in line["msg"] for line in sink.logs)


def test_echo_runs_through_the_step_process(tmp_path: Path) -> None:
    store = Store(tmp_path / "cas")
    ref = text_input(store, "shalom")
    r, sink, store = runner(tmp_path, lease("echo", params={"prefix": "> "}, inputs={"text": ref}))
    out = r.run()
    assert out["state"] == "done", out
    got = out["outputs"]["text"]
    assert got["type"] == "text"
    assert got["size"] == 8
    assert got["meta"] == {"layout": "file"}
    assert store.path(got["hash"]).read_text() == "> shalom"
    assert any(line["msg"] == "echoed" for line in sink.logs)
    assert sink.progresses[-1] == (1.0, "done")
    assert not (tmp_path / "scratch" / "lse_test").exists()


def test_bad_params_are_an_input_error(tmp_path: Path) -> None:
    store = Store(tmp_path / "cas")
    r, _, _ = runner(tmp_path, lease("echo", params={"prefix": "x" * 65}, inputs={"text": text_input(store, "a")}))
    out = r.run()
    assert out["state"] == "failed"
    assert out["error"]["type"] == "input"


def test_unknown_kind_version_is_refused(tmp_path: Path) -> None:
    le = lease("echo")
    le["spec"]["kindVersion"] = "9"
    r, _, _ = runner(tmp_path, le)
    out = r.run()
    assert out["state"] == "failed"
    assert out["error"]["type"] == "input"
    assert "echo@9" in out["error"]["message"]


def test_missing_input_blob_is_an_input_error(tmp_path: Path) -> None:
    ref: ArtifactRef = {"hash": "b3:" + "0" * 64, "type": "text"}
    r, _, _ = runner(tmp_path, lease("echo", inputs={"text": ref}))
    out = r.run()
    assert out["state"] == "failed"
    assert out["error"]["type"] == "input"


def test_card_oom_is_typed_and_retryable(tmp_path: Path) -> None:
    r, sink, _ = runner(tmp_path, lease("CardOom"))
    out = r.run()
    assert out["state"] == "failed"
    assert out["error"]["type"] == "oom"
    assert out["error"]["retryable"] is True
    assert any(line["level"] == "error" and "OutOfMemoryError" in line["msg"] for line in sink.logs)


def test_a_step_that_forgets_its_output_fails(tmp_path: Path) -> None:
    out = runner(tmp_path, lease("Forgetful"))[0].run()
    assert out["state"] == "failed"
    assert out["error"]["type"] == "step"
    assert "did not write" in out["error"]["message"]


class StopOnFirstMetric(MemorySink):
    def __init__(self) -> None:
        super().__init__()
        self.stop: threading.Event = threading.Event()

    def metric(self, point: MetricPoint) -> None:
        super().metric(point)
        self.stop.set()


def stop_when_measured(sink: StopOnFirstMetric, r: LeaseRunner, reason: str) -> None:
    def watch() -> None:
        if sink.stop.wait(30):
            r.stop(reason)

    threading.Thread(target=watch, daemon=True).start()


def test_stop_releases_cancelled_with_the_training_state_only(tmp_path: Path) -> None:
    sink = StopOnFirstMetric()
    r, _, store = runner(tmp_path, lease("SlowTrain", params={"seconds": 30}), sink=sink)
    stop_when_measured(sink, r, "cancelled")
    out = r.run()
    assert out["state"] == "cancelled", out
    assert out["error"]["type"] == "cancelled"
    assert out["error"]["message"] == "cancelled"
    assert set(out["outputs"]) == {"state"}
    state = out["outputs"]["state"]
    assert state["type"] == "training-state"
    assert state["meta"] == {"step": 1, "layout": "dir"}
    assert [f.path for f in store.read_manifest(state["hash"])] == ["state.json"]
    assert out["metrics"] == {"loss": 1.0}


def test_a_step_ignoring_the_stop_is_killed_after_the_grace(tmp_path: Path) -> None:
    sink = StopOnFirstMetric()
    r, _, _ = runner(tmp_path, lease("Stubborn"), sink=sink, stop_grace=0.5)
    stop_when_measured(sink, r, "paused")
    out = r.run()
    assert out["state"] == "cancelled"
    assert "outputs" not in out
    assert out["error"]["message"] == "paused"


def test_secrets_reach_only_the_step_and_are_redacted(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("CADENCE_WORKER_TOKEN_FILE", "/worker-credential/token")
    r, sink, store = runner(tmp_path, lease("EnvProbe", env={"HF_TOKEN": "hf_supersecret"}, card_index=3, cap_mb=2048))
    out = r.run()
    assert out["state"] == "done", out
    seen = json.loads(store.path(out["outputs"]["env"]["hash"]).read_text())
    assert seen["HF_TOKEN"] == "hf_supersecret"
    assert seen["CUDA_VISIBLE_DEVICES"] == "3"
    assert seen["CADENCE_MEMORY_CAP_MB"] == "2048"
    assert seen["CADENCE_WORKER_TOKEN_FILE"] is None
    assert json.loads(seen["card"]) == {"index": 0, "cap": 2048}
    dumped = json.dumps(sink.logs)
    assert "hf_supersecret" not in dumped
    assert "token is [redacted]" in dumped
    assert '"token": "[redacted]"' in dumped


def test_non_finite_numbers_never_leave_the_worker(tmp_path: Path) -> None:
    r, sink, _ = runner(tmp_path, lease("NonFinite"))
    out = r.run()
    assert out["state"] == "done", out
    # Points: only the finite one, without its NaN epoch.
    assert [(p["name"], p["value"], p.get("step"), "epoch" in p) for p in sink.metrics] == [("loss", 0.5, 20, False)]
    warnings = [line["msg"] for line in sink.logs if line["level"] == "warn"]
    assert sum("'val_wer'" in w for w in warnings) == 1  # once per name, not per point
    assert sum("'grad_norm'" in w for w in warnings) == 1
    assert any("val_wer" in w and "published checkpoint" in w for w in warnings)
    assert any("seconds_per_step" in w and "outcome" in w for w in warnings)
    assert sink.progresses == [(0.0, "validating")]
    [published] = sink.published
    assert published.get("metrics") == {"loss": 0.5}
    assert published["artifact"].get("meta") == {"step": 20, "valWer": None, "curve": [1.0, None], "layout": "file"}
    assert out.get("metrics") == {"loss": 0.5}
    assert out["outputs"]["checkpoint"].get("meta") == {"step": 30, "valWer": None, "layout": "file"}
    for doc in (out, sink.published, sink.metrics, sink.logs, sink.progresses):
        json.dumps(doc, allow_nan=False)


def test_error_meta_and_progress_are_redacted(tmp_path: Path) -> None:
    secret = "hf_leaky42"
    r, sink, _ = runner(tmp_path, lease("Leaky", env={"HF_TOKEN": secret}))
    out = r.run()
    assert out["state"] == "failed", out
    msg = out["error"]["message"]
    assert "token=[redacted]" in msg
    assert "Bearer [redacted]" in msg
    assert "agent [redacted], key [redacted]" in msg
    assert sink.progresses == [(0.5, "downloading with [redacted]")]
    [published] = sink.published
    assert published["artifact"].get("meta") == {
        "source": "https://user:[redacted]@hub.example/model",
        "note": ["[redacted]"],
        "layout": "file",
    }
    fields = next(line.get("fields") or {} for line in sink.logs if line["msg"] == "a token")
    assert fields["agent"] == 'say "[redacted]"'
    assert fields["[redacted]"] == "as a key"
    everything = json.dumps([out, sink.published, sink.logs, sink.progresses])
    for leaked in (secret, "cst_AbCd", "cdk_0123", "abc.def"):
        assert leaked not in everything


def test_cpu_steps_are_kept_off_the_card(tmp_path: Path) -> None:
    le = lease("EnvProbe")
    le["spec"]["resources"] = {"gpu": False}
    r, _, store = runner(tmp_path, le)
    out = r.run()
    seen = json.loads(store.path(out["outputs"]["env"]["hash"]).read_text())
    assert seen["CUDA_VISIBLE_DEVICES"] == ""
    assert seen["CADENCE_MEMORY_CAP_MB"] is None
    assert json.loads(seen["card"]) is None


def test_the_step_runs_in_a_span_under_the_lease_trace(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """The step gets TRACEPARENT naming its own span (a child of the lease's job span), its log lines carry the
    trace and span ids, and the finished span lands in CADENCE_WORKER_TRACE_FILE."""
    trace_file = tmp_path / "traces.jsonl"
    monkeypatch.setenv("CADENCE_WORKER_TRACE_FILE", str(trace_file))
    le = lease("EnvProbe")
    trace_id, job_span = le["traceparent"].split("-")[1:3]
    r, sink, store = runner(tmp_path, le)
    out = r.run()
    assert out["state"] == "done", out
    seen = json.loads(store.path(out["outputs"]["env"]["hash"]).read_text())
    _, t, step_span, _ = seen["TRACEPARENT"].split("-")
    assert t == trace_id
    assert step_span != job_span
    assert sink.logs
    for line in sink.logs:
        assert line.get("fields", {}).get("trace_id") == trace_id, line
        assert line.get("fields", {}).get("span_id") == step_span, line
    (span,) = [json.loads(x) for x in trace_file.read_text().splitlines()]
    assert span["SpanContext"] == {"TraceID": trace_id, "SpanID": step_span, "TraceFlags": "01"}
    assert span["Parent"]["SpanID"] == job_span
    assert span["Name"] == "step EnvProbe@1"
    assert span["Status"]["Code"] == "Ok"


def test_a_lease_without_a_trace_starts_one() -> None:
    s = Span.child_of("garbage", "step x@1")
    assert s.parent_id is None
    assert parse(s.traceparent) == (s.trace_id, s.span_id, "01")
    assert parse("00-" + "0" * 32 + "-b7ad6b7169203331-01") is None


def test_an_optional_output_may_stay_unwritten(tmp_path: Path) -> None:
    out = runner(tmp_path, lease("SkipsOptional"))[0].run()
    assert out["state"] == "done", out
    assert set(out.get("outputs") or {}) == {"out"}


def test_a_lingering_library_thread_does_not_hold_the_lease(tmp_path: Path) -> None:
    done: dict[str, Any] = {}
    t = threading.Thread(target=lambda: done.update(runner(tmp_path, lease("Lingering"))[0].run()), daemon=True)
    t.start()
    t.join(timeout=60)
    assert not t.is_alive(), "the step process never exited: a non-daemon thread held it"
    assert done["state"] == "done", done
