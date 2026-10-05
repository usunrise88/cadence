"""``parity_score@1`` — the runtime-neutral parity judge of phase 5 (R31; docs/spec/03-pipelines-defaults.md "Export,
parity and benchmark (phase 5)"; spike E1, docs/spikes/E1-onnx-triton.md).

Consumes two ``hypotheses`` of the same sample — ``reference`` (the family's own decoder, its parity-reference role)
and ``served`` (the family's serve role through the staging server) — the sample ``dataset`` and the golden set's
``normalizer``; optionally the reference's ``smoke`` inputs (``smoke_inputs``, ``cadence.smoke-inputs/1``: what the
family's smoke client sends for the first utterances). Produces ``report``, a ``parity_report`` directory:

    report.json   {schema: cadence.parity/1, scorer, normalizer, utterances, compared: tokens|text, identical,
                  identicalShare, reference: {wer, words, refWords}, served: {wer, words}, werDelta, disagreement,
                  thresholds: {maxWerDelta, minIdenticalShare, maxDisagreement}, verdict: passed|failed, reasons,
                  differing: [{audio, reference, served, referenceTokens?, servedTokens?}],
                  smoke: {format, items: [{name, audio, file, expected}]}}
    smoke/<name>  the smoke inputs, copied as they are (the delivery bundle's smoke set, with ``expected``)

Token sequences are compared when every row of both sides carries ``tokens`` (the final's token ids), otherwise the
NFC-normalised texts, and the report says which. ``disagreement`` scores the served transcripts with the reference's
as the reference (word errors per reference-decode word, after the normalizer): fp32 exports measured 0.0005-0.0033
in E1, fp16 0.017. A smoke item's ``expected`` is what the staging server returned for it — its token ids joined by
spaces when the served side carries them (what the family's smoke client prints), else its NFC text. Help:
docs/help/steps/parity-score.md.
"""

from __future__ import annotations

import json
import shutil
import unicodedata
from collections.abc import Mapping, Sequence
from pathlib import Path, PurePosixPath
from typing import Any, ClassVar

from pydantic import BaseModel

from cadence_worker.align import align
from cadence_worker.cas import hash_file
from cadence_worker.normalize import Normalizer, NormalizerError
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import Reference, read_hypotheses, read_references, score

SCHEMA = "cadence.parity/1"
SCORER = "parity_score@1"
SMOKE_SCHEMA = "cadence.smoke-inputs/1"
MAX_SMOKE = 20  # the delivery bundle's smoke set (docs/spec/02 "Delivery bundle": ≤ 20 utterances)
MAX_DIFFERING = 200  # differing utterances listed in the report
EPS = 1e-12


class ParityScoreParams(BaseModel):
    max_wer_delta: float = cadence_field(default_ref="deploy.parity_max_wer_delta")
    min_identical_share: float = cadence_field(default_ref="deploy.parity_min_identical_share")
    max_disagreement: float = cadence_field(default_ref="deploy.parity_max_disagreement")
    smoke_utterances: int = cadence_field(
        MAX_SMOKE,
        description="Smoke inputs the report carries for the delivery bundle (the first of the reference's smoke set "
        "whose served output exists)",
        source='docs/spec/02-domain-projects-registry.md "Delivery bundle" (≤ 20 utterances of the parity sample)',
        range={"min": 0, "max": MAX_SMOKE},
    )


def nfc(text: str) -> str:
    return " ".join(unicodedata.normalize("NFC", text).split())


def tokens_of(row: Mapping[str, Any]) -> list[int] | None:
    t = row.get("tokens")
    if isinstance(t, list) and all(isinstance(v, int) and not isinstance(v, bool) for v in t):
        return [int(v) for v in t]
    return None


