"""``dataset_freeze@1`` — turn split, filtered segments into a dataset version: a draft, then the frozen copy.

``mode: draft`` (the end of ``pipelines/data-ingest.yaml``) writes a ``dataset`` artifact of format
``cadence.dataset-draft/1``: the would-be members (mount URI, canonical hash, text, split), the quality checks, the
statistics and the dataset card — no audio. The control plane registers it as a draft dataset version: hashes are
known, so leakage checks and fingerprints work before anything is copied.

``mode: cut`` (what ``datasets.freeze`` runs) re-decodes every file from its mount, cuts the segments, checks each
against its hash and writes the first copy — a ``cadence.dataset/1`` artifact the phase-2 training path reads —
with the quality checks measured on the cut audio, the card, and the members grouped into shards (a Lhotse cuts file
each, ``shards/cuts.NNNNNN.jsonl.gz``): the unit of pinning and eviction. Help: docs/help/steps/dataset-freeze.md.
"""

from __future__ import annotations

import bisect
import gzip
import io
import json
import math
import tempfile
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar, Literal

import numpy as np
from pydantic import BaseModel

from cadence_worker import ingest_mounts as mounts
from cadence_worker import segments as seg
from cadence_worker.cas import valid_hash
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field

KIND = "dataset_freeze@1"
DRAFT_FORMAT = "cadence.dataset-draft/1"
DATASET_FORMAT = "cadence.dataset/1"
SPLITS = ("train", "validation", "test")
SPLIT_RULES = ("speaker-disjoint", "source", "all-train", "all-validation", "all-test")
DURATION_EDGES = [0, 1, 2, 4, 6, 8, 10, 15, 20, 25, 30]
CPS_EDGES = list(range(0, 32, 2))
LEVEL_EDGES = [-60, -50, -40, -30, -20, -10]
CLIPPED = 0.001  # a segment clips when more than this share of its samples is at full scale


class DatasetFreezeParams(BaseModel):
    mode: Literal["draft", "cut"] = cadence_field(
        "draft",
        description="draft registers the would-be version without audio; cut (datasets.freeze) writes the frozen copy",
        source="docs/review/2026-10-03-phase-4-plan.md decisions 3 and 4",
        range={"values": ["draft", "cut"]},
    )
    name: str = cadence_field(
        "",
        description="Registry collection of the dataset version, without dataset/; empty = the source name",
        source="Cadence recommendation",
        range={"pattern": "^([a-z0-9][a-z0-9._-]{0,98}[a-z0-9])?$"},
    )
    description: str = cadence_field(
        "",
        description="What the version is for, shown in the Library and the card",
        source="Cadence recommendation",
        range={"maxLength": 2000},
    )
    tags: list[str] = cadence_field(
        [],
        description="Collection tags (domain:telephony, synthetic, …)",
        source="Cadence recommendation",
        range={"maxLength": 32},
    )
    eval_only: bool = cadence_field(
        False,
        description="Register the version for evaluation only (never mixed or trained on)",
        source="docs/spec/08-resolutions.md R18",
        range={"values": [True, False]},
    )
    draft_version: str = cadence_field(
        "",
        description="The draft dataset version (ver_…) a cut freezes; set by datasets.freeze, required in cut mode",
        source="docs/review/2026-10-03-phase-4-plan.md decision 4",
        range={"maxLength": 64},
    )
    shard_utterances: int = cadence_field(default_ref="data.shard_utterances")
    quality_max_silence_share: float = cadence_field(default_ref="data.quality_max_silence_share")
    quality_max_clipped_share: float = cadence_field(default_ref="data.quality_max_clipped_share")
    quality_outlier_z: float = cadence_field(default_ref="data.quality_outlier_z")
    quality_max_outlier_share: float = cadence_field(default_ref="data.quality_max_outlier_share")


class DatasetFreezeStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"dataset": "dataset"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = DatasetFreezeParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = DatasetFreezeParams.model_validate(params.model_dump())
        header, lines = seg.read(inputs["segments"])
        freeze(p, header, lines, outputs["dataset"], ctx)


