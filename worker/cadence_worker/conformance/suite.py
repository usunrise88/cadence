"""The framework-pack conformance suite (R45), parameterised by runtime.

Schema checks first (x-cadence complete, help present, profiles declared, every role mapped to a published kind of
the pack whose ``role`` matches), then the flow on the pack's fixtures through the real harness path (LeaseRunner →
``python -m cadence_worker.run_step``) with a local content store and no control plane, starting from a
``dataset_import`` of the pack's fixtures (a ``folder-csv`` folder):

    import → calibrate → train a few steps → stop (training-state on cancel) → resume → average → transcribe (file and
    streaming profiles, each scored) → baseline → materialize the base model → transcribe it → score

Every transcription is scored by the neutral ``wer_score`` kind (the eval pipeline's scorer) with the suite's
normalizer, and its ``scores`` artifact is checked against ``cadence.scores/1``. ``baseline`` trains with the family's
``conformance["baseline"]`` parameters (one step, say) and transcribes with the nearly untrained checkpoint; ``score``
then requires the trained, averaged model to beat it on every profile, and to stay under
``conformance["score"]["maxWer"]`` when the family declares one. A family without ``baseline`` has that check reported
as skipped. The base model's WER (materialize role → transcribe at the kind's default profile → score) is reported.

Export and parity join in phase 5. Contracts a pack must meet beyond the schemas: the transcribe kind takes a
``profile`` parameter naming a latency profile; the train kind resumes from ``overrides.resumeFrom``; checkpoints carry
the neutral meta family, step, valWer and weightsHash (a materialized base model's valWer may be null).

Inputs are filled by declared artifact type, as the control plane fills a run's pipeline inputs: ``dataset`` (the
imported fixtures), ``mix`` (a ``cadence.mix/1`` rendered over that dataset), ``base_model`` (a
``cadence.base_model/1`` from the family's ``conformance["base_model"]``), ``calibration`` (the calibrate stage's
output), ``checkpoint`` (the stage's model) and ``normalizer``; optional inputs the flow cannot fill (a boost list)
stay unwired.
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
from cadence_worker.steps.base import help_slug, missing_metadata, role_of

REQUIRED_ROLES = ("calibrate", "train", "average", "transcribe", "materialize")
LATER_ROLES = {"export": "joins in phase 5", "parity": "joins in phase 5"}
CHECKPOINT_META = ("family", "step", "valWer", "weightsHash")
HYPOTHESIS_FIELDS = ("text", "words", "decoding", "decodingHash", "family", "weightsHash")
SCORES_SUMMARY = ("schema", "scorer", "utterances", "refWords", "wer", "cer", "werNoPunct", "sub", "del", "ins")
SCORES_ROW = ("audio", "group", "durationS", "ref", "hyp", "refWords", "sub", "del", "ins", "ops")
IMPORT_KIND = "dataset_import"
SCORE_KIND = "wer_score"
# The suite's scoring normalizer: NFKC, case-folded, punctuation stripped (a family's cased, punctuated output is
# compared with the fixtures' references on words alone).
NORMALIZER: Mapping[str, Any] = {
    "locale": "*",
    "unicode": "NFKC",
    "casefold": True,
    "punctuation": "strip",
    "removeMarks": False,
    "mappings": [],
    "numbers": "keep",
    "versionId": "ver_conformance",
}
IMPORT_PARAMS: Mapping[str, Any] = {
    "format": "folder-csv",
    "source_name": "conformance-fixtures",
    "source_kind": "synthetic",
    "licence": "CC0-1.0",
    "locale": "und",
    "split_rule": "source",
}
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
        if stray := sorted(set(getattr(entry.cls, "optional_inputs", ())) - set(entry.cls.consumes)):
            problems.append(f"step kind {name}: optional inputs {stray} are not inputs it consumes")
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
        if fam.fixtures is None or not (fam.fixtures / "metadata.csv").is_file():
            problems.append(f"family {fname}: no conformance fixtures (a folder-csv import folder with metadata.csv)")
    if families and IMPORT_KIND not in kinds:
        problems.append(f"runtime {runtime!r} does not publish the neutral {IMPORT_KIND} kind the flow starts from")
    if families and SCORE_KIND not in kinds:
        problems.append(f"runtime {runtime!r} does not publish the neutral {SCORE_KIND} kind evaluations score with")
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
    def __init__(
        self,
        runtime: str,
        kinds: Mapping[str, KindEntry],
        work: Path,
        stop_grace: float = 60.0,
        memory_cap_mb: int = 0,
    ) -> None:
        self.runtime = runtime
        self.memory_cap_mb = memory_cap_mb
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
            "card": {"index": 0, "memoryCapMb": self.memory_cap_mb},
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

    def ingest(self, fixtures: Path, params: Mapping[str, Any]) -> tuple[ArtifactRef, dict[str, str]]:
        """Import the fixtures (a ``folder-csv`` folder) with ``dataset_import`` through the harness, as a pipeline's
        import step would: the ``dataset`` directory artifact every training and transcribe step reads, and the
        reference text of every utterance by the BLAKE3 hash of its audio."""
        p = {**IMPORT_PARAMS, **params, "path": str(fixtures)}
        out, _ = self.run(IMPORT_KIND, p, {})
        data = _by_type(_expect_done(out, "import"), "dataset", "import")
        files = {f.path: f.hash for f in self.store.read_manifest(data["hash"])}
        if "dataset.json" not in files or "manifest.jsonl" not in files:
            raise ConformanceError(f"the imported dataset lacks dataset.json or manifest.jsonl: {sorted(files)}")
        refs: dict[str, str] = {}
        for line in self.store.path(files["manifest.jsonl"]).read_text(encoding="utf-8").splitlines():
            if line:
                row = json.loads(line)
                refs[files[row["audio"]]] = row["text"]
        return data, refs

    def put_json(self, doc: Mapping[str, Any], typ: str, meta: Mapping[str, Any] | None = None) -> ArtifactRef:
        b = json.dumps(doc, sort_keys=True, separators=(",", ":")).encode()
        return {
            "hash": self.store.put_bytes(b),
            "type": typ,
            "size": len(b),
            "meta": {"layout": "file", **(meta or {})},
        }

    def render_mix(self, data: ArtifactRef) -> ArtifactRef:
        """A one-group mix over the imported dataset, in the control plane's format (runs.RenderMix)."""
        files = {f.path: f.hash for f in self.store.read_manifest(data["hash"])}
        header = json.loads(self.store.path(files["dataset.json"]).read_bytes())
        hours = float(header.get("hours") or 0.0)
        doc = {
            "format": "cadence.mix/1",
            "mix": {"id": "mix_conformance", "name": "conformance", "revision": 1},
            "temperature": 1.0,
            "replayShare": 0.0,
            "input_cfg": [
                {
                    "type": "group",
                    "name": "fixtures",
                    "replay": False,
                    "weight": 1.0,
                    "probability": 1.0,
                    "input_cfg": [
                        {
                            "type": "dataset",
                            "dataset": "dsv_conformance",
                            "name": "conformance-fixtures",
                            "version": "fixtures",
                            "artifact": data["hash"],
                            "hours": hours,
                        }
                    ],
                }
            ],
        }
        return self.put_json(doc, "mix", {"format": "cadence.mix/1", "datasets": ["dsv_conformance"]})

    def render_base_model(self, family: str, model: Mapping[str, Any]) -> ArtifactRef:
        doc = {
            "format": "cadence.base_model/1",
            "versionId": "bmv_conformance",
            "collection": "base-model/conformance",
            "version": "fixtures",
            "family": {"name": family, "versionId": "mfv_conformance"},
            "model": dict(model),
        }
        return self.put_json(doc, "base_model", {"format": "cadence.base_model/1", "family": family})

    def inputs_for(self, kind: str, available: Mapping[str, ArtifactRef]) -> dict[str, ArtifactRef]:
        """Every input the kind consumes, filled from ``available`` by artifact type; an optional input of a type the
        flow does not have stays unwired, as a pipeline may leave it."""
        cls = self.kinds[kind].cls
        optional = set(getattr(cls, "optional_inputs", ()))
        out: dict[str, ArtifactRef] = {}
        for name, typ in sorted(cls.consumes.items()):
            if typ not in available:
                if name in optional:
                    continue
                raise ConformanceError(f"{kind} consumes {name} ({typ}), which the conformance flow cannot provide")
            out[name] = available[typ]
        return out

    def read_json(self, ref: ArtifactRef) -> Any:
        return json.loads(self.store.path(ref["hash"]).read_bytes())

    def dir_files(self, ref: ArtifactRef) -> dict[str, Path]:
        """The files of a directory artifact by relative path."""
        return {f.path: self.store.path(f.hash) for f in self.store.read_manifest(ref["hash"])}

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
    fixtures = fam.fixtures
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

    state: dict[str, Any] = {}

    def ingest() -> dict[str, Any]:
        state["data"], state["refs"] = flow.ingest(fixtures, conf.get("import", {}))
        return {"dataset": state["data"]["hash"], "utterances": len(state["refs"])}

    if not stage("import", ingest):
        return
    data: ArtifactRef = state["data"]
    refs: dict[str, str] = state["refs"]
    available: dict[str, ArtifactRef] = {"dataset": data}
    consumed = {t for k in roles.values() if k in flow.kinds for t in flow.kinds[k].cls.consumes.values()}
    if "mix" in consumed:
        available["mix"] = flow.render_mix(data)
    if "base_model" in consumed:
        if "base_model" not in conf:
            report.stages.append(Stage(f"{prefix}/inputs", False, 0.0, {"error": "no conformance base_model"}))
            return
        available["base_model"] = flow.render_base_model(d["name"], conf["base_model"])
    available["normalizer"] = flow.put_json(NORMALIZER, "normalizer", {"versionId": NORMALIZER["versionId"]})

    def calibrate() -> dict[str, Any]:
        out, _ = flow.run(roles["calibrate"], conf.get("calibrate", {}), flow.inputs_for(roles["calibrate"], available))
        cal = _by_type(_expect_done(out, "calibrate"), "calibration", "calibrate")
        available["calibration"] = cal
        doc = flow.read_json(cal)
        if not (isinstance(doc, dict) and float(doc.get("secondsPerStep", 0)) > 0 and int(doc.get("batchSize", 0)) > 0):
            raise ConformanceError(f"calibration lacks secondsPerStep/batchSize: {doc}")
        return {"secondsPerStep": doc["secondsPerStep"], "batchSize": doc["batchSize"]}

    def check_checkpoint(ref: ArtifactRef, what: str, untrained: bool = False) -> dict[str, Any]:
        meta = ref.get("meta") or {}
        missing = [k for k in CHECKPOINT_META if meta.get(k) is None and not (untrained and k == "valWer")]
        if missing or meta.get("family") != d["name"]:
            raise ConformanceError(f"{what}: checkpoint meta {meta} lacks {missing} or names another family")
        return meta

    def train() -> dict[str, Any]:
        out, sink = flow.run(roles["train"], conf.get("train", {}), flow.inputs_for(roles["train"], available))
        outputs = _expect_done(out, "train")
        ck = _by_type(outputs, "checkpoint", "train")
        state["ck1"] = ck
        # A finished train step may skip its final training state when its kind declares it optional (nothing
        # resumes a finished run); resume then continues from the stopped run's state.
        optional = set(getattr(flow.kinds[roles["train"]].cls, "optional_outputs", ()))
        produced = {n: t for n, t in flow.kinds[roles["train"]].cls.produces.items() if t == "training-state"}
        if any(r["type"] == "training-state" for r in outputs.values()):
            state["ts1"] = _by_type(outputs, "training-state", "train")
        elif not produced or not set(produced) <= optional:
            raise ConformanceError("train: no training-state output, and the kind does not declare it optional")
        names = {p["name"] for p in sink.metrics}
        if not {"loss", "val_wer"} <= names:
            raise ConformanceError(f"train posted metrics {sorted(names)}, expected loss and val_wer")
        meta = check_checkpoint(ck, "train")
        # Every validation's checkpoint is registered, not only the last: the ones before it are published during
        # the lease (ctx.publish → workerOutputs.new), each with the neutral checkpoint meta.
        registered = {ck["hash"]}
        for pub in sink.published:
            if pub["artifact"]["type"] == "checkpoint":
                check_checkpoint(pub["artifact"], f"published checkpoint {pub['name']}")
                registered.add(pub["artifact"]["hash"])
        validations = sum(1 for p in sink.metrics if p["name"] == "val_wer")
        if validations >= 2 and len(registered) < 2:
            raise ConformanceError(
                f"{validations} validations registered {len(registered)} checkpoint(s); publish each validation's "
                "checkpoint with ctx.publish"
            )
        losses = [p["value"] for p in sink.metrics if p["name"] == "loss"]
        return {
            "step": meta["step"],
            "valWer": meta["valWer"],
            "firstLoss": losses[0],
            "lastLoss": losses[-1],
            "validations": validations,
            "checkpoints": len(registered),
        }

    def stop() -> dict[str, Any]:
        out, _ = flow.run(
            roles["train"],
            conf.get("stop", {}),
            flow.inputs_for(roles["train"], available),
            sink=StopAfterFirstMetric(),
        )
        if out["state"] != "cancelled":
            raise ConformanceError(f"a stopped train step ended {out['state']}, expected cancelled")
        outputs = out.get("outputs") or {}
        ts = _by_type(outputs, "training-state", "stop")
        if any(r["type"] != "training-state" for r in outputs.values()):
            raise ConformanceError("a cancelled step released outputs other than its training-state")
        state["tsStop"] = ts
        return {"trainingState": ts["hash"], "meta": ts.get("meta")}

    def resume() -> dict[str, Any]:
        out, _ = flow.run(
            roles["train"],
            conf.get("resume", {}),
            flow.inputs_for(roles["train"], available),
            {"resumeFrom": (state.get("ts1") or state["tsStop"])["hash"]},
        )
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

    def transcribe(profile: Mapping[str, Any], model: str = "avg", key: str = "wer") -> Callable[[], dict[str, Any]]:
        def run() -> dict[str, Any]:
            params = {**conf.get("transcribe", {}), "profile": profile["name"]}
            out, _ = flow.run(
                roles["transcribe"],
                params,
                flow.inputs_for(roles["transcribe"], {**available, "checkpoint": state[model]}),
            )
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
            summary = score_hypotheses(hyp, len(refs), bool(profile.get("chunkMs")))
            state.setdefault(key, {})[profile["name"]] = float(summary["wer"])
            detail = {k: round(float(summary[k]), 4) for k in ("wer", "cer", "werNoPunct")}
            if "stability" in summary:
                detail["unstablePartialWordRatio"] = round(float(summary["stability"]["ratio"]), 4)
            return {**detail, "utterances": len(lines)}

        return run

    def score_hypotheses(hyp: ArtifactRef, utterances: int, streaming: bool) -> dict[str, Any]:
        """Score through the neutral scorer, as an eval pipeline does, and check the scores artifact's shape."""
        out, _ = flow.run(SCORE_KIND, {}, flow.inputs_for(SCORE_KIND, {**available, "hypotheses": hyp}))
        scores = _by_type(_expect_done(out, SCORE_KIND), "scores", SCORE_KIND)
        files = flow.dir_files(scores)
        if "summary.json" not in files or "utterances.jsonl" not in files:
            raise ConformanceError(f"the scores artifact lacks summary.json or utterances.jsonl: {sorted(files)}")
        summary = json.loads(files["summary.json"].read_bytes())
        rows = [json.loads(x) for x in files["utterances.jsonl"].read_text(encoding="utf-8").splitlines() if x]
        missing = [k for k in SCORES_SUMMARY if k not in summary]
        if missing or summary["schema"] != "cadence.scores/1":
            raise ConformanceError(f"summary.json is not cadence.scores/1 (lacks {missing})")
        if len(rows) != utterances or any(k not in r for r in rows for k in SCORES_ROW):
            raise ConformanceError(
                f"utterances.jsonl has {len(rows)} rows for {utterances} utterances, or rows lack keys"
            )
        if streaming and "stability" not in summary:
            raise ConformanceError("streaming hypotheses scored without partial stability")
        return dict(summary)

    def baseline() -> dict[str, Any]:
        out, _ = flow.run(roles["train"], conf["baseline"], flow.inputs_for(roles["train"], available))
        ck = _by_type(_expect_done(out, "baseline"), "checkpoint", "baseline")
        state["base"] = ck
        return {"step": check_checkpoint(ck, "baseline")["step"]}

    def materialize() -> dict[str, Any]:
        """The base model as a checkpoint (role materialize), what an eval transcribes for the baseline cells."""
        kind = roles["materialize"]
        out, _ = flow.run(kind, {}, flow.inputs_for(kind, available))
        ck = _by_type(_expect_done(out, "materialize"), "checkpoint", "materialize")
        meta = check_checkpoint(ck, "materialize", untrained=True)
        state["material"] = ck
        return {"weightsHash": meta["weightsHash"], "step": meta["step"]}

    def default_profile() -> Mapping[str, Any]:
        """The profile the transcribe kind decodes at by default (the primary cell), else the family's first."""
        field = flow.kinds[roles["transcribe"]].cls.Params.model_fields.get("profile")
        name = field.default if field is not None else None
        return next((p for p in d["latencyProfiles"] if p["name"] == name), d["latencyProfiles"][0])

    def score() -> dict[str, Any]:
        """The trained model must beat the untrained one (baseline, first profile) and meet the family's maxWer."""
        got: dict[str, float] = state.get("wer", {})
        base: dict[str, float] = state.get("baseWer", {})
        max_wer = conf.get("score", {}).get("maxWer")
        detail: dict[str, Any] = {"wer": {k: round(v, 4) for k, v in got.items()}}
        if material := state.get("baseModelWer"):
            detail["baseModelWer"] = {k: round(v, 4) for k, v in material.items()}
        if "baseline" not in conf:
            detail["baseline"] = "skipped: the family declares no conformance baseline"
        elif not base:
            raise ConformanceError("the baseline transcription did not run")
        else:
            floor = next(iter(base.values()))
            detail["baselineWer"] = round(floor, 4)
            if worse := {k: v for k, v in got.items() if v >= floor}:
                raise ConformanceError(f"the trained model does not beat the untrained one ({floor:.4f}): {worse}")
        if max_wer is not None:
            detail["maxWer"] = max_wer
            if over := {k: v for k, v in got.items() if v > float(max_wer)}:
                raise ConformanceError(f"WER above the family's conformance bound {max_wer}: {over}")
        return detail

    ok = stage("calibrate", calibrate)
    ok = stage("train", train) and ok
    if ok:
        stage("stop", stop)
        if stage("resume", resume) and stage("average", average):
            for p in d["latencyProfiles"]:
                stage(f"transcribe:{p['name']}", transcribe(p))
            if "baseline" in conf and stage("baseline", baseline):
                stage("transcribe:baseline", transcribe(d["latencyProfiles"][0], "base", "baseWer"))
            if stage("materialize", materialize):
                stage("transcribe:base-model", transcribe(default_profile(), "material", "baseModelWer"))
            stage("score", score)
    for role, why in LATER_ROLES.items():
        report.stages.append(Stage(f"{prefix}/{role}", True, 0.0, {"skipped": why}))


def run(
    runtime: str,
    *,
    help_dir: Path | None = None,
    work: Path | None = None,
    stop_grace: float = 60.0,
    memory_cap_mb: int = 0,
) -> Report:
    """The suite for one runtime. ``memory_cap_mb`` is the card cap GPU steps run under, as a lease's would be (0: the
    whole card)."""
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
        flow = Flow(runtime, kinds, Path(tmp), stop_grace=stop_grace, memory_cap_mb=memory_cap_mb)
        for fam in families:
            run_family(flow, fam, report)
    return report
