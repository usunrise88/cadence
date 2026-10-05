"""The Nemotron family on a staging server (docs/spec/06-platform.md "Staging serving", R30, R46; spike E1).

The control plane never speaks a server's stream protocol: a ``serve`` lease carries the staging target's endpoint,
its server kind and the versioned model name (``CADENCE_SERVING_*``, internal/serving), and this module does the rest
for the family's deployable (``cadence.deployable/1``, written by the export role):

- **the model**: copy the deployable's model directory into the ``serving`` volume under the model name (the server
  sees the same volume read-only at ``/models``), load it through the model-control API (Triton's explicit mode), and
  check that it took no more card memory than its reservation (``serving-over-cap``);
- **the stream protocol**: E1's step graph — one request per chunk of one stream, ``audio_signal`` [n_mels, T] (the
  pre-encode cache and the chunk, right-padded), ``length`` and ``prompt``; the sequence batcher keeps the stream's
  state between requests (implicit state), ``sequence_start`` resets it, and the answer is the chunk's token ids
  (-1 = none). Requests go over HTTP with the binary tensor extension (no client library);
- **the front end**: the client computes the features the pipeline decoder would (``pipeline.Features``, built from
  the preprocessor config the export recorded), and cuts them into the buffers ``nemotron_transcribe`` sends;
- **the text**: token ids → pieces → text, the locale tag stripped; endpointing (a final after
  ``stop_history_eou_ms`` of audio without a token) happens here, as Эра's client must do it.

``streaming_cfg.json`` beside the model (E1 ``export_step.py``) gives the geometry: ``chunk_size`` and
``pre_encode_cache_size`` ([first, later] frames), ``buffer_frames``, ``n_mels``, ``window_stride_s``, ``sample_rate``,
``chunk_ms``, ``prompt_dictionary``, ``blank_id``, ``valid_out_len`` x ``max_symbols`` token slots; and, for the client,
``vocabulary`` (the tokenizer's pieces by id) and ``frontend`` (``{"kind": "nemo", "preprocessor": {…}}``, or
``{"kind": "fixture"}``: a log-energy front end only the serving fixture uses).
"""

from __future__ import annotations

import contextlib
import http.client
import itertools
import json
import math
import os
import re
import secrets
import shutil
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol

import numpy as np

from cadence_nemo import lang
from cadence_worker.steps.base import ServingOverCap, ServingUnavailable, StepInputError, TargetDoesNotServe
from cadence_worker.telemetry import Telemetry

# The lease environment internal/serving sets (Go names them; the values travel with the lease only).
ENV_TARGET = "CADENCE_SERVING_TARGET"
ENV_ENDPOINT = "CADENCE_SERVING_ENDPOINT"
ENV_SERVER = "CADENCE_SERVING_SERVER"
ENV_SERVER_VERSION = "CADENCE_SERVING_SERVER_VERSION"
ENV_MODEL = "CADENCE_SERVING_MODEL"
ENV_MODELS = "CADENCE_SERVING_MODELS"
ENV_REFUSED = "CADENCE_SERVING_REFUSED"
# The worker's side of the serving volume (docker-compose.yml: the GPU worker mounts it read-write here).
ENV_DIR = "CADENCE_SERVING_DIR"
DEFAULT_DIR = "/var/lib/cadence/serving"

SERVER_KIND = "triton"
DECODER = "served-step-graph"
DEPLOYABLE_SCHEMA = "cadence.deployable/1"
INSTALLED = ".cadence-installed"
SR = 16000

Audio = np.ndarray[Any, np.dtype[np.float32]]
Event = dict[str, Any]


# ---------------------------------------------------------------- the lease and the deployable