def compare(
    refs: Sequence[Reference],
    reference: Mapping[str, Mapping[str, Any]],
    served: Mapping[str, Mapping[str, Any]],
    norm: Normalizer,
) -> dict[str, Any]:
    """Identical sequences, word disagreement and the differing utterances of two decodes of the same utterances."""
    by_tokens = all(tokens_of(reference[r.audio]) is not None and tokens_of(served[r.audio]) is not None for r in refs)
    identical = 0
    errors = words = 0
    differing: list[dict[str, Any]] = []
    for r in refs:
        a, b = reference[r.audio], served[r.audio]
        ta, tb = str(a.get("text") or ""), str(b.get("text") or "")
        same = tokens_of(a) == tokens_of(b) if by_tokens else nfc(ta) == nfc(tb)
        wa, wb = norm(ta).split(), norm(tb).split()
        errors += align(wa, wb).errors
        words += len(wa)
        if same:
            identical += 1
        elif len(differing) < MAX_DIFFERING:
            d: dict[str, Any] = {"audio": r.audio, "reference": ta, "served": tb}
            if by_tokens:
                d["referenceTokens"], d["servedTokens"] = tokens_of(a), tokens_of(b)
            differing.append(d)
    n = len(refs)
    return {
        "compared": "tokens" if by_tokens else "text",
        "identical": identical,
        "identicalShare": identical / n if n else 0.0,
        "disagreement": errors / words if words else float(errors > 0),
        "differing": differing,
    }


def verdict(
    wer_delta: float, identical_share: float, disagreement: float, p: ParityScoreParams
) -> tuple[str, list[str]]:
    reasons: list[str] = []
    if abs(wer_delta) > p.max_wer_delta + EPS:
        reasons.append(f"|WER difference| {abs(wer_delta):.4f} is above {p.max_wer_delta:g}")
    if identical_share < p.min_identical_share - EPS:
        reasons.append(f"identical share {identical_share:.4f} is below {p.min_identical_share:g}")
    if disagreement > p.max_disagreement + EPS:
        reasons.append(f"word disagreement {disagreement:.4f} is above {p.max_disagreement:g}")
    return ("failed" if reasons else "passed"), reasons


def expected_of(row: Mapping[str, Any]) -> str:
    """What the family's smoke client must print for an utterance: the served token ids, else the served text."""
    t = tokens_of(row)
    if t is not None:
        return " ".join(str(v) for v in t)
    return nfc(str(row.get("text") or ""))


