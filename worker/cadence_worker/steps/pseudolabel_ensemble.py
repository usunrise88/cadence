"""``pseudolabel_ensemble@2`` — combine the hypotheses of several pseudo-label members into one text per segment, or
a dispute (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for phase 4" 6-7, R26; Granary-style agreement).

Consumes ``segments`` (``cadence.segments/1``), ``hypotheses`` from each member wired as ``hypotheses.0``,
``hypotheses.1``, … (rows keyed by the segment's audio hash, :mod:`cadence_worker.members`), the scoring
``normalizer`` and optionally ``lid`` (a ``lid_classify`` output). Produces ``segments`` (every input row, with
``text``, ``origin`` and ``confidence`` on the segments it labelled) and ``hypotheses`` (one row per labelled
segment: the chosen text and every member's text).

Per segment without a text of its own (a segment that already has text and an origin that is not a pseudo-label,
such as the bot channel's TTS script, passes unchanged):

1. Candidates are the members that wrote a hypothesis for it. Fewer than ``min_members`` → disputed
   ``too-few-members``; every text empty after the normalizer → ``no-speech``. The step itself refuses to run with
   fewer than ``min_members`` members wired: a one-member "ensemble" would agree with itself.
2. Pairwise WER after the scoring normalizer: word edit distance over the longer of the two texts (symmetric, so the
   order of members does not matter). Members within ``max_pairwise_wer`` of at least one other member agree; fewer
   than ``min_agreeing_members`` agreeing → ``disagreement``.
3. Language identification must agree with the segment's language (or the dataset's): the ``lid`` row when its
   confidence reaches ``lid_min_confidence``, else the members' own detected languages (Whisper's second opinion),
   compared by primary subtag or within one of ``lid_equivalents``. A mismatch → ``lid-mismatch``; no evidence while
   ``require_lid`` → ``lid-unknown``; a segment without a language skips the check.
4. The pick among the agreeing members: with ``prefer_written_form`` (the default) first a member whose text is in
   written form — capitals or sentence punctuation, as Whisper writes — over one in spoken form (OASIS writes
   lowercase without punctuation), so a label keeps the training style; then a member marked ``vote`` (itself an
   ensemble); then the lowest mean WER to the others. Its text is written as the member wrote it.
   Confidence = (agreeing members / members) x (1 - the pick's mean WER to the other agreeing members).

A kept segment gets origin ``pseudo-label``; a disputed one ``pseudo-label:disputed`` with the best candidate as its
text and ``dispute: {reason, candidates, lid}``. Every row with language evidence gets ``lid: {language, confidence?,
agrees?, source}`` (``manifest_filter`` drops a mismatch). A segment whose audio repeats (the same hash twice) gets the
verdict on that audio on every row. The control plane puts disputed segments in the triage queue
(``triage.list``); ``manifest_filter`` drops them by default, so they never reach training. Help:
docs/help/steps/pseudolabel-ensemble.md.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker import segments as seg
from cadence_worker.cas import hash_file
from cadence_worker.members import read_jsonl_by_audio, same_language, write_jsonl
from cadence_worker.normalize import Normalizer, NormalizerError
from cadence_worker.protocol_gen import StepResources
from cadence_worker.scoring import edit_distance
from cadence_worker.segments import ORIGIN_DISPUTED, ORIGIN_PSEUDO, needs_label, read_segments
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext

KIND = "pseudolabel_ensemble@2"
MEMBER_INPUT = re.compile(r"^hypotheses(\.\d+)?$")


class EnsembleParams(BaseModel):
    max_pairwise_wer: float = cadence_field(default_ref="pseudolabel.max_pairwise_wer")
    min_agreeing_members: int = cadence_field(default_ref="pseudolabel.min_agreeing_members")
    lid_min_confidence: float = cadence_field(default_ref="pseudolabel.lid_min_confidence")
    lid_equivalents: list[list[str]] = cadence_field(default_ref="pseudolabel.lid_equivalents")
    require_lid: bool = cadence_field(default_ref="pseudolabel.require_lid")
    min_members: int = cadence_field(default_ref="pseudolabel.min_members")
    prefer_written_form: bool = cadence_field(default_ref="pseudolabel.prefer_written_form")


@dataclass
class Candidate:
    member: str
    text: str
    words: list[str]
    vote: bool = False
    confidence: float | None = None
    language: str = ""
    language_confidence: float | None = None
    mean_wer: float = 0.0


@dataclass
class Verdict:
    origin: str
    text: str
    confidence: float
    pick: str = ""
    reason: str = ""
    lid: dict[str, Any] = field(default_factory=dict)
    candidates: list[Candidate] = field(default_factory=list)


def pairwise_wer(a: Sequence[str], b: Sequence[str]) -> float:
    """Word edit distance over the longer text: 0 for equal texts, 1 when nothing matches (symmetric)."""
    longest = max(len(a), len(b))
    return edit_distance(list(a), list(b)) / longest if longest else 0.0


# Apostrophes and hyphens sit inside words of spoken-form text too ("don't", "e-mail"): they say nothing of style.
INWORD_PUNCTUATION = frozenset("'\u2019-\u2010")


def written_form(text: str) -> bool:
    """Whether a text is in written form: a capital letter or sentence punctuation (Whisper's "Dobar dan, hvala.")
    rather than spoken form (OASIS's "dobar dan hvala"). Scripts without case (Hebrew) count by punctuation."""
    for ch in text:
        if ch.isupper() or ch.istitle():
            return True
        if unicodedata.category(ch).startswith("P") and ch not in INWORD_PUNCTUATION:
            return True
    return False


def lid_verdict(
    expected: str, lid_row: Mapping[str, Any] | None, cands: Sequence[Candidate], p: EnsembleParams
) -> dict[str, Any]:
    """{language, confidence, source, agrees}; agrees is None without evidence or without an expected language."""
    out: dict[str, Any] = {}
    if lid_row is not None and float(lid_row.get("confidence") or 0.0) >= p.lid_min_confidence:
        out = {"language": str(lid_row.get("language") or ""), "confidence": float(lid_row.get("confidence") or 0.0)}
        out["source"] = "lid"
    else:
        detected = [c for c in cands if c.language]
        if detected:
            # The members' own detections (Whisper's language token): all must agree with the expected language.
            best = max(detected, key=lambda c: c.language_confidence or 0.0)
            out = {"language": best.language, "source": "members"}
            if best.language_confidence is not None:
                out["confidence"] = best.language_confidence
            if expected:
                out["agrees"] = all(same_language(expected, c.language, p.lid_equivalents) for c in detected)
                return out
    if not expected or "language" not in out:
        out["agrees"] = None
        return out
    out["agrees"] = same_language(expected, out["language"], p.lid_equivalents)
    return out


def decide(cands: list[Candidate], expected: str, lid_row: Mapping[str, Any] | None, p: EnsembleParams) -> Verdict:
    """The verdict on one segment (module docstring, steps 1-4)."""
    n = len(cands)
    wer = [[pairwise_wer(a.words, b.words) for b in cands] for a in cands]
    for i, c in enumerate(cands):
        others = [wer[i][j] for j in range(n) if j != i]
        c.mean_wer = sum(others) / len(others) if others else 1.0
    best = min(cands, key=lambda c: c.mean_wer) if cands else None
    lid = lid_verdict(expected, lid_row, cands, p)

    def disputed(reason: str) -> Verdict:
        conf = max(0.0, 1.0 - best.mean_wer) if best and n > 1 else 0.0
        return Verdict(ORIGIN_DISPUTED, best.text if best else "", round(conf, 4), "", reason, lid, cands)

    if n < max(2, p.min_members):
        return disputed("too-few-members")
    if all(not c.words for c in cands):
        return disputed("no-speech")
    agree = [i for i in range(n) if any(j != i and wer[i][j] <= p.max_pairwise_wer for j in range(n))]
    if len(agree) < p.min_agreeing_members:
        return disputed("disagreement")
    if lid.get("agrees") is False:
        return disputed("lid-mismatch")
    if lid.get("agrees") is None and expected and p.require_lid:
        return disputed("lid-unknown")

    def mean_to_agreeing(i: int) -> float:
        others = [wer[i][j] for j in agree if j != i]
        return sum(others) / len(others) if others else 1.0

    if p.prefer_written_form:
        pick = min(agree, key=lambda i: (not written_form(cands[i].text), not cands[i].vote, cands[i].mean_wer, i))
    else:
        voters = [i for i in agree if cands[i].vote]
        pick = voters[0] if voters else min(agree, key=lambda i: (cands[i].mean_wer, i))
    conf = len(agree) / n * (1.0 - mean_to_agreeing(pick))
    return Verdict(
        ORIGIN_PSEUDO, cands[pick].text, round(max(0.0, min(1.0, conf)), 4), cands[pick].member, "", lid, cands
    )


def candidate_json(c: Candidate) -> dict[str, Any]:
    out: dict[str, Any] = {"member": c.member, "text": c.text, "meanWer": round(c.mean_wer, 4)}
    if c.confidence is not None:
        out["confidence"] = c.confidence
    if c.language:
        out["language"] = c.language
    return out


def row_lid(lid: Mapping[str, Any]) -> dict[str, Any]:
    """The ``lid`` a segments row carries: the verdict's language, confidence, agreement and evidence."""
    return {k: lid[k] for k in ("language", "confidence", "agrees", "source") if k in lid and lid[k] is not None}


def labelled(row: Mapping[str, Any], v: Verdict) -> dict[str, Any]:
    """The segments row with the verdict v: text, origin, confidence, lid (when there is evidence), dispute."""
    out = {**row, "text": v.text, "origin": v.origin, "confidence": v.confidence}
    out.pop("dispute", None)
    out.pop("lid", None)
    if v.lid.get("language"):
        out["lid"] = row_lid(v.lid)
    if v.origin == ORIGIN_DISPUTED:
        dispute: dict[str, Any] = {"reason": v.reason, "candidates": [candidate_json(c) for c in v.candidates]}
        if v.lid:
            dispute["lid"] = {k: v.lid[k] for k in ("language", "confidence", "agrees") if k in v.lid}
        out["dispute"] = dispute
    return out


class PseudolabelEnsembleStep:
    version: ClassVar[str] = "2"
    consumes: ClassVar[Mapping[str, str]] = {
        "segments": "segments",
        "hypotheses": "hypotheses",
        "normalizer": "normalizer",
        "lid": "lid",
    }
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"lid"})
    produces: ClassVar[Mapping[str, str]] = {"segments": "segments", "hypotheses": "hypotheses"}
    resources: ClassVar[StepResources] = {"gpu": False, "memoryGb": 2, "diskGb": 1, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = EnsembleParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = EnsembleParams.model_validate(params.model_dump())
        try:
            norm = Normalizer.from_file(inputs["normalizer"])
        except NormalizerError as e:
            raise StepInputError(str(e)) from e
        header, rows = read_segments(inputs["segments"])
        names = sorted((k for k in inputs if MEMBER_INPUT.match(k)), key=lambda k: (len(k), k))
        if len(names) < max(2, p.min_members):
            raise StepInputError(
                f"wire at least {max(2, p.min_members)} members' hypotheses (hypotheses.0, hypotheses.1, …; "
                f"pseudolabel.min_members); got {names}: an ensemble of fewer agrees with itself"
            )
        members = [read_jsonl_by_audio(inputs[k], f"hypotheses {k}") for k in names]
        lid = read_jsonl_by_audio(inputs["lid"], "lid") if "lid" in inputs else {}
        default_language = str(header.get("language") or "")

        out_rows: list[dict[str, Any]] = []
        hyp_rows: list[dict[str, Any]] = []
        counts = {"labelled": 0, "disputed": 0, "passed": 0}
        reasons: dict[str, int] = {}
        labels: set[str] = set()
        verdicts: dict[str, Verdict] = {}  # by audio hash: a repeated segment gets the same verdict, one hypotheses row
        for i, row_in in enumerate(rows):
            if not needs_label(row_in):
                passed = dict(row_in)
                lid_row = lid.get(row_in["hash"])
                if lid_row is not None and float(lid_row.get("confidence") or 0.0) >= p.lid_min_confidence:
                    expected = str(row_in.get("language") or default_language)
                    passed["lid"] = row_lid(lid_verdict(expected, lid_row, [], p))
                out_rows.append(passed)
                counts["passed"] += 1
                continue
            h = row_in["hash"]
            if h in verdicts:
                v = verdicts[h]
                out_rows.append(labelled(row_in, v))
                counts["disputed" if v.origin == ORIGIN_DISPUTED else "labelled"] += 1
                if v.origin == ORIGIN_DISPUTED:
                    reasons[v.reason] = reasons.get(v.reason, 0) + 1
                continue
            cands: list[Candidate] = []
            for k, m in zip(names, members, strict=True):
                hy = m.get(h)
                if hy is None or not isinstance(hy.get("text"), str):
                    continue
                label = str(hy.get("member") or k)
                labels.add(label)
                cands.append(
                    Candidate(
                        member=label,
                        text=hy["text"],
                        words=norm.words(hy["text"]),
                        vote=bool(hy.get("vote")),
                        confidence=float(hy["confidence"]) if isinstance(hy.get("confidence"), int | float) else None,
                        language=str(hy.get("detectedLanguage") or ""),
                        language_confidence=(
                            float(hy["languageConfidence"])
                            if isinstance(hy.get("languageConfidence"), int | float)
                            else None
                        ),
                    )
                )
            v = decide(cands, str(row_in.get("language") or default_language), lid.get(h), p)
            verdicts[h] = v
            if v.origin == ORIGIN_DISPUTED:
                counts["disputed"] += 1
                reasons[v.reason] = reasons.get(v.reason, 0) + 1
            else:
                counts["labelled"] += 1
            out_rows.append(labelled(row_in, v))
            hyp: dict[str, Any] = {
                "audio": h,
                "text": v.text,
                "origin": v.origin,
                "confidence": v.confidence,
                "members": [candidate_json(c) for c in v.candidates],
            }
            if v.pick:
                hyp["pick"] = v.pick
            if v.reason:
                hyp["reason"] = v.reason
            hyp_rows.append(hyp)
            if i % 200 == 0:
                ctx.progress(i / max(len(rows), 1), f"{i}/{len(rows)} segments")

        out_header = {
            **seg.with_step(header, KIND),
            "producer": KIND,
            "members": sorted(labels),
            "normalizer": hash_file(inputs["normalizer"]),
            "maxPairwiseWer": p.max_pairwise_wer,
        }
        seg.write(outputs["segments"], out_header, out_rows, files_from=inputs["segments"])
        write_jsonl(outputs["hypotheses"], hyp_rows)
        considered = counts["labelled"] + counts["disputed"]
        meta = {
            "format": "cadence.segments/1",
            "segments": len(out_rows),
            "pseudoLabelled": counts["labelled"],
            "disputed": counts["disputed"],
            "passedThrough": counts["passed"],
            "reasons": reasons,
            "members": sorted(labels),
        }
        ctx.set_meta("segments", meta)
        ctx.set_meta("hypotheses", {"producer": KIND, "utterances": len(hyp_rows), "members": sorted(labels)})
        if considered:
            ctx.final_metric("disputed_share", counts["disputed"] / considered)
        ctx.log(
            "pseudo-labelled",
            labelled=counts["labelled"],
            disputed=counts["disputed"],
            passed=counts["passed"],
            reasons=reasons,
        )
