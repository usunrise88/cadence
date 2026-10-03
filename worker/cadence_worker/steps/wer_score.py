"""``wer_score@2`` — the runtime-neutral word error rate scorer (docs/review/2026-10-02-phase-3-plan.md "The scores
artifact"; R21, R54). Version 2 computes CER without spaces (version 1 counted them, which inflated the CER of
languages written without spaces between words and so the CER gate of eval.character_error_languages).

Consumes ``hypotheses`` (a transcribe step's JSON lines, R42), the ``dataset`` they decode (the golden set's dataset
artifact) and a ``normalizer`` (a scoring normalizer payload, :mod:`cadence_worker.normalize`); produces ``scores``, a
directory artifact:

    summary.json      {schema: cadence.scores/1, scorer: wer_score@2, normalizer: {versionId, hash}, language,
                      utterances, refWords, refChars, wer, cer, werNoPunct, sub, del, ins, charErrors,
                      buckets: [{lo, hi, utterances, refWords, wer}], stability?: {partialWords, unstableWords, ratio,
                      editsPerSecond}, groups, hypotheses: {family, weightsHash, decodingHash, profile}}
    utterances.jsonl  one row per utterance in dataset order: {audio, speaker?, group, durationS, ref, hyp, refWords,
                      sub, del, ins, refChars, charErrors, ops: [[op, ref, hyp]]}

Hypotheses join the dataset by the BLAKE3 hash of each audio file. Words are the normalized text split on spaces; S/D/I
come from a Levenshtein alignment; CER is the character edit distance over the normalized texts with every space
removed (as FLEURS and Whisper report it: a space is not a character a reader checks, and a language written without
spaces would otherwise count a segmenter's spaces as errors), ``refChars`` the reference's characters without spaces;
``werNoPunct`` uses the same normalizer with punctuation stripped. Rates are fractions. ``group`` is the bootstrap's
resampling unit (R54): the call id when every utterance has one (``callId``), else the speaker when every utterance
has one, else the audio hash (``groups`` names which). Partial stability (Shangguan et al., Interspeech 2020) comes
from the partial events when the hypotheses carry them. Help: docs/help/steps/wer-score.md.
"""

from __future__ import annotations

import json
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass
from itertools import pairwise
from pathlib import Path, PurePosixPath
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker.align import align, char_distance
from cadence_worker.cas import hash_file
from cadence_worker.normalize import Normalizer, NormalizerError
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

SCHEMA = "cadence.scores/1"
SCORER = "wer_score@2"
DATASET_FORMAT = "cadence.dataset/1"
CALL_FIELDS = ("callId", "call")


class WerScoreParams(BaseModel):
    duration_buckets_s: list[float] = cadence_field(
        description="Lower bounds of the utterance-duration buckets WER is reported for, in seconds (ascending); the "
        "last bucket is open",
        default_ref="eval.duration_buckets_s",
    )


@dataclass
class Reference:
    audio: str  # b3 hash of the audio file
    text: str
    duration: float
    language: str
    speaker: str = ""
    call: str = ""