# ---------------------------------------------------------------- members


def members(lines: Sequence[Mapping[str, Any]], ctx: Any = None) -> list[dict[str, Any]]:
    """The segments that become members, checked; a repeated hash keeps its first segment."""
    out: list[dict[str, Any]] = []
    seen: set[str] = set()
    dup = 0
    for n, x in enumerate(lines, 1):
        where = f"segment {n} ({x.get('uri', '?')})"
        text = x.get("text")
        if not isinstance(text, str) or not text.strip():
            raise StepInputError(f"{where} has no text: pseudo-label it or drop it (manifest_filter require_text)")
        if not str(x.get("language") or "").strip():
            raise StepInputError(f"{where} has no language: set sdp_ingest language or a sidecar language")
        if x.get("split") not in SPLITS:
            raise StepInputError(f"{where} has no split: run speaker_disjoint_split before dataset_freeze")
        origin = str(x.get("origin") or "human")
        if origin == "pseudo-label:disputed":
            raise StepInputError(
                f"{where} is a disputed pseudo-label; disputes go to triage (manifest_filter drops them)"
            )
        if origin not in ("human", "pseudo-label") and not (origin.startswith("model:") and len(origin) > 6):
            raise StepInputError(f"{where}: origin {origin!r} is not human, pseudo-label or model:<id>")
        h = str(x.get("hash") or "")
        if not h.startswith("b3:") or not x.get("uri"):
            raise StepInputError(f"{where} has no uri or hash: it does not come from sdp_ingest")
        if h in seen:
            dup += 1
            continue
        seen.add(h)
        out.append({**x, "origin": origin})
    if dup:
        log = getattr(ctx, "log", None)
        if callable(log):
            log(f"{dup} segments repeat the audio of an earlier one and were left out", "warn")
    if not out:
        raise StepInputError("no segments to freeze")
    return out


def member_line(x: Mapping[str, Any]) -> dict[str, Any]:
    line: dict[str, Any] = {
        "duration": float(x["duration"]),
        "sampleRate": seg.RATE,
        "channels": 1,
        "language": str(x["language"]),
        "text": str(x["text"]),
        "origin": str(x["origin"]),
        "split": str(x["split"]),
        "uri": str(x["uri"]),
        "role": str(x.get("role") or "mono"),
    }
    if x.get("speaker"):
        line["speaker"] = str(x["speaker"])
    if isinstance(x.get("confidence"), int | float):
        line["confidence"] = float(x["confidence"])
    # The whole source track's canonical hash: the leakage check matches it against every golden utterance's audio, so
    # a golden set's file re-cut by VAD on a mount is still found (sdp_ingest@2).
    whole = x.get(seg.FILE_FINGERPRINT)
    if isinstance(whole, str) and valid_hash(whole):
        line["fingerprints"] = {seg.FILE_FINGERPRINT: whole}
    # An annotation batch's rows carry entity spans (names, addresses) entity_score reads from the reference.
    ents = [e for e in x.get("entities") or [] if isinstance(e, Mapping) and isinstance(e.get("class"), str)]
    if ents:
        line["entities"] = [dict(e) for e in ents]
    return line


# ---------------------------------------------------------------- quality, statistics, card


def histogram(values: Sequence[float], edges: Sequence[float]) -> dict[str, list[Any]]:
    counts = [0] * len(edges)
    for v in values:
        counts[max(0, bisect.bisect_right(edges, v) - 1)] += 1
    return {"edges": list(edges), "counts": counts}


def cps(x: Mapping[str, Any]) -> float:
    d = float(x["duration"])
    return len("".join(str(x["text"]).split())) / d if d > 0 else 0.0


