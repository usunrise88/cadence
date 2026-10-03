"""``text_normalise@1`` — bring segment transcripts into the training text style of the project's language pack.

The parameters mirror ``lang/<locale>/normalizer.yaml`` → ``training`` (unicode, removeMarks, casefold, punctuation,
mappings, transliterate) plus spoken → written rules in the spirit of ``itn.yaml``; a pipeline copies them from the
pack (the defaults keep the base model's style: cased and punctuated text, NFC). Steps run in this order:
transliterate, Unicode form, mappings, mark removal, case folding, punctuation, whitespace, then the ``itn`` rules
(whole words, case-insensitive, longest spoken form first). A changed text keeps its original as ``textOriginal``.
Help: docs/help/steps/text-normalise.md.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any, ClassVar, Literal

from pydantic import BaseModel

from cadence_worker import segments as seg
from cadence_worker.normalize import strip_punctuation
from cadence_worker.protocol_gen import StepResources
from cadence_worker.steps.base import StepInputError, cadence_field
from cadence_worker.translit import SCHEMES, transliterate

KIND = "text_normalise@1"


class TextNormaliseParams(BaseModel):
    unicode: Literal["NFC", "NFKC"] = cadence_field(
        "NFC",
        description="Unicode normalisation form (normalizer.yaml training.unicode)",
        source="templates/lang/*/normalizer.yaml (training.unicode: NFC)",
        range={"values": ["NFC", "NFKC"]},
    )
    remove_marks: bool = cadence_field(
        False,
        description="Drop combining marks (Hebrew niqqud, Latin accents) — off where marks are letters (č ć ž š đ)",
        source="templates/lang/*/normalizer.yaml (training.removeMarks)",
        range={"values": [True, False]},
    )
    casefold: bool = cadence_field(
        False,
        description="Fold case; off keeps the base model's cased style",
        source="nvidia/nemotron-3.5-asr-streaming-0.6b model card (cased output); normalizer.yaml training.casefold",
        range={"values": [True, False]},
    )
    punctuation: Literal["keep", "strip"] = cadence_field(
        "keep",
        description="keep the base model's punctuated style, or strip punctuation",
        source="nvidia/nemotron-3.5-asr-streaming-0.6b model card (punctuated output); normalizer.yaml",
        range={"values": ["keep", "strip"]},
    )
    mappings: list[dict[str, str]] = cadence_field(
        [],
        description="Literal replacements in order, each {from, to} over the whole text (normalizer.yaml mappings)",
        source="templates/lang/*/normalizer.yaml (training.mappings)",
        range={"maxLength": 1000},
    )
    transliterate: str = cadence_field(
        "",
        description="Script map applied first (translit.yaml), e.g. sr-Cyrl-Latn; empty keeps the script",
        source="templates/lang/sr/normalizer.yaml (training.transliterate)",
        range={"values": ["", *sorted(SCHEMES)]},
    )
    itn: list[dict[str, str]] = cadence_field(
        [],
        description="Spoken → written rules, each {spoken, written}: whole words, case-insensitive, longest first",
        source="templates/lang/*/itn.yaml (classes and examples)",
        range={"maxLength": 10000},
    )


class Itn:
    def __init__(self, rules: Sequence[Mapping[str, str]]) -> None:
        pairs: list[tuple[str, str]] = []
        for i, r in enumerate(rules):
            spoken, written = str(r.get("spoken", "")).strip(), r.get("written")
            if not spoken or written is None or set(r) - {"spoken", "written"}:
                raise StepInputError(f"itn[{i}] must be {{spoken, written}} with a non-empty spoken form")
            pairs.append((" ".join(spoken.split()), str(written)))
        pairs.sort(key=lambda x: (-len(x[0]), x[0]))
        self.table = {s.casefold(): w for s, w in pairs}
        self.pattern = (
            re.compile(r"(?<!\w)(" + "|".join(re.escape(s) for s, _ in pairs) + r")(?!\w)", re.IGNORECASE)
            if pairs
            else None
        )

    def __call__(self, text: str) -> str:
        if self.pattern is None:
            return text
        return self.pattern.sub(lambda m: self.table.get(m.group(0).casefold(), m.group(0)), text)


def normaliser(p: TextNormaliseParams) -> Any:
    for i, m in enumerate(p.mappings):
        if set(m) != {"from", "to"} or not m["from"]:
            raise StepInputError(f"mappings[{i}] must be {{from, to}} with a non-empty from")
    itn = Itn(p.itn)

    def apply(text: str) -> str:
        t = transliterate(text, p.transliterate)
        t = unicodedata.normalize(p.unicode, t)
        for m in p.mappings:
            t = t.replace(m["from"], m["to"])
        if p.remove_marks:
            t = "".join(c for c in unicodedata.normalize("NFD", t) if unicodedata.category(c) != "Mn")
            t = unicodedata.normalize(p.unicode, t)
        if p.casefold:
            t = t.casefold()
        if p.punctuation == "strip":
            t = strip_punctuation(t)
        t = " ".join(t.split())
        return " ".join(itn(t).split())

    return apply


class TextNormaliseStep:
    version: ClassVar[str] = "1"
    consumes: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    produces: ClassVar[Mapping[str, str]] = {"segments": "segments"}
    resources: ClassVar[StepResources] = {"gpu": False, "gpus": 0, "jobKind": "data"}
    neutral: ClassVar[bool] = True
    Params: ClassVar[type[BaseModel]] = TextNormaliseParams

    def run(
        self,
        params: BaseModel,
        inputs: Mapping[str, Path],
        outputs: Mapping[str, Path],
        ctx: Any = None,
    ) -> None:
        p = TextNormaliseParams.model_validate(params.model_dump())
        header, lines = seg.read(inputs["segments"])
        apply = normaliser(p)
        changed = 0
        for x in lines:
            text = x.get("text")
            if not isinstance(text, str):
                continue
            new = apply(text)
            if new != text:
                x.setdefault("textOriginal", text)
                x["text"] = new
                changed += 1
        seg.write(outputs["segments"], seg.with_step(header, KIND), lines, files_from=inputs["segments"])
        log = getattr(ctx, "log", None)
        if callable(log):
            log(f"{changed} of {len(lines)} transcripts changed")
