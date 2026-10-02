"""The scoring normalizer interpreter (docs/spec/08-resolutions.md R21; docs/review/2026-10-02-phase-3-plan.md
"Registry payloads"): applies a ``NormalizerPayload`` — the registry kind ``normalizer``, rendered by the control plane
into a ``normalizer`` artifact (the payload as JSON) — to the text both sides of a WER are compared after.

The steps run in this fixed order (any other implementation of the payload must follow it):

1. ``unicode``: Unicode normalisation form NFC or NFKC;
2. ``mappings``: literal replacements, in list order, each over the whole text;
3. ``removeMarks``: canonical decomposition (NFD), every nonspacing combining mark (category Mn: Hebrew niqqud and
   cantillation, Latin accents) dropped, then recomposed with the form of step 1;
4. ``casefold``: Unicode full case folding (``str.casefold``);
5. ``punctuation: strip``: every Unicode punctuation character (categories Pc, Pd, Ps, Pe, Pi, Pf, Po — the Hebrew
   maqaf, geresh and gershayim included) becomes a space;
6. whitespace: runs of whitespace collapse to one space, none at either end (always).

``numbers`` is ``keep`` (numbers compare as written); spoken/written conversion arrives with the pack's ITN in phase 4.
"""

from __future__ import annotations

import json
import unicodedata
from functools import cached_property
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, ValidationError


class TextMapping(BaseModel):
    model_config = ConfigDict(extra="forbid", populate_by_name=True)

    from_: str = Field(alias="from", min_length=1)
    to: str


class NormalizerPayload(BaseModel):
    """The contract's NormalizerPayload. The rendered artifact may carry the registry version beside it
    (``versionId``, ``collection``, ``version``); other keys are ignored."""

    model_config = ConfigDict(extra="ignore", frozen=True)

    locale: str
    unicode: Literal["NFC", "NFKC"]
    casefold: bool
    punctuation: Literal["keep", "strip"]
    removeMarks: bool  # noqa: N815 - the contract's spelling
    mappings: tuple[TextMapping, ...]
    numbers: Literal["keep"]
    versionId: str | None = None  # noqa: N815
    collection: str | None = None
    version: str | None = None


class NormalizerError(ValueError):
    pass


class Normalizer:
    def __init__(self, payload: NormalizerPayload) -> None:
        self.payload = payload

    @classmethod
    def from_json(cls, raw: bytes | str) -> Normalizer:
        try:
            return cls(NormalizerPayload.model_validate(json.loads(raw)))
        except (ValueError, ValidationError) as e:
            raise NormalizerError(f"not a normalizer payload: {e}") from e

    @classmethod
    def from_file(cls, path: Path) -> Normalizer:
        try:
            return cls.from_json(path.read_bytes())
        except OSError as e:
            raise NormalizerError(f"cannot read the normalizer: {e}") from e

    @cached_property
    def strips_punctuation(self) -> bool:
        return self.payload.punctuation == "strip"

    def without_punctuation(self) -> Normalizer:
        """The same normalizer with punctuation stripped (the ``werNoPunct`` companion score)."""
        if self.strips_punctuation:
            return self
        return Normalizer(self.payload.model_copy(update={"punctuation": "strip"}))

    def __call__(self, text: str) -> str:
        p = self.payload
        t = unicodedata.normalize(p.unicode, text)
        for m in p.mappings:
            t = t.replace(m.from_, m.to)
        if p.removeMarks:
            t = "".join(c for c in unicodedata.normalize("NFD", t) if unicodedata.category(c) != "Mn")
            t = unicodedata.normalize(p.unicode, t)
        if p.casefold:
            t = t.casefold()
        if p.punctuation == "strip":
            t = strip_punctuation(t)
        return " ".join(t.split())

    def words(self, text: str) -> list[str]:
        return self(text).split()


def strip_punctuation(text: str) -> str:
    """Every Unicode punctuation character (categories P*) as a space."""
    return "".join(" " if unicodedata.category(c).startswith("P") else c for c in text)