@dataclass(frozen=True)
class ServingLease:
    """What the control plane put in the lease for a served step."""

    target: str
    endpoint: str
    server: str
    server_version: str
    model: str
    models: Mapping[str, str]
    refused: str

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> ServingLease:
        e = os.environ if env is None else env
        try:
            models = json.loads(e.get(ENV_MODELS) or "{}")
        except json.JSONDecodeError:
            models = {}
        return cls(
            target=e.get(ENV_TARGET, ""),
            endpoint=e.get(ENV_ENDPOINT, "").rstrip("/"),
            server=e.get(ENV_SERVER, ""),
            server_version=e.get(ENV_SERVER_VERSION, ""),
            model=e.get(ENV_MODEL, ""),
            models={str(k): str(v) for k, v in models.items()} if isinstance(models, dict) else {},
            refused=e.get(ENV_REFUSED, ""),
        )

    def check(self) -> None:
        """Refuse a lease the step cannot serve through: refused by the control plane, no endpoint, another server."""
        if self.refused:
            raise TargetDoesNotServe(self.refused.removeprefix("target-does-not-serve: "))
        if not self.endpoint:
            raise ServingUnavailable(
                "the lease names no staging endpoint: the control plane grants one to a step that consumes a deployable"
            )
        if self.server != SERVER_KIND:
            raise TargetDoesNotServe(
                f"the target's server kind is {self.server!r}; the family's step graph is served by {SERVER_KIND}"
            )

    def model_for(self, input_name: str) -> str:
        if m := self.models.get(input_name):
            return m
        if self.model:
            return self.model
        raise ServingUnavailable(f"the lease names no served model for input {input_name!r}")


@dataclass(frozen=True)
class Geometry:
    """The served step graph's stream geometry (streaming_cfg.json)."""

    chunk_frames: tuple[int, int]
    pre_encode: tuple[int, int]
    buffer_frames: int
    n_mels: int
    hop: int
    half: int
    chunk_ms: int
    prompts: Mapping[str, int]
    blank_id: int
    vocabulary: Sequence[str]
    frontend: Mapping[str, Any]
    token_slots: int

    @classmethod
    def from_cfg(cls, d: Mapping[str, Any]) -> Geometry:
        def two(v: Any) -> tuple[int, int]:
            if isinstance(v, list | tuple):
                return int(v[0]), int(v[-1])
            return int(v), int(v)

        try:
            sr = int(d.get("sample_rate", SR))
            stride = float(d.get("window_stride_s", 0.01))
            fe = dict(d.get("frontend") or {})
            pre = fe.get("preprocessor") or {}
            n_fft = int(pre.get("n_fft") or 512)
            vout = d.get("valid_out_len", 1)
            slots = int(vout if isinstance(vout, int) else two(vout)[1]) * int(d.get("max_symbols", 10))
            return cls(
                chunk_frames=two(d["chunk_size"]),
                pre_encode=two(d["pre_encode_cache_size"]),
                buffer_frames=int(d["buffer_frames"]),
                n_mels=int(d["n_mels"]),
                hop=round(stride * sr),
                half=n_fft // 2,
                chunk_ms=int(d["chunk_ms"]),
                prompts={str(k): int(v) for k, v in dict(d["prompt_dictionary"]).items()},
                blank_id=int(d.get("blank_id", -1)),
                vocabulary=[str(p) for p in d.get("vocabulary") or []],
                frontend=fe,
                token_slots=slots,
            )
        except (KeyError, TypeError, ValueError) as e:
            raise StepInputError(f"streaming_cfg.json lacks or mistypes {e}: re-export the model") from e

    def prompt(self, language: str) -> tuple[str, int]:
        key = lang.resolve_prompt_key(language, self.prompts)
        return key, self.prompts[key]


@dataclass(frozen=True)
class Deployable:
    """A deployable artifact directory (cadence.deployable/1)."""

    root: Path
    format: str
    family: str
    profile: str
    weights_hash: str
    model_dir: Path
    memory_mb: int | None
    geometry: Geometry