def read_references(root: Path) -> list[Reference]:
    """The utterances of a materialised ``dataset`` artifact in manifest order, each with its audio hash."""
    if not root.is_dir() or not (root / "manifest.jsonl").is_file():
        raise StepInputError("the data input is not a dataset artifact (dataset.json, manifest.jsonl, audio/)")
    try:
        header = json.loads((root / "dataset.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the dataset has no readable dataset.json: {e}") from e
    if not isinstance(header, dict) or header.get("format") != DATASET_FORMAT:
        raise StepInputError(f"dataset.json is not {DATASET_FORMAT}")
    out: list[Reference] = []
    for n, line in enumerate((root / "manifest.jsonl").read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"dataset manifest line {n} is not JSON") from e
        rel = row.get("audio") if isinstance(row, dict) else None
        if not isinstance(rel, str) or not isinstance(row.get("text"), str):
            raise StepInputError(f"dataset manifest line {n} lacks audio or text")
        p = PurePosixPath(rel)
        if not p.parts or p.is_absolute() or ".." in p.parts:
            raise StepInputError(f"dataset manifest line {n}: audio {rel!r} is not a path inside the artifact")
        f = root.joinpath(*p.parts)
        if not f.is_file():
            raise StepInputError(f"dataset manifest line {n}: audio {rel} is not in the artifact")
        call = next((str(row[k]) for k in CALL_FIELDS if row.get(k) not in (None, "")), "")
        out.append(
            Reference(
                audio=hash_file(f),
                text=row["text"],
                duration=float(row.get("duration") or 0.0),
                language=str(row.get("language") or ""),
                speaker=str(row.get("speaker") or ""),
                call=call,
            )
        )
    if not out:
        raise StepInputError("the dataset has no utterances")
    return out


def read_hypotheses(path: Path) -> dict[str, dict[str, Any]]:
    """Hypothesis rows by audio hash."""
    out: dict[str, dict[str, Any]] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as e:
        raise StepInputError(f"cannot read the hypotheses: {e}") from e
    for n, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            row = json.loads(line)
        except ValueError as e:
            raise StepInputError(f"hypotheses line {n} is not JSON") from e
        if not isinstance(row, dict) or not isinstance(row.get("audio"), str) or not isinstance(row.get("text"), str):
            raise StepInputError(f"hypotheses line {n} lacks audio or text")
        if row["audio"] in out:
            raise StepInputError(f"hypotheses line {n}: audio {row['audio']} has a second hypothesis")
        out[row["audio"]] = row
    return out


def group_unit(refs: Sequence[Reference]) -> str:
    if all(r.call for r in refs):
        return "call"
    if all(r.speaker for r in refs):
        return "speaker"
    return "utterance"


def group_of(r: Reference, unit: str) -> str:
    return r.call if unit == "call" else r.speaker if unit == "speaker" else r.audio


@dataclass
class Stability:
    partial_words: int = 0
    unstable_words: int = 0
    edits: int = 0
    seconds: float = 0.0

    def add(self, other: Stability) -> None:
        self.partial_words += other.partial_words
        self.unstable_words += other.unstable_words
        self.edits += other.edits
        self.seconds += other.seconds

    def to_json(self) -> dict[str, Any]:
        return {
            "partialWords": self.partial_words,
            "unstableWords": self.unstable_words,
            "ratio": self.unstable_words / self.partial_words if self.partial_words else 0.0,
            "editsPerSecond": self.edits / self.seconds if self.seconds > 0 else 0.0,
        }


def partial_stability(partials: Iterable[Mapping[str, Any]], final: str, norm: Normalizer, seconds: float) -> Stability:
    """Shangguan et al.'s unstable partial word ratio for one utterance, by word position on the normalized texts.

    A word is *shown* when a partial puts it at a position the previous partial held empty or held another word; it is
    *unstable* when the final text does not have it at that position (changed or dropped). An *edit* is a word of a
    partial that the next partial (or the final) changes or drops.

    The final is not a partial: the event that carries it (``final: true``, a decode's last event) shows no words to
    count, so the ratio's denominator holds only words a reader saw before the end (version 1 counted the final's words
    as shown, stable by definition, which lowered the ratio). The final still ends the last partial: its changes are
    edits."""
    seqs = [norm.words(str(p.get("text") or "")) for p in partials if p.get("final") is not True]
    fin = norm.words(final)
    st = Stability(seconds=seconds)
    prev: list[str] = []
    for words in seqs:
        for i, w in enumerate(words):
            if i >= len(prev) or prev[i] != w:
                st.partial_words += 1
                if i >= len(fin) or fin[i] != w:
                    st.unstable_words += 1
        st.edits += sum(1 for i, w in enumerate(prev) if i >= len(words) or words[i] != w)
        prev = words
    st.edits += sum(1 for i, w in enumerate(prev) if i >= len(fin) or fin[i] != w)
    return st


def _rate(errors: int, total: int) -> float:
    return errors / total if total else float(errors > 0)


def buckets_of(bounds: Sequence[float]) -> list[tuple[float, float | None]]:
    if not bounds:
        return []
    if any(b < 0 for b in bounds) or any(b >= c for b, c in pairwise(bounds)):
        raise StepInputError(f"duration_buckets_s must be ascending and non-negative: {list(bounds)}")
    return [(float(b), float(bounds[i + 1]) if i + 1 < len(bounds) else None) for i, b in enumerate(bounds)]


def score(
    refs: Sequence[Reference],
    hyps: Mapping[str, Mapping[str, Any]],
    norm: Normalizer,
    bounds: Sequence[float],
) -> tuple[dict[str, Any], list[dict[str, Any]], list[str]]:
    """The summary, the per-utterance rows and warnings. Raises StepInputError when utterances lack a hypothesis."""
    missing = [r.audio for r in refs if r.audio not in hyps]
    if missing:
        raise StepInputError(
            f"{len(missing)} of {len(refs)} utterances have no hypothesis (first: {', '.join(missing[:3])}); "
            "score the hypotheses of this dataset"
        )
    warnings: list[str] = []
    known = {r.audio for r in refs}
    if extra := [a for a in hyps if a not in known]:
        warnings.append(f"{len(extra)} hypotheses belong to no utterance of the dataset and are ignored")
    nopunct = norm.without_punctuation()
    unit = group_unit(refs)
    buckets = buckets_of(bounds)
    per_bucket = [[0, 0, 0] for _ in buckets]  # utterances, refWords, errors
    totals = {"refWords": 0, "refChars": 0, "sub": 0, "del": 0, "ins": 0, "charErrors": 0, "npErr": 0, "npWords": 0}
    stability: Stability | None = None
    rows: list[dict[str, Any]] = []
    for r in refs:
        h = hyps[r.audio]
        ref_n, hyp_n = norm(r.text), norm(str(h["text"]))
        ref_w, hyp_w = ref_n.split(), hyp_n.split()
        a = align(ref_w, hyp_w)
        ref_c, hyp_c = "".join(ref_w), "".join(hyp_w)  # CER over the characters, spaces removed
        ce = char_distance(ref_c, hyp_c)
        if nopunct is norm:
            np_err, np_words = a.errors, len(ref_w)
        else:
            np_ref = nopunct.words(r.text)
            np_err, np_words = align(np_ref, nopunct.words(str(h["text"]))).errors, len(np_ref)
        totals["refWords"] += len(ref_w)
        totals["refChars"] += len(ref_c)
        totals["sub"] += a.sub
        totals["del"] += a.dele
        totals["ins"] += a.ins
        totals["charErrors"] += ce
        totals["npErr"] += np_err
        totals["npWords"] += np_words
        for i, (lo, hi) in enumerate(buckets):
            if r.duration >= lo and (hi is None or r.duration < hi):
                per_bucket[i][0] += 1
                per_bucket[i][1] += len(ref_w)
                per_bucket[i][2] += a.errors
                break
        partials = h.get("partials")
        if isinstance(partials, list) and partials:
            st = partial_stability(partials, str(h["text"]), norm, r.duration)
            stability = stability or Stability()
            stability.add(st)
        row: dict[str, Any] = {"audio": r.audio}
        if r.speaker:
            row["speaker"] = r.speaker
        row.update(
            {
                "group": group_of(r, unit),
                "durationS": r.duration,
                "ref": ref_n,
                "hyp": hyp_n,
                "refWords": len(ref_w),
                "sub": a.sub,
                "del": a.dele,
                "ins": a.ins,
                "refChars": len(ref_c),
                "charErrors": ce,
                "ops": [list(op) for op in a.ops],
            }
        )
        rows.append(row)
    errors = totals["sub"] + totals["del"] + totals["ins"]
    languages = sorted({r.language for r in refs if r.language})
    first = hyps[refs[0].audio]
    decoding = first.get("decoding")
    summary: dict[str, Any] = {
        "schema": SCHEMA,
        "scorer": SCORER,
        "normalizer": {"versionId": norm.payload.versionId},
        "language": languages[0] if len(languages) == 1 else ("mul" if languages else ""),
        "utterances": len(refs),
        "refWords": totals["refWords"],
        "refChars": totals["refChars"],
        "wer": _rate(errors, totals["refWords"]),
        "cer": _rate(totals["charErrors"], totals["refChars"]),
        "werNoPunct": _rate(totals["npErr"], totals["npWords"]),
        "sub": totals["sub"],
        "del": totals["del"],
        "ins": totals["ins"],
        "charErrors": totals["charErrors"],
        "buckets": [
            {
                "lo": lo,
                "hi": hi,
                "utterances": n,
                "refWords": words,
                "wer": _rate(err, words) if n else None,
            }
            for (lo, hi), (n, words, err) in zip(buckets, per_bucket, strict=True)
        ],
        "groups": unit,
        "hypotheses": {
            "family": first.get("family"),
            "weightsHash": first.get("weightsHash"),
            "decodingHash": first.get("decodingHash"),
            "profile": decoding.get("profile") if isinstance(decoding, dict) else None,
        },
    }
    if stability is not None:
        summary["stability"] = stability.to_json()
    return summary, rows, warnings


class WerScoreStep:
    version: ClassVar[str] = "2"
    consumes: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "data": "dataset", "normalizer": "normalizer"}
    produces: ClassVar[Mapping[str, str]] = {"scores": "scores"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = WerScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = WerScoreParams.model_validate(params.model_dump())
        for name in self.consumes:
            if name not in inputs:
                raise StepInputError(f"wer_score needs its {name} input")
        try:
            norm = Normalizer.from_file(inputs["normalizer"])
        except NormalizerError as e:
            raise StepInputError(str(e)) from e
        refs = read_references(inputs["data"])
        hyps = read_hypotheses(inputs["hypotheses"])
        summary, rows, warnings = score(refs, hyps, norm, p.duration_buckets_s)
        summary["normalizer"]["hash"] = hash_file(inputs["normalizer"])
        out = outputs["scores"]
        out.mkdir(parents=True, exist_ok=True)
        with (out / "utterances.jsonl").open("w", encoding="utf-8") as f:
            for row in rows:
                f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
        (out / "summary.json").write_text(
            json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        for w in warnings:
            ctx.log(w, level="warn")
        for name, key in (("wer", "wer"), ("cer", "cer"), ("wer_no_punct", "werNoPunct")):
            ctx.final_metric(name, summary[key])
        meta = {
            "schema": SCHEMA,
            "scorer": SCORER,
            "utterances": summary["utterances"],
            "refWords": summary["refWords"],
            "wer": summary["wer"],
            "cer": summary["cer"],
            "werNoPunct": summary["werNoPunct"],
            "normalizerVersionId": norm.payload.versionId,
            **{k: v for k, v in summary["hypotheses"].items() if v is not None},
        }
        ctx.set_meta("scores", meta)
        ctx.progress(1.0, f"WER {summary['wer']:.4f} over {summary['utterances']} utterances")
