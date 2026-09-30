"""The framework-pack conformance suite (R45), parameterised by runtime.

Schema checks first (x-cadence complete, help present, profiles declared, every role mapped to a published kind of
the pack whose ``role`` matches), then the flow on the pack's fixtures through the real harness path (LeaseRunner →
``python -m cadence_worker.run_step``) with a local content store and no control plane:

    calibrate → train a few steps → stop (training-state on cancel) → resume → average → transcribe (file and
    streaming profiles) → score

Export and parity join in phase 5. Contracts a pack must meet beyond the schemas: the transcribe kind takes a
``profile`` parameter naming a latency profile; the train kind resumes from ``overrides.resumeFrom``; checkpoints carry
the neutral meta family, step, valWer and weightsHash.
"""

from __future__ import annotations

import json
import tempfile
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from cadence_worker.cas import Store
from cadence_worker.executor import LeaseRunner, MemorySink
from cadence_worker.protocol_gen import ArtifactRef, Lease, MetricPoint, StepOutcome, StepSpec
from cadence_worker.registry import Family, KindEntry, load_families, load_kinds
from cadence_worker.scoring import wer
from cadence_worker.steps.base import help_slug, missing_metadata, role_of

REQUIRED_ROLES = ("calibrate", "train", "average", "transcribe")
LATER_ROLES = {"export": "joins in phase 5", "parity": "joins in phase 5"}
CHECKPOINT_META = ("family", "step", "valWer", "weightsHash")
HYPOTHESIS_FIELDS = ("text", "words", "decoding", "decodingHash", "family", "weightsHash")
REPO_HELP = Path(__file__).resolve().parents[3] / "docs" / "help"


class ConformanceError(Exception):
    pass


@dataclass
class Stage:
    name: str
    ok: bool
    seconds: float = 0.0
    detail: dict[str, Any] = field(default_factory=dict)


@dataclass
class Report:
    runtime: str
    stages: list[Stage] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return bool(self.stages) and all(s.ok for s in self.stages)

    def to_json(self) -> dict[str, Any]:
        return {
            "runtime": self.runtime,
            "ok": self.ok,
            "stages": [
                {"name": s.name, "ok": s.ok, "seconds": round(s.seconds, 3), "detail": s.detail} for s in self.stages
            ],
        }


def _help_file(slug: str, help_dir: Path | None) -> Path | None:
    if help_dir is None or "." not in slug:
        return None
    section, _, name = slug.partition(".")
    return help_dir / section / f"{name}.md"


def check_schemas(
    runtime: str, kinds: Mapping[str, KindEntry], families: list[Family], help_dir: Path | None
) -> list[str]:
    """Problems with the pack's publication (empty when it conforms)."""
    problems: list[str] = []
    if not families:
        problems.append(f"runtime {runtime!r} publishes no model family")
    for name, entry in sorted(kinds.items()):
        if bad := missing_metadata(entry.cls):
            problems.append(f"step kind {name}: parameters without complete x-cadence: {bad}")
        f = _help_file(help_slug(name), help_dir)
        if f is not None and not f.is_file():
            problems.append(f"step kind {name}: help article {f} is missing")
        for n, t in {**entry.cls.consumes, **entry.cls.produces}.items():
            if not t:
                problems.append(f"step kind {name}: {n} has no artifact type")
    for fam in families:
        d = fam.descriptor
        fname = d["name"]
        profiles = d.get("latencyProfiles") or []
        names = [p["name"] for p in profiles]
        if not profiles:
            problems.append(f"family {fname}: no latency profiles")
        if len(set(names)) != len(names):
            problems.append(f"family {fname}: duplicate latency profile names")
        if (d.get("capabilities") or {}).get("streaming") and not any(p.get("chunkMs") for p in profiles):
            problems.append(f"family {fname}: streaming capability without a streaming (chunkMs) profile")
        roles = d.get("roles") or {}
        for role in REQUIRED_ROLES:
            if role not in roles:
                problems.append(f"family {fname}: role {role} is not mapped")
        for role, kind in roles.items():
            mapped = kinds.get(kind)
            if mapped is None:
                problems.append(f"family {fname}: role {role} maps to {kind}, which runtime {runtime} does not publish")
            elif role_of(mapped.cls) != role:
                problems.append(f"family {fname}: {kind} fills role {role!r} but declares {role_of(mapped.cls)!r}")
        if not d.get("help"):
            problems.append(f"family {fname}: no help slug")
        elif (f := _help_file(str(d["help"]), help_dir)) is not None and not f.is_file():
            problems.append(f"family {fname}: help article {f} is missing")
        if fam.fixtures is None or not (fam.fixtures / "manifest.jsonl").is_file():
            problems.append(f"family {fname}: no conformance fixtures (manifest.jsonl)")
    return problems