def read_deployable(path: Path) -> Deployable:
    """The deployable at ``path``: deployable.json, the model directory it names and its streaming_cfg.json."""
    doc_path = path / "deployable.json"
    try:
        doc = json.loads(doc_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        raise StepInputError(f"the deployable has no readable deployable.json: {e}") from e
    if doc.get("schema", DEPLOYABLE_SCHEMA) != DEPLOYABLE_SCHEMA:
        raise StepInputError(f"deployable.json is {doc.get('schema')!r}, not {DEPLOYABLE_SCHEMA}")
    serving = dict(doc.get("serving") or {})
    server = dict(serving.get("server") or {})
    if server.get("kind", SERVER_KIND) != SERVER_KIND:
        raise TargetDoesNotServe(f"the deployable is for server kind {server.get('kind')!r}, not {SERVER_KIND}")
    rel = str(serving.get("modelDir") or "")
    model_dir = (path / rel).resolve() if rel else path
    if not str(model_dir).startswith(str(path.resolve())) or not (model_dir / "config.pbtxt").is_file():
        raise StepInputError(f"the deployable's serving.modelDir {rel!r} is not a model directory with config.pbtxt")
    cfg_path = model_dir / "streaming_cfg.json"
    if not cfg_path.is_file():
        raise StepInputError("the deployable's model directory has no streaming_cfg.json")
    geo = Geometry.from_cfg(json.loads(cfg_path.read_text(encoding="utf-8")))
    mem = serving.get("memoryMb")
    return Deployable(
        root=path,
        format=str(doc.get("format", "")),
        family=str(doc.get("family", "")),
        profile=str(doc.get("profile", "")),
        weights_hash=str(doc.get("weightsHash", "")),
        model_dir=model_dir,
        memory_mb=int(mem) if mem else None,
        geometry=geo,
    )


# ---------------------------------------------------------------- the model on the server

NAME_LINE = re.compile(r'^name:\s*"[^"]*"\s*$', re.MULTILINE)


def install(dep: Deployable, root: Path, name: str) -> Path:
    """Copy the deployable's model directory into the serving volume as ``root/name`` (the server's repository),
    with its config naming ``name``. An installed copy (same name, so the same deployable) is reused."""
    dest = root / name
    if (dest / INSTALLED).is_file():
        return dest
    root.mkdir(parents=True, exist_ok=True)
    tmp = root / f".{name}.{os.getpid()}.{secrets.token_hex(4)}"
    # The store's blobs are read-only to the worker's user; the server reads the copy as another user.
    shutil.copytree(dep.model_dir, tmp, copy_function=shutil.copyfile)
    for d, _, files in os.walk(tmp):
        os.chmod(d, 0o755)
        for f in files:
            os.chmod(os.path.join(d, f), 0o644)
    cfg = tmp / "config.pbtxt"
    text = cfg.read_text(encoding="utf-8")
    line = f'name: "{name}"'
    text = NAME_LINE.sub(line, text) if NAME_LINE.search(text) else f"{line}\n{text}"
    cfg.write_text(text, encoding="utf-8")
    (tmp / INSTALLED).write_text(json.dumps({"deployable": str(dep.root), "weightsHash": dep.weights_hash}) + "\n")
    try:
        tmp.rename(dest)
    except OSError:
        shutil.rmtree(tmp, ignore_errors=True)  # another lease installed it first
        if not (dest / INSTALLED).is_file():
            raise
    return dest


class Control:
    """The server's health and model-control API (KServe v2 with Triton's repository extension)."""

    def __init__(self, endpoint: str, timeout: float = 10.0) -> None:
        self.endpoint = endpoint.rstrip("/")
        self.timeout = timeout

    def _call(self, method: str, path: str, body: bytes | None = None) -> tuple[int, bytes]:
        req = urllib.request.Request(self.endpoint + path, data=body, method=method)
        if body is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                return int(r.status), r.read()
        except urllib.error.HTTPError as e:
            return int(e.code), e.read()
        except (urllib.error.URLError, OSError) as e:
            raise ServingUnavailable(f"{self.endpoint} does not answer: {e}") from e

    def ready(self) -> bool:
        code, _ = self._call("GET", "/v2/health/ready")
        return code == 200

    def model_ready(self, name: str) -> bool:
        code, _ = self._call("GET", f"/v2/models/{urllib.parse.quote(name)}/ready")
        return code == 200

    def load(self, name: str, timeout_s: float, clock: Callable[[], float] = time.monotonic) -> None:
        """Load ``name`` from the repository and wait until it is ready (serving-unavailable after ``timeout_s``)."""
        deadline = clock() + timeout_s
        code, body = self._call("POST", f"/v2/repository/models/{urllib.parse.quote(name)}/load", b"{}")
        if code != 200:
            raise ServingUnavailable(f"the server refused to load {name}: {code} {body[:300].decode(errors='replace')}")
        while not self.model_ready(name):
            if clock() > deadline:
                raise ServingUnavailable(f"the model {name} did not load within {timeout_s:g} s")
            time.sleep(0.5)

    def unload(self, name: str) -> None:
        with contextlib.suppress(ServingUnavailable):
            self._call("POST", f"/v2/repository/models/{urllib.parse.quote(name)}/unload", b"{}")

    def stats(self, name: str) -> dict[str, Any]:
        """The model's inference statistics (counts and cumulative durations), or {} when the server has none."""
        code, body = self._call("GET", f"/v2/models/{urllib.parse.quote(name)}/stats")
        if code != 200:
            return {}
        with contextlib.suppress(json.JSONDecodeError, KeyError, IndexError, TypeError):
            return dict(json.loads(body)["model_stats"][0])
        return {}


def physical_card(env: Mapping[str, str] | None = None) -> int | None:
    """The lease's card as the host numbers it (the harness names it in CUDA_VISIBLE_DEVICES), for its telemetry."""
    v = (os.environ if env is None else env).get("CUDA_VISIBLE_DEVICES", "").split(",")[0].strip()
    return int(v) if v.isdigit() else None


def card_used_mb(index: int | None, telemetry: Telemetry | None = None) -> int | None:
    """The card's used memory by its telemetry, or None when unknown."""
    if index is None or index < 0:
        return None
    for c in (telemetry or Telemetry()).cards():
        if c.get("index") == index and "memoryUsedMb" in c:
            return int(c["memoryUsedMb"])
    return None


def ensure_loaded(
    control: Control,
    name: str,
    *,
    reservation_mb: int | None,
    slack_mb: int,
    timeout_s: float,
    used_mb: Callable[[], int | None],
) -> dict[str, Any]:
    """Load ``name`` unless the server has it ready; a load that took more card memory than the reservation plus the
    slack is undone and fails serving-over-cap (06 "Staging serving", Memory)."""
    if not control.ready():
        raise ServingUnavailable(f"the server at {control.endpoint} is not ready")
    if control.model_ready(name):
        return {"model": name, "loaded": False}
    before = used_mb()
    t0 = time.monotonic()
    control.load(name, timeout_s)
    after = used_mb()
    out: dict[str, Any] = {"model": name, "loaded": True, "loadS": round(time.monotonic() - t0, 2)}
    if before is not None and after is not None:
        took = after - before
        out["tookMb"] = took
        if reservation_mb is not None and took > reservation_mb + slack_mb:
            control.unload(name)
            raise ServingOverCap(
                f"the model {name} took {took} MB on the card, its reservation is {reservation_mb} MB "
                f"(+{slack_mb} MB slack); it was unloaded"
            )
    return out


# ---------------------------------------------------------------- the stream protocol


class StepClient:
    """One HTTP connection to the step graph: one request per chunk of one sequence (Triton's binary tensors)."""

    def __init__(self, endpoint: str, model: str, timeout: float = 30.0) -> None:
        u = urllib.parse.urlsplit(endpoint)
        if u.scheme not in ("http", "https") or not u.hostname:
            raise ServingUnavailable(f"{endpoint!r} is not an http(s) endpoint")
        self.host, self.port, self.https = (
            u.hostname,
            u.port or (443 if u.scheme == "https" else 80),
            u.scheme == "https",
        )
        self.base = u.path.rstrip("/")
        self.model = model
        self.timeout = timeout
        self._conn: http.client.HTTPConnection | None = None

    def _connection(self) -> http.client.HTTPConnection:
        if self._conn is None:
            cls = http.client.HTTPSConnection if self.https else http.client.HTTPConnection
            self._conn = cls(self.host, self.port, timeout=self.timeout)
        return self._conn

    def close(self) -> None:
        if self._conn is not None:
            self._conn.close()
            self._conn = None

    def infer(
        self, sequence: int, start: bool, end: bool, features: np.ndarray[Any, Any], length: int, prompt: int
    ) -> np.ndarray[Any, np.dtype[np.int32]]:
        """The token ids of one chunk (-1 slots dropped)."""
        feats = np.ascontiguousarray(features, dtype=np.float32)[None]
        blobs = [feats.tobytes(), np.array([[length]], np.int64).tobytes(), np.array([[prompt]], np.int64).tobytes()]
        header = {
            "parameters": {"sequence_id": sequence, "sequence_start": start, "sequence_end": end},
            "inputs": [
                {
                    "name": "audio_signal",
                    "shape": list(feats.shape),
                    "datatype": "FP32",
                    "parameters": {"binary_data_size": len(blobs[0])},
                },
                {"name": "length", "shape": [1, 1], "datatype": "INT64", "parameters": {"binary_data_size": 8}},
                {"name": "prompt", "shape": [1, 1], "datatype": "INT64", "parameters": {"binary_data_size": 8}},
            ],
            "outputs": [{"name": "tokens", "parameters": {"binary_data": True}}],
        }
        head = json.dumps(header, separators=(",", ":")).encode()
        body = head + b"".join(blobs)
        hdrs = {"Content-Type": "application/octet-stream", "Inference-Header-Content-Length": str(len(head))}
        path = f"{self.base}/v2/models/{urllib.parse.quote(self.model)}/infer"
        for attempt in (1, 2):
            try:
                conn = self._connection()
                conn.request("POST", path, body=body, headers=hdrs)
                resp = conn.getresponse()
                data = resp.read()
                status, hlen = resp.status, resp.getheader("Inference-Header-Content-Length")
                break
            except (OSError, http.client.HTTPException) as e:
                self.close()
                if attempt == 2:
                    raise ServingUnavailable(f"the server at {self.host}:{self.port} does not answer: {e}") from e
        if status != 200:
            raise ServingUnavailable(f"{self.model} answered {status}: {data[:300].decode(errors='replace')}")
        return parse_tokens(data, int(hlen) if hlen else len(data))


def parse_tokens(data: bytes, header_len: int) -> np.ndarray[Any, np.dtype[np.int32]]:
    """The ``tokens`` output of an inference answer (binary or JSON), without the -1 slots."""
    doc = json.loads(data[:header_len])
    off = header_len
    for out in doc.get("outputs") or []:
        size = int((out.get("parameters") or {}).get("binary_data_size") or 0)
        if out.get("name") == "tokens":
            if size:
                arr = np.frombuffer(data[off : off + size], dtype=np.int32)
            else:
                arr = np.asarray(out.get("data") or [], dtype=np.int32)
            return arr[arr >= 0]
        off += size
    raise ServingUnavailable("the answer has no tokens output")


_sequences = itertools.count(1)
_sequence_base = secrets.randbits(40) << 20


def next_sequence() -> int:
    """A sequence id no other client of the model is likely to use (the server keys stream state by it)."""
    return _sequence_base + next(_sequences)


# ---------------------------------------------------------------- the front end


class Featurizer(Protocol):
    hop: int

    @property
    def n(self) -> int:
        """Frames computed so far."""
        ...

    @property
    def total(self) -> int:
        """Stream samples received."""
        ...

    def push(self, x: Audio) -> None: ...

    def finish(self) -> None: ...

    def frames(self, start: int, end: int) -> np.ndarray[Any, Any]: ...

    def trim(self, keep_from: int) -> None: ...


class NemoFeaturizer:
    """pipeline.Features (the features of the whole stream, computed as audio arrives) over the export's
    preprocessor."""

    def __init__(self, preprocessor: Any, hop: int, half: int) -> None:
        from cadence_nemo import pipeline

        self.f = pipeline.Features(preprocessor=preprocessor, hop=hop, half=half)
        self.hop = hop

    @property
    def n(self) -> int:
        return self.f.n

    @property
    def total(self) -> int:
        return self.f.total

    def push(self, x: Audio) -> None:
        self.f.push(x)

    def finish(self) -> None:
        self.f.finish()

    def frames(self, start: int, end: int) -> np.ndarray[Any, Any]:
        return np.asarray(self.f.frames(start, end).detach().float().cpu().numpy(), dtype=np.float32)

    def trim(self, keep_from: int) -> None:
        self.f.trim(keep_from)


class FixtureFeaturizer:
    """The serving fixture's front end: per 10 ms frame the log energy of its window, on every mel row. Not a model
    front end; it lets the client's chunking and the server's sequence protocol be tested without NeMo."""

    def __init__(self, n_mels: int, hop: int, half: int) -> None:
        self.n_mels, self.hop, self.half = n_mels, hop, half
        self.audio: Audio = np.zeros(0, np.float32)
        self.total = 0
        self.n = 0
        self.final = False

    def push(self, x: Audio) -> None:
        self.audio = np.concatenate([self.audio, np.asarray(x, np.float32)])
        self.total = int(self.audio.size)
        self.n = max(0, (self.total - self.half) // self.hop + 1) if self.total >= self.half else 0

    def finish(self) -> None:
        self.final = True
        self.n = self.total // self.hop + 1 if self.total else 0

    def frames(self, start: int, end: int) -> np.ndarray[Any, Any]:
        out = np.zeros((self.n_mels, max(0, end - start)), np.float32)
        for j in range(start, end):
            c = j * self.hop
            seg = self.audio[max(0, c - self.half) : c + self.half]
            out[:, j - start] = math.log(float(np.mean(seg * seg)) + 1e-6) if seg.size else math.log(1e-6)
        return out

    def trim(self, keep_from: int) -> None:
        return None


def nemo_preprocessor(cfg: Mapping[str, Any]) -> Any:
    """NeMo's mel front end from the preprocessor config the export recorded (no dither: the decoder's eval mode)."""
    from nemo.collections.asr.modules import AudioToMelSpectrogramPreprocessor

    c = {k: v for k, v in cfg.items() if not str(k).startswith("_")}
    c["dither"] = 0.0
    c["pad_to"] = 0
    return AudioToMelSpectrogramPreprocessor(**c).eval()


def featurizer_factory(geo: Geometry) -> Callable[[], Featurizer]:
    """A new featurizer per stream, sharing the front end's model."""
    kind = str(geo.frontend.get("kind") or "")
    if kind == "fixture":
        return lambda: FixtureFeaturizer(geo.n_mels, geo.hop, geo.half)
    if kind == "nemo":
        pre = nemo_preprocessor(dict(geo.frontend.get("preprocessor") or {}))
        return lambda: NemoFeaturizer(pre, geo.hop, geo.half)
    raise StepInputError(f"streaming_cfg.json names front end {kind!r}; the client knows nemo (and the fixture's)")


@dataclass
class Chunk:
    """One request's buffer: the pre-encode cache and the chunk, right-padded to the graph's buffer."""

    features: np.ndarray[Any, Any]
    length: int
    first: bool
    last: bool
    real: int  # stream samples it accounts for


@dataclass
class Chunker:
    """The buffers ``nemotron_transcribe``'s pipeline decoder sends for one stream (pipeline.PipelineStream's
    chunking): a first chunk without cache, then later chunks with the pre-encode cache; the last one marked."""

    geo: Geometry
    feats: Featurizer
    idx: int = 0
    sent: int = 0
    first_pending: bool = True

    def _buffer(self, start: int, length: int, first: bool) -> tuple[np.ndarray[Any, Any], int]:
        g = self.geo
        _, p1 = g.pre_encode
        if first:
            cache = np.zeros((g.n_mels, g.pre_encode[0]), np.float32)
        else:
            lo = max(0, start - p1)
            cache = self.feats.frames(lo, start)
            if cache.shape[1] < p1:
                cache = np.concatenate([np.zeros((g.n_mels, p1 - cache.shape[1]), np.float32), cache], axis=1)
        body = self.feats.frames(start, start + length) if length > 0 else np.zeros((g.n_mels, 0), np.float32)
        buf = np.concatenate([cache, body], axis=1)[:, : g.buffer_frames]
        valid = int(buf.shape[1])
        if valid < g.buffer_frames:
            buf = np.concatenate([buf, np.zeros((g.n_mels, g.buffer_frames - valid), np.float32)], axis=1)
        return buf, valid

    def chunks(self, final: bool) -> list[Chunk]:
        c0, c1 = self.geo.chunk_frames
        _, p1 = self.geo.pre_encode
        plan: list[tuple[int, int, bool]] = []
        idx, first = self.idx, self.first_pending
        while True:
            cs = c0 if first else c1
            avail = self.feats.n - idx
            if (not final and avail < cs) or (final and avail <= 0):
                break
            plan.append((idx, min(cs, avail), first))
            idx += cs
            first = False
        if final and not plan:
            plan.append((self.feats.n, 0, self.first_pending))
        out: list[Chunk] = []
        for k, (start, length, first_chunk) in enumerate(plan):
            last = final and k == len(plan) - 1
            buf, valid = self._buffer(start, length, first_chunk)
            real = (self.feats.total - self.sent) if last else length * self.feats.hop
            real = max(0, min(real, self.feats.total - self.sent))
            self.sent += real
            out.append(Chunk(features=buf, length=valid, first=first_chunk, last=last, real=real))
        if plan:
            self.idx = plan[-1][0] + (c0 if plan[-1][2] else c1)
            self.first_pending = False
        self.feats.trim(max(0, self.idx - p1))
        return out


def utterance_chunks(geo: Geometry, make: Callable[[], Featurizer], audio: Audio) -> list[Chunk]:
    """Every buffer of one whole utterance (the features equal the incremental ones: pipeline.Features)."""
    f = make()
    f.push(audio)
    f.finish()
    return Chunker(geo, f).chunks(final=True)


# ---------------------------------------------------------------- text and endpointing


def detokenize(ids: Sequence[int], vocabulary: Sequence[str]) -> str:
    """SentencePiece pieces joined, ▁ as a space, the locale tag stripped."""
    pieces = [vocabulary[i] for i in ids if 0 <= i < len(vocabulary)]
    return lang.strip_tags("".join(pieces).replace("▁", " "))


def words_of(ids: Sequence[int], times: Sequence[tuple[float, float]], vocabulary: Sequence[str]) -> list[Event]:
    """Words with the audio time of the chunks that emitted their tokens (start of the first, end of the last)."""
    words: list[Event] = []
    for tok, (t0, t1) in zip(ids, times, strict=True):
        piece = vocabulary[tok] if 0 <= tok < len(vocabulary) else ""
        if not piece:
            continue
        text = piece.replace("▁", " ")
        if (piece.startswith("▁") or not words) and text.strip():
            words.append({"word": text.strip(), "start": round(t0, 3), "end": round(t1, 3)})
        elif words:
            words[-1]["word"] += text.strip()
            words[-1]["end"] = round(t1, 3)
    out = []
    for w in words:
        t = lang.strip_tags(str(w["word"]))
        if t:
            w["word"] = t
            out.append(w)
    return out


@dataclass
class ServedStream:
    """One target of a live session (cadence_worker.live.Decoder), or one utterance of a batch: audio in, the
    served model's partials and finals out. A segment ends after ``eou_ms`` of audio without a token (E1: endpointing
    stays outside the server); the server's stream state runs on across segments until finalize."""

    target: str
    client: StepClient
    geo: Geometry
    make: Callable[[], Featurizer]
    prompt: int
    profile: str
    language: str
    eou_ms: int = 800
    load_s: float = 0.0
    decoder: str = DECODER
    step_ms: list[float] = field(default_factory=list)
    boost: Mapping[str, Any] | None = None
    sequence: int = 0
    chunker: Chunker | None = None
    consumed: int = 0  # session samples accounted for by the chunks sent
    segment: int = 0
    seq: int = 0
    seg_ids: list[int] = field(default_factory=list)
    seg_times: list[tuple[float, float]] = field(default_factory=list)
    last_token_at: int = 0  # session sample of the end of the chunk that last emitted a token
    tokens: list[int] = field(default_factory=list)  # every token of the stream (the parity comparison)

    @property
    def chunk_ms(self) -> int:
        return self.geo.chunk_ms

    def _open(self) -> Chunker:
        if self.chunker is None:
            self.chunker = Chunker(self.geo, self.make())
            self.sequence = next_sequence()
        return self.chunker

    def send(self, ch: Chunk) -> tuple[list[Event], np.ndarray[Any, np.dtype[np.int32]]]:
        """One chunk to the server: its tokens and the events they make."""
        t0 = time.perf_counter()
        toks = self.client.infer(self.sequence, ch.first, ch.last, ch.features, ch.length, self.prompt)
        self.step_ms.append((time.perf_counter() - t0) * 1000)
        return self.consume(toks, ch.real), toks

    def consume(self, toks: np.ndarray[Any, Any], real: int) -> list[Event]:
        start = self.consumed
        self.consumed += real
        ev: list[Event] = []
        ids = [int(t) for t in toks if int(t) != self.geo.blank_id]
        if ids:
            span = (start / SR, self.consumed / SR)
            self.seg_ids += ids
            self.seg_times += [span] * len(ids)
            self.tokens += ids
            self.last_token_at = self.consumed
            self.seq += 1
            ev.append(
                {
                    "type": "partial",
                    "target": self.target,
                    "segment": self.segment,
                    "seq": self.seq,
                    "text": detokenize(self.seg_ids, self.geo.vocabulary),
                    "audioEnd": round(self.consumed / SR, 3),
                    "space": True,
                }
            )
        elif self.seg_ids and (self.consumed - self.last_token_at) * 1000 >= self.eou_ms * SR:
            ev.append(self.final("eou"))
        return ev

    def final(self, reason: str) -> Event:
        """Close the current segment with its final (endpoint ``reason``); the server sequence runs on."""
        self.seq += 1
        ev: Event = {
            "type": "final",
            "target": self.target,
            "segment": self.segment,
            "seq": self.seq,
            "text": detokenize(self.seg_ids, self.geo.vocabulary),
            "words": words_of(self.seg_ids, self.seg_times, self.geo.vocabulary),
            "endpoint": reason,
            "audioEnd": round(self.consumed / SR, 3),
            "space": True,
        }
        self.segment += 1
        self.seg_ids, self.seg_times = [], []
        return ev

    def push(self, x: Audio) -> list[Event]:
        x = np.asarray(x, np.float32)
        if x.size == 0:
            return []
        ch = self._open()
        ch.feats.push(x)
        ev: list[Event] = []
        for c in ch.chunks(final=False):
            ev += self.send(c)[0]
        return ev

    def finalize(self, reason: str) -> list[Event]:
        """The rest of the stream (the last chunk ends the server's sequence), then the segment's final."""
        ev: list[Event] = []
        if self.chunker is not None and self.chunker.feats.total > 0:
            self.chunker.feats.finish()
            for c in self.chunker.chunks(final=True):
                ev += self.send(c)[0]
        self.chunker = None
        ev.append(self.final(reason))
        return ev


def join_text(finals: Sequence[Event]) -> str:
    return " ".join(str(f.get("text") or "") for f in finals if f.get("text")).strip()
