"""``entity_score@1`` — entity accuracy for number classes (docs/spec/04-blocks.md "Task and streaming metrics";
docs/spec/03-pipelines-defaults.md "Scorers and metrics"; phase 3 stream R): numbers, dates, phone numbers, amounts —
the classes of the locale's language pack ``itn.yaml`` — compared between the reference and the hypothesis.

Consumes ``hypotheses``, the ``dataset`` they decode and ``itn`` (the pack's ``lang/<locale>/itn.yaml`` at the pack's
commit, rendered by the control plane as JSON; the YAML file itself is read too). Produces ``scores``, a
``metric_scores`` artifact (:mod:`cadence_worker.metric_scores`) with metric ``entities``:

    summary   utterances, utterancesWithEntities, refEntities, hypEntities, correct, accuracy (correct / refEntities),
              precision (correct / hypEntities), classes: [{class, refEntities, hypEntities, correct, accuracy,
              precision}], itn: {locale, commit}
    rows      {index, audio, ref: [{class, text, found}], extra: [{class, text}]} for utterances with an entity

How: both texts first go through the pack's examples as spoken → written replacements (whole words, case-insensitive,
longest spoken form first) — the examples-based conversion the pack offers until the ITN step (phase 4) converts
spoken numbers in general. Each class's ``pattern`` then finds its written forms; where matches of several classes
overlap, the longest wins (then the class listed first). A reference entity is found when the hypothesis has the same
class with the same text (whitespace ignored), counted as multisets per utterance. Names and addresses need annotated
spans: a reference annotated in a batch (phase 4) carries them, and each is found when the hypothesis holds its text.
Reported, not gated. Help: docs/help/steps/entity-score.md.
"""

from __future__ import annotations

import re
from collections import Counter
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any, ClassVar

import yaml
from pydantic import BaseModel

from cadence_worker import metric_scores
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError
from cadence_worker.steps.context import StepContext
from cadence_worker.steps.wer_score import read_hypotheses, read_references

SCORER = "entity_score@1"
METRIC = "entities"


class EntityScoreParams(BaseModel):
    """No parameters: the language pack's ITN file names the classes."""


@dataclass(frozen=True)
class EntityClass:
    name: str
    pattern: re.Pattern[str]


@dataclass(frozen=True)
class ITN:
    locale: str
    commit: str
    classes: tuple[EntityClass, ...]
    examples: tuple[tuple[re.Pattern[str], str], ...]  # spoken (whole words) → written, longest spoken first


def read_itn(path: Path) -> ITN:
    try:
        doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as e:
        raise StepInputError(f"cannot read the itn input: {e}") from e
    if not isinstance(doc, dict) or not isinstance(doc.get("classes"), list):
        raise StepInputError("the itn input has no classes (lang/<locale>/itn.yaml)")
    classes: list[EntityClass] = []
    examples: list[tuple[str, str]] = []
    for n, c in enumerate(doc["classes"]):
        if not isinstance(c, dict) or not isinstance(c.get("name"), str) or not isinstance(c.get("pattern"), str):
            raise StepInputError(f"itn class {n} lacks a name or a pattern")
        try:
            classes.append(EntityClass(c["name"], re.compile(c["pattern"])))
        except re.error as e:
            raise StepInputError(f"itn class {c['name']}: the pattern does not compile: {e}") from e
        for ex in c.get("examples") or []:
            spoken, written = (ex.get("spoken"), ex.get("written")) if isinstance(ex, dict) else (None, None)
            if isinstance(spoken, str) and isinstance(written, str) and spoken.strip():
                examples.append((spoken.strip(), written))
    if not classes:
        raise StepInputError("the itn input has no classes")
    examples.sort(key=lambda e: -len(e[0]))
    compiled = tuple(
        (re.compile(r"(?<!\w)" + re.escape(spoken) + r"(?!\w)", re.IGNORECASE), written) for spoken, written in examples
    )
    return ITN(str(doc.get("locale") or ""), str(doc.get("commit") or ""), tuple(classes), compiled)


def convert(text: str, itn: ITN) -> str:
    """The pack's examples applied as spoken → written replacements."""
    for pat, written in itn.examples:
        text = pat.sub(written.replace("\\", "\\\\"), text)  # a literal replacement (backslashes escaped)
    return text


def entities(text: str, itn: ITN) -> list[tuple[str, str]]:
    """(class, text) of the written forms in ``text``, overlaps resolved longest first, then by class order."""
    found: list[tuple[int, int, int, str]] = []
    for ci, c in enumerate(itn.classes):
        for m in c.pattern.finditer(text):
            if m.end() > m.start():
                found.append((m.start(), m.end(), ci, m.group(0)))
    found.sort(key=lambda f: (-(f[1] - f[0]), f[2], f[0]))
    taken: list[tuple[int, int]] = []
    keep: list[tuple[int, int, str]] = []
    for start, end, ci, s in found:
        if any(start < e and s_ < end for s_, e in taken):
            continue
        taken.append((start, end))
        keep.append((start, ci, s))
    keep.sort()
    return [(itn.classes[ci].name, "".join(s.split())) for _, ci, s in keep]