def read_smoke(root: Path) -> tuple[str, list[tuple[str, Path]]]:
    """The smoke inputs: their format and (audio hash, file) per item in order."""
    try:
        doc = json.loads((root / "smoke.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as e:
        raise StepInputError(f"the smoke input has no readable smoke.json: {e}") from e
    if not isinstance(doc, dict) or doc.get("schema") != SMOKE_SCHEMA:
        raise StepInputError(f"smoke.json is not {SMOKE_SCHEMA}")
    out: list[tuple[str, Path]] = []
    for i, it in enumerate(doc.get("items") or []):
        rel = it.get("file") if isinstance(it, dict) else None
        audio = it.get("audio") if isinstance(it, dict) else None
        if not isinstance(rel, str) or not isinstance(audio, str):
            raise StepInputError(f"smoke.json item {i} lacks audio or file")
        p = PurePosixPath(rel)
        if len(p.parts) != 1 or p.name in ("", ".", "..", "smoke.json"):
            raise StepInputError(f"smoke.json item {i}: {rel!r} is not a file name inside the artifact")
        f = root / p.name
        if not f.is_file():
            raise StepInputError(f"smoke.json item {i}: {rel} is not in the artifact")
        out.append((audio, f))
    return str(doc.get("format") or ""), out


class ParityScoreStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {
        "reference": "hypotheses",
        "served": "hypotheses",
        "data": "dataset",
        "normalizer": "normalizer",
        "smoke": "smoke_inputs",
    }
    optional_inputs: ClassVar[frozenset[str]] = frozenset({"smoke"})
    produces: ClassVar[Mapping[str, str]] = {"report": "parity_report"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = ParityScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        p = ParityScoreParams.model_validate(params.model_dump())
        for name in ("reference", "served", "data", "normalizer"):
            if name not in inputs:
                raise StepInputError(f"parity_score needs its {name} input")
        try:
            norm = Normalizer.from_file(inputs["normalizer"])
        except NormalizerError as e:
            raise StepInputError(str(e)) from e
        refs = read_references(inputs["data"])
        reference = read_hypotheses(inputs["reference"])
        served = read_hypotheses(inputs["served"])
        for side, hyps in (("reference", reference), ("served", served)):
            if missing := [r.audio for r in refs if r.audio not in hyps]:
                raise StepInputError(
                    f"the {side} decode lacks {len(missing)} of {len(refs)} utterances (first: {missing[0]})"
                )
        ref_summary, _, _ = score(refs, reference, norm, [])
        srv_summary, _, _ = score(refs, served, norm, [])
        cmp = compare(refs, reference, served, norm)
        wer_delta = float(srv_summary["wer"]) - float(ref_summary["wer"])
        result, reasons = verdict(wer_delta, cmp["identicalShare"], cmp["disagreement"], p)
        out = outputs["report"]
        out.mkdir(parents=True, exist_ok=True)
        smoke_items: list[dict[str, Any]] = []
        smoke_format = ""
        if "smoke" in inputs and p.smoke_utterances > 0:
            smoke_format, items = read_smoke(inputs["smoke"])
            (out / "smoke").mkdir(exist_ok=True)
            for audio, f in items:
                if len(smoke_items) == p.smoke_utterances:
                    break
                if audio not in served:
                    continue
                shutil.copyfile(f, out / "smoke" / f.name)
                smoke_items.append(
                    {"name": f.name, "audio": audio, "file": f"smoke/{f.name}", "expected": expected_of(served[audio])}
                )
        report: dict[str, Any] = {
            "schema": SCHEMA,
            "scorer": SCORER,
            "normalizer": {"versionId": norm.payload.versionId, "hash": hash_file(inputs["normalizer"])},
            "utterances": len(refs),
            "compared": cmp["compared"],
            "identical": cmp["identical"],
            "identicalShare": cmp["identicalShare"],
            "reference": {
                "wer": ref_summary["wer"],
                "refWords": ref_summary["refWords"],
                "hypotheses": ref_summary["hypotheses"],
            },
            "served": {
                "wer": srv_summary["wer"],
                "refWords": srv_summary["refWords"],
                "hypotheses": srv_summary["hypotheses"],
            },
            "werDelta": wer_delta,
            "disagreement": cmp["disagreement"],
            "thresholds": {
                "maxWerDelta": p.max_wer_delta,
                "minIdenticalShare": p.min_identical_share,
                "maxDisagreement": p.max_disagreement,
            },
            "verdict": result,
            "reasons": reasons,
            "differing": cmp["differing"],
            "smoke": {"format": smoke_format, "items": smoke_items},
        }
        (out / "report.json").write_text(
            json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        for name, value in (
            ("wer_delta", wer_delta),
            ("identical_share", cmp["identicalShare"]),
            ("disagreement", cmp["disagreement"]),
        ):
            ctx.final_metric(name, value)
        ctx.set_meta(
            "report",
            {
                "schema": SCHEMA,
                "verdict": result,
                "werDelta": wer_delta,
                "identicalShare": cmp["identicalShare"],
                "disagreement": cmp["disagreement"],
                "compared": cmp["compared"],
                "utterances": len(refs),
                "smoke": len(smoke_items),
            },
        )
        msg = f"parity {result}: ΔWER {wer_delta * 100:+.3f} points, {cmp['identical']}/{len(refs)} identical"
        ctx.progress(1.0, msg + (f" ({'; '.join(reasons)})" if reasons else ""))