class StopAfterFirstMetric(MemorySink):
    """Asks the runner to stop once the step reported its first metric (the stop stage)."""

    def __init__(self) -> None:
        super().__init__()
        self.on_first: Callable[[], None] | None = None

    def metric(self, point: MetricPoint) -> None:
        super().metric(point)
        if self.on_first is not None:
            cb, self.on_first = self.on_first, None
            cb()


class Flow:
    def __init__(self, runtime: str, kinds: Mapping[str, KindEntry], work: Path, stop_grace: float = 60.0) -> None:
        self.runtime = runtime
        self.kinds = kinds
        self.store = Store(work / "cas")
        self.scratch = work / "scratch"
        self.stop_grace = stop_grace
        self.n = 0

    def lease(
        self,
        kind: str,
        params: Mapping[str, Any],
        inputs: Mapping[str, ArtifactRef],
        overrides: Mapping[str, Any] | None = None,
    ) -> Lease:
        self.n += 1
        cls = self.kinds[kind].cls
        spec: StepSpec = {
            "stepId": f"pls_conformance{self.n}",
            "pipelineRunId": "plr_conformance",
            "kind": kind,
            "kindVersion": cls.version,
            "params": dict(params),
            "inputs": dict(inputs),
            "outputs": dict(cls.produces),
            "resources": dict(cls.resources),  # type: ignore[typeddict-item]
            "attempt": 1,
        }
        if overrides:
            spec["overrides"] = dict(overrides)  # type: ignore[typeddict-item]
        return {
            "id": f"lse_conformance{self.n}",
            "jobId": f"job_conformance{self.n}",
            "spec": spec,
            "inputs": {k: "cas://" + v["hash"] for k, v in inputs.items()},
            "card": {"index": 0, "memoryCapMb": 0},
            "heartbeatSeconds": 10,
        }

    def run(
        self,
        kind: str,
        params: Mapping[str, Any],
        inputs: Mapping[str, ArtifactRef],
        overrides: Mapping[str, Any] | None = None,
        sink: MemorySink | None = None,
    ) -> tuple[StepOutcome, MemorySink]:
        sink = sink or MemorySink()
        runner = LeaseRunner(
            self.lease(kind, params, inputs, overrides),
            kinds=self.kinds,
            store=self.store,
            scratch=self.scratch,
            sink=sink,
            stop_grace=self.stop_grace,
        )
        if isinstance(sink, StopAfterFirstMetric):
            sink.on_first = lambda: runner.stop("conformance: stop after the first metric")
        return runner.run(), sink

    def ingest(self, fixtures: Path) -> tuple[ArtifactRef, dict[str, str]]:
        """The fixtures as a ``dataset`` artifact: a header line, then one line per utterance with its audio hash."""
        rows = [
            json.loads(line) for line in (fixtures / "manifest.jsonl").read_text(encoding="utf-8").splitlines() if line
        ]
        refs: dict[str, str] = {}
        lines = [json.dumps({"source": {"name": "conformance-fixtures", "licence": "CC0-1.0", "kind": "synthetic"}})]
        for r in rows:
            h, _ = self.store.put_file(fixtures / r["audio"])
            refs[h] = r["text"]
            lines.append(
                json.dumps(
                    {
                        "audio": h,
                        "text": r["text"],
                        "language": r.get("language", "und"),
                        "origin": "fixture",
                        "split": r.get("split", "train"),
                    }
                )
            )
        h = self.store.put_bytes(("\n".join(lines) + "\n").encode())
        return {"hash": h, "type": "dataset", "meta": {"layout": "file"}}, refs

    def read_json(self, ref: ArtifactRef) -> Any:
        return json.loads(self.store.path(ref["hash"]).read_bytes())

    def read_lines(self, ref: ArtifactRef) -> list[dict[str, Any]]:
        return [json.loads(x) for x in self.store.path(ref["hash"]).read_text(encoding="utf-8").splitlines() if x]