def _ratio(a: int, b: int) -> float | None:
    return a / b if b else None


def score(
    refs: Sequence[Any], hyps: Mapping[str, Mapping[str, Any]], itn: ITN
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    missing = [r.audio for r in refs if r.audio not in hyps]
    if missing:
        raise StepInputError(f"{len(missing)} of {len(refs)} utterances have no hypothesis (first: {missing[0]})")
    names = [c.name for c in itn.classes]
    per = {n: [0, 0, 0] for n in names}  # ref, hyp, correct
    rows: list[dict[str, Any]] = []
    with_entities = 0
    for i, r in enumerate(refs):
        ref_e = entities(convert(r.text, itn), itn)
        hyp_text = str(hyps[r.audio]["text"])
        hyp_e = entities(convert(hyp_text, itn), itn)
        # Annotated spans (names, addresses — classes no pattern finds) are found when the hypothesis holds their
        # text, case and spacing aside; a found one counts as a hypothesis entity too (precision is 1 for them).
        spans = [
            (c, t) for c, t in getattr(r, "entities", ()) if c not in names or (c, "".join(t.split())) not in ref_e
        ]
        if not ref_e and not hyp_e and not spans:
            continue
        if ref_e or spans:
            with_entities += 1
        pool = Counter(hyp_e)
        ref_rows = []
        for e in ref_e:
            ok = pool[e] > 0
            if ok:
                pool[e] -= 1
                per[e[0]][2] += 1
            per[e[0]][0] += 1
            ref_rows.append({"class": e[0], "text": e[1], "found": ok})
        folded_hyp = " ".join(hyp_text.casefold().split())
        for cls, text in spans:
            if cls not in per:
                per[cls] = [0, 0, 0]
                names.append(cls)
            ok = " ".join(text.casefold().split()) in folded_hyp
            per[cls][0] += 1
            if ok:
                per[cls][1] += 1
                per[cls][2] += 1
            ref_rows.append({"class": cls, "text": text, "found": ok, "annotated": True})
        for e in hyp_e:
            per[e[0]][1] += 1
        extra = [{"class": c, "text": t} for (c, t), n in pool.items() for _ in range(n)]
        rows.append({"index": i, "audio": r.audio, "ref": ref_rows, "extra": extra})
    ref_n = sum(v[0] for v in per.values())
    hyp_n = sum(v[1] for v in per.values())
    ok_n = sum(v[2] for v in per.values())
    summary: dict[str, Any] = {
        "schema": metric_scores.SCHEMA,
        "scorer": SCORER,
        "metric": METRIC,
        "available": ref_n > 0,
        "itn": {"locale": itn.locale, "commit": itn.commit},
        "utterances": len(refs),
        "utterancesWithEntities": with_entities,
        "refEntities": ref_n,
        "hypEntities": hyp_n,
        "correct": ok_n,
        "accuracy": _ratio(ok_n, ref_n),
        "precision": _ratio(ok_n, hyp_n),
        "classes": [
            {
                "class": n,
                "refEntities": per[n][0],
                "hypEntities": per[n][1],
                "correct": per[n][2],
                "accuracy": _ratio(per[n][2], per[n][0]),
                "precision": _ratio(per[n][2], per[n][1]),
            }
            for n in names
        ],
    }
    if ref_n == 0:
        summary["reason"] = "no reference of the golden set has a written form of the pack's ITN classes"
    return summary, rows


class EntityScoreStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"hypotheses": "hypotheses", "data": "dataset", "itn": "itn"}
    produces: ClassVar[Mapping[str, str]] = {"scores": "metric_scores"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "eval"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = EntityScoreParams

    def run(self, params: BaseModel, inputs: Mapping[str, Path], outputs: Mapping[str, Path], ctx: StepContext) -> None:
        for name in self.consumes:
            if name not in inputs:
                raise StepInputError(f"entity_score needs its {name} input")
        itn = read_itn(inputs["itn"])
        refs = read_references(inputs["data"])
        hyps = read_hypotheses(inputs["hypotheses"])
        summary, rows = score(refs, hyps, itn)
        metric_scores.write(outputs["scores"], summary, rows)
        if summary["accuracy"] is not None:
            ctx.final_metric("entity_accuracy", summary["accuracy"])
        ctx.set_meta(
            "scores",
            {k: summary[k] for k in ("schema", "scorer", "metric", "available", "refEntities", "correct", "accuracy")},
        )
        acc = summary["accuracy"]
        ctx.progress(
            1.0,
            f"entity accuracy {acc:.4f} over {summary['refEntities']} entities"
            if acc is not None
            else "no entities in the references",
        )
