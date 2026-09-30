"""The language prompt of the Nemotron 3.5 streaming family.

The model conditions on a prompt index from ``cfg.model_defaults.prompt_dictionary`` (keys such as ``he-IL``, plus
``auto``). A clip's prompt key comes from its dataset ``language`` (or the step's ``target_lang``), matched exactly,
then case-insensitively, then by primary language subtag (``he`` → ``he-IL``). Training text ends with the locale tag
(`` <he-IL>``) when the tokenizer has it as a single piece: the base model emits the tag after the terminal punctuation,
so targets keep the pretraining format and decoding strips it (spike A3; docs/spikes/A3-nemotron-finetune.md).
"""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping

from cadence_worker.steps.base import StepInputError

AUTO = "auto"
TAG = re.compile(r"\s*<(?:[a-z]{2,3}-[A-Z]{2}|auto)>\s*")


def resolve_prompt_key(language: str, prompt_dict: Mapping[str, int]) -> str:
    """The prompt dictionary key for a language (BCP 47 or a bare primary subtag); StepInputError when none fits."""
    lang = (language or "").strip().replace("_", "-")
    if not lang or lang == "und":
        raise StepInputError(
            "the clip has no language (dataset language 'und' or empty); set target_lang or import with a locale"
        )
    if lang in prompt_dict:
        return lang
    lower = {k.lower(): k for k in prompt_dict}
    if lang.lower() in lower:
        return lower[lang.lower()]
    primary = lang.split("-")[0].lower()
    candidates = sorted(k for k in prompt_dict if k.lower().split("-")[0] == primary and k != AUTO)
    if candidates:
        return candidates[0]
    sample = ", ".join(sorted(k for k in prompt_dict if k != AUTO)[:12])
    raise StepInputError(f"the model has no language prompt for {language!r} (it knows {sample}, …)")


def prompt_keys(languages: Iterable[str], prompt_dict: Mapping[str, int], override: str = "") -> dict[str, str]:
    """Dataset language → prompt key for every language in the data (the override, when set, for all of them)."""
    out: dict[str, str] = {}
    for lang in sorted(set(languages)):
        out[lang] = resolve_prompt_key(override or lang, prompt_dict)
    return out


def with_tag(text: str, key: str) -> str:
    """Training text with the locale tag appended (once)."""
    t = strip_tags(text)
    return f"{t} <{key}>" if t else f"<{key}>"


def strip_tags(text: str) -> str:
    return " ".join(TAG.sub(" ", text).split())