def _expect_done(outcome: StepOutcome, what: str) -> dict[str, ArtifactRef]:
    if outcome["state"] != "done":
        raise ConformanceError(f"{what}: {outcome['state']}: {outcome.get('error')}")
    return outcome.get("outputs") or {}


def _by_type(outputs: Mapping[str, ArtifactRef], typ: str, what: str) -> ArtifactRef:
    for ref in outputs.values():
        if ref["type"] == typ:
            return ref
    raise ConformanceError(f"{what}: no {typ} output")


def run_family(flow: Flow, fam: Family, report: Report) -> None:
    d = fam.descriptor
    roles = d["roles"]
    conf = fam.conformance
    assert fam.fixtures is not None
    prefix = d["name"]

    def stage(name: str, fn: Callable[[], dict[str, Any]]) -> bool:
        t0 = time.monotonic()
        try:
            detail = fn()
            report.stages.append(Stage(f"{prefix}/{name}", True, time.monotonic() - t0, detail))
            return True
        except Exception as e:
            report.stages.append(Stage(f"{prefix}/{name}", False, time.monotonic() - t0, {"error": str(e)}))
            return False

    data, refs = flow.ingest(fam.fixtures)
    state: dict[str, Any] = {}

    def calibrate() -> dict[str, Any]:
        out, _ = flow.run(roles["calibrate"], conf.get("calibrate", {}), {"data": data})
        cal = _by_type(_expect_done(out, "calibrate"), "calibration", "calibrate")
        doc = flow.read_json(cal)
        if not (isinstance(doc, dict) and float(doc.get("secondsPerStep", 0)) > 0 and int(doc.get("batchSize", 0)) > 0):
            raise ConformanceError(f"calibration lacks secondsPerStep/batchSize: {doc}")
        return {"secondsPerStep": doc["secondsPerStep"], "batchSize": doc["batchSize"]}

    def check_checkpoint(ref: ArtifactRef, what: str) -> dict[str, Any]:
        meta = ref.get("meta") or {}
        missing = [k for k in CHECKPOINT_META if meta.get(k) is None]
        if missing or meta.get("family") != d["name"]:
            raise ConformanceError(f"{what}: checkpoint meta {meta} lacks {missing} or names another family")
        return meta

    def train() -> dict[str, Any]:
        out, sink = flow.run(roles["train"], conf.get("train", {}), {"data": data})
        outputs = _expect_done(out, "train")
        ck = _by_type(outputs, "checkpoint", "train")
        state["ck1"], state["ts1"] = ck, _by_type(outputs, "training-state", "train")
        names = {p["name"] for p in sink.metrics}
        if not {"loss", "val_wer"} <= names:
            raise ConformanceError(f"train posted metrics {sorted(names)}, expected loss and val_wer")
        meta = check_checkpoint(ck, "train")
        losses = [p["value"] for p in sink.metrics if p["name"] == "loss"]
        return {"step": meta["step"], "valWer": meta["valWer"], "firstLoss": losses[0], "lastLoss": losses[-1]}

    def stop() -> dict[str, Any]:
        out, _ = flow.run(roles["train"], conf.get("stop", {}), {"data": data}, sink=StopAfterFirstMetric())
        if out["state"] != "cancelled":
            raise ConformanceError(f"a stopped train step ended {out['state']}, expected cancelled")
        outputs = out.get("outputs") or {}
        ts = _by_type(outputs, "training-state", "stop")
        if any(r["type"] != "training-state" for r in outputs.values()):
            raise ConformanceError("a cancelled step released outputs other than its training-state")
        return {"trainingState": ts["hash"], "meta": ts.get("meta")}

    def resume() -> dict[str, Any]:
        out, _ = flow.run(roles["train"], conf.get("resume", {}), {"data": data}, {"resumeFrom": state["ts1"]["hash"]})
        ck = _by_type(_expect_done(out, "resume"), "checkpoint", "resume")
        meta = check_checkpoint(ck, "resume")
        if int(meta["step"]) <= int((state["ck1"].get("meta") or {})["step"]):
            raise ConformanceError(f"the resumed run ended at step {meta['step']}, not after the first run")
        state["ck2"] = ck
        return {"step": meta["step"], "valWer": meta["valWer"]}

    def average() -> dict[str, Any]:
        out, _ = flow.run(
            roles["average"], conf.get("average", {}), {"checkpoints.0": state["ck1"], "checkpoints.1": state["ck2"]}
        )
        ck = _by_type(_expect_done(out, "average"), "checkpoint", "average")
        meta = ck.get("meta") or {}
        if meta.get("family") != d["name"] or not meta.get("weightsHash"):
            raise ConformanceError(f"averaged checkpoint meta {meta}")
        state["avg"] = ck
        return {"weightsHash": meta["weightsHash"]}

    def transcribe(profile: Mapping[str, Any]) -> Callable[[], dict[str, Any]]:
        def run() -> dict[str, Any]:
            params = {**conf.get("transcribe", {}), "profile": profile["name"]}
            out, _ = flow.run(roles["transcribe"], params, {"model": state["avg"], "data": data})
            hyp = _by_type(_expect_done(out, f"transcribe {profile['name']}"), "hypotheses", "transcribe")
            lines = flow.read_lines(hyp)
            if len(lines) != len(refs):
                raise ConformanceError(f"{len(lines)} hypotheses for {len(refs)} utterances")
            for row in lines:
                if missing := [k for k in HYPOTHESIS_FIELDS if k not in row]:
                    raise ConformanceError(f"hypothesis lacks {missing}")
                if profile.get("chunkMs"):
                    partials = row.get("partials") or []
                    offsets = [p["audioOffsetMs"] for p in partials]
                    if (
                        not partials
                        or offsets != sorted(offsets)
                        or any("emitMs" not in p or "text" not in p for p in partials)
                    ):
                        raise ConformanceError("a streaming hypothesis lacks ordered partial events")
            score = wer((" ".join(refs[r["audio"]].lower().split()), str(r["text"])) for r in lines)
            state.setdefault("wer", {})[profile["name"]] = score
            return {"wer": round(score, 4), "utterances": len(lines)}

        return run

    ok = stage("calibrate", calibrate)
    ok = stage("train", train) and ok
    if ok:
        stage("stop", stop)
        if stage("resume", resume) and stage("average", average):
            for p in d["latencyProfiles"]:
                stage(f"transcribe:{p['name']}", transcribe(p))
            stage("score", lambda: {"wer": {k: round(v, 4) for k, v in state.get("wer", {}).items()}})
    for role, why in LATER_ROLES.items():
        report.stages.append(Stage(f"{prefix}/{role}", True, 0.0, {"skipped": why}))


def run(runtime: str, *, help_dir: Path | None = None, work: Path | None = None, stop_grace: float = 60.0) -> Report:
    report = Report(runtime)
    kinds = load_kinds(runtime)
    families = load_families(runtime)
    if help_dir is None and REPO_HELP.is_dir():
        help_dir = REPO_HELP
    t0 = time.monotonic()
    problems = check_schemas(runtime, kinds, families, help_dir)
    report.stages.append(Stage("schemas", not problems, time.monotonic() - t0, {"problems": problems}))
    if problems:
        return report
    with tempfile.TemporaryDirectory(prefix="cadence-conformance-", dir=work) as tmp:
        flow = Flow(runtime, kinds, Path(tmp), stop_grace=stop_grace)
        for fam in families:
            run_family(flow, fam, report)
    return report