def quality(p: DatasetFreezeParams, xs: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    ratios = [float(x["vad"]["ratio"]) for x in xs if isinstance(x.get("vad"), Mapping) and "ratio" in x["vad"]]
    silence = 1 - sum(ratios) / len(ratios) if ratios else 0.0
    clips = [float(x["level"].get("clipping", 0.0)) for x in xs if isinstance(x.get("level"), Mapping)]
    clipped = sum(1 for c in clips if c > CLIPPED) / len(xs)
    rates = np.array([cps(x) for x in xs], dtype=np.float64)
    sd = float(rates.std())
    outliers = float(np.mean(np.abs(rates - rates.mean()) / sd > p.quality_outlier_z)) if sd > 0 else 0.0

    def check(name: str, value: float, threshold: float, what: str) -> dict[str, Any]:
        ok = value <= threshold
        return {
            "name": name,
            "status": "pass" if ok else "warn",
            "value": round(value, 4),
            "threshold": threshold,
            "message": f"{what} {value:.1%} {'≤' if ok else '>'} {threshold:.1%}",
        }

    checks = [
        check("silence_share", silence, p.quality_max_silence_share, "mean silence share"),
        check("clipping_share", clipped, p.quality_max_clipped_share, "segments that clip"),
        check(
            "length_outliers",
            outliers,
            p.quality_max_outlier_share,
            f"transcripts beyond {p.quality_outlier_z:g} standard deviations of characters per second",
        ),
    ]
    return {"passed": all(c["status"] == "pass" for c in checks), "checks": checks}


def _groups(xs: Sequence[Mapping[str, Any]], key: str, default: str) -> list[dict[str, Any]]:
    acc: dict[str, list[float]] = {}
    for x in xs:
        acc.setdefault(str(x.get(key) or default), []).append(float(x["duration"]))
    return [{key: k, "utterances": len(v), "hours": round(sum(v) / 3600, 4)} for k, v in sorted(acc.items())]


def stats(xs: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    durations = [float(x["duration"]) for x in xs]
    levels = [float(x["level"]["rmsDb"]) for x in xs if isinstance(x.get("level"), Mapping) and "rmsDb" in x["level"]]
    pct = np.percentile(np.array(durations), [5, 50, 95])
    rates: dict[int, int] = {}
    for x in xs:
        r = int(x.get("sourceRate") or seg.RATE)
        rates[r] = rates.get(r, 0) + 1
    return {
        "durationHistogram": histogram(durations, DURATION_EDGES),
        "charsPerSecondHistogram": histogram([cps(x) for x in xs], CPS_EDGES),
        "levelHistogram": histogram(levels, LEVEL_EDGES),
        "durationPercentiles": {
            "p5": round(float(pct[0]), 3),
            "p50": round(float(pct[1]), 3),
            "p95": round(float(pct[2]), 3),
        },
        "origins": _groups(xs, "origin", "human"),
        "roles": _groups(xs, "role", "mono"),
        "sourceRates": [{"rate": r, "utterances": n} for r, n in sorted(rates.items())],
        "speakers": len({str(x["speaker"]) for x in xs if x.get("speaker")}),
    }


def card(
    p: DatasetFreezeParams, header: Mapping[str, Any], xs: Sequence[Mapping[str, Any]], q: Mapping[str, Any]
) -> str:
    source = str((header.get("source") or {}).get("name") or "")
    title = "dataset/" + (p.name or source)
    hours = sum(float(x["duration"]) for x in xs) / 3600
    st = stats(xs)
    langs = sorted({str(x["language"]) for x in xs})
    out = [f"# {title}", ""]
    if p.description:
        out += [p.description, ""]
    out += [
        "| | |",
        "| --- | --- |",
        f"| Utterances | {len(xs)} |",
        f"| Hours | {hours:.3f} |",
        f"| Speakers | {st['speakers']} |",
        f"| Languages | {', '.join(langs)} |",
    ]
    for s in SPLITS:
        part = [x for x in xs if x["split"] == s]
        if part:
            out.append(f"| {s} | {len(part)} utterances, {sum(float(x['duration']) for x in part) / 3600:.3f} h |")
    out += ["", f"Source: `{source}` (licence: see source {source} in the registry)."]
    if header.get("root"):
        out.append(f"Indexed from `{header['root']}`.")
    if p.eval_only:
        out.append("Registered for evaluation only: never mixed or trained on.")
    out += ["", "## Quality checks", "", "| Check | Status | Value | Threshold |", "| --- | --- | --- | --- |"]
    for c in q["checks"]:
        out.append(f"| {c['name']} | {c['status']} | {c['value']} | {c['threshold']} |")
    dp = st["durationPercentiles"]
    out += ["", f"Duration percentiles: p5 {dp['p5']} s, p50 {dp['p50']} s, p95 {dp['p95']} s.", ""]
    origins = ", ".join(f"{o['origin']} {o['utterances']}" for o in st["origins"])
    out += [f"Transcript origins: {origins}.", ""]
    steps = [str(s) for s in header.get("steps") or []]
    if steps:
        out += ["## Recipe", "", "Segments went through: " + " → ".join(f"`{s}`" for s in [*steps, KIND]) + ".", ""]
    if header.get("filtered"):
        dropped = ", ".join(f"{k} {v}" for k, v in sorted(dict(header["filtered"]).items()))
        out += [f"Dropped by filters: {dropped}.", ""]
    return "\n".join(out)


# ---------------------------------------------------------------- writing


def base_header(p: DatasetFreezeParams, header: Mapping[str, Any], xs: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    rule = str(header.get("splitRule") or "speaker-disjoint")
    h: dict[str, Any] = {
        "source": {"name": str((header.get("source") or {}).get("name") or "")},
        "splitRule": rule if rule in SPLIT_RULES else "speaker-disjoint",
        "counts": {s: sum(1 for x in xs if x["split"] == s) for s in SPLITS},
        "hours": sum(float(x["duration"]) for x in xs) / 3600,
        "card": "card.md",
    }
    if not h["source"]["name"]:
        raise StepInputError("the segments name no source (segments.json source.name)")
    if p.name:
        h["name"] = p.name
    if p.description:
        h["description"] = p.description
    tags = list(dict.fromkeys(p.tags))
    if p.eval_only and "eval-only" not in tags:
        tags.append("eval-only")
    if tags:
        h["tags"] = tags
    if p.eval_only:
        h["evalOnly"] = True
    return h


def _write_json(path: Path, doc: Any) -> None:
    path.write_text(json.dumps(doc, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _write_lines(path: Path, rows: Sequence[Mapping[str, Any]]) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False, sort_keys=True) + "\n")


def freeze(
    p: DatasetFreezeParams, header: Mapping[str, Any], lines: Sequence[Mapping[str, Any]], out: Path, ctx: Any = None
) -> dict[str, Any]:
    xs = members(lines, ctx)
    if p.mode == "cut":
        if not p.draft_version:
            raise StepInputError("cut mode needs draft_version: the draft dataset version datasets.freeze freezes")
        xs = cut(xs, out, ctx)
    out.mkdir(parents=True, exist_ok=True)
    h = base_header(p, header, xs)
    q = quality(p, xs)
    h["quality"] = q
    h["stats"] = stats(xs)
    if isinstance(header.get("sourceInfo"), Mapping):
        h["sourceInfo"] = dict(header["sourceInfo"])
    rows = []
    for x in xs:
        line = member_line(x)
        if p.mode == "cut":
            line["audio"] = x["audio"]
        else:
            line["hash"], line["bytes"] = str(x["hash"]), int(x["bytes"])
        rows.append(line)
    if p.mode == "cut":
        h["format"] = DATASET_FORMAT
        h["draftVersionId"] = p.draft_version
        h.pop("sourceInfo", None)
        h["shards"] = shards(xs, out, p.shard_utterances)
    else:
        h["format"] = DRAFT_FORMAT
    _write_lines(out / "manifest.jsonl", rows)
    (out / "card.md").write_text(card(p, header, xs, q) + "\n", encoding="utf-8")
    _write_json(out / "dataset.json", h)
    report = getattr(ctx, "progress", None)
    if callable(report):
        report(1.0, f"{p.mode}: {len(xs)} utterances, {h['hours']:.3f} h")
    return h


def cut(xs: list[dict[str, Any]], out: Path, ctx: Any = None) -> list[dict[str, Any]]:
    """Cut every member from its file, check its hash, write audio/<hex[:2]>/<hex>.wav; level and clipping are
    measured again on the cut audio. Files are decoded once each, in first-use order."""
    ms = mounts.mounts_of(ctx)
    by_file: dict[str, list[int]] = {}
    for i, x in enumerate(xs):
        ref = mounts.parse(str(x["uri"]))
        by_file.setdefault(ref.file_uri, []).append(i)
    done = 0
    with tempfile.TemporaryDirectory(prefix="dataset-freeze-") as tmp:
        for file_uri, idx in by_file.items():
            d = seg.decode(mounts.resolve(file_uri, ms), Path(tmp))
            tracks: dict[int, seg.Signal] = {}
            for i in idx:
                x = xs[i]
                ref = mounts.parse(str(x["uri"]))
                c = int(x.get("channel", ref.channel if ref.channel is not None else seg.MIXED))
                if c not in tracks:
                    tracks[c] = seg.track(d, c)
                i0, i1 = seg.bounds(float(x["start"]), float(x["end"]))
                wav = seg.wav_of(tracks[c], i0, i1)
                h = seg.hash_of(wav)
                if h != x["hash"]:
                    raise StepInputError(
                        f"{x['uri']}: the audio on the mount no longer matches the indexed segment ({h} ≠ {x['hash']});"
                        " the file changed since ingest — ingest it again"
                    )
                hexd = h.removeprefix("b3:")
                rel = f"audio/{hexd[:2]}/{hexd}.wav"
                dst = out / rel
                dst.parent.mkdir(parents=True, exist_ok=True)
                dst.write_bytes(wav)
                x["audio"] = rel
                x["level"] = seg.level(tracks[c][i0:i1])
                done += 1
            report = getattr(ctx, "progress", None)
            if callable(report):
                report(done / len(xs) * 0.95, f"{done}/{len(xs)} segments cut")
    return xs


def shards(xs: Sequence[Mapping[str, Any]], out: Path, size: int) -> list[dict[str, Any]]:
    """Group members into shards of size, each a deterministic Lhotse cuts file (MonoCut per member, its recording a
    file inside the artifact)."""
    (out / "shards").mkdir(parents=True, exist_ok=True)
    result = []
    for k in range(math.ceil(len(xs) / size)):
        part = xs[k * size : (k + 1) * size]
        rel = f"shards/cuts.{k:06d}.jsonl.gz"
        buf = io.BytesIO()
        with gzip.GzipFile(filename="", mode="wb", fileobj=buf, mtime=0) as gz:
            for x in part:
                gz.write((json.dumps(lhotse_cut(x), ensure_ascii=False, sort_keys=True) + "\n").encode("utf-8"))
        (out / rel).write_bytes(buf.getvalue())
        result.append(
            {
                "index": k,
                "cuts": rel,
                "utterances": len(part),
                "bytes": sum(int(x["bytes"]) for x in part),
                "seconds": round(sum(float(x["duration"]) for x in part), 3),
            }
        )
    return result


def lhotse_cut(x: Mapping[str, Any]) -> dict[str, Any]:
    cid = str(x["hash"]).removeprefix("b3:")
    dur = float(x["duration"])
    n = round(dur * seg.RATE)
    sup: dict[str, Any] = {
        "id": cid,
        "recording_id": cid,
        "start": 0.0,
        "duration": dur,
        "channel": 0,
        "text": str(x["text"]),
        "language": str(x["language"]),
    }
    if x.get("speaker"):
        sup["speaker"] = str(x["speaker"])
    return {
        "id": cid,
        "start": 0.0,
        "duration": dur,
        "channel": 0,
        "supervisions": [sup],
        "recording": {
            "id": cid,
            "sources": [{"type": "file", "channels": [0], "source": str(x["audio"])}],
            "sampling_rate": seg.RATE,
            "num_samples": n,
            "duration": dur,
            "channel_ids": [0],
        },
        "custom": {"uri": str(x["uri"]), "origin": str(x["origin"]), "split": str(x["split"])},
        "type": "MonoCut",
    }
