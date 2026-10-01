"""Validation WER against the raw reference text.

NeMo's WER metric decodes each reference from its token ids, so a character outside the tokenizer's vocabulary comes
back as ``⁇`` and the reference is not what the speaker said. Here the decoded reference is looked up in the
validation manifest's own texts (by the same round trip through the tokenizer), and hypothesis and reference are
both normalised as evaluation transcripts are (NFKC, case-folded, no punctuation, single spaces) before the word
edit distance. A reference the lookup misses keeps the decoded text, as before.

The pieces are plain functions over strings; :func:`install` patches one NeMo ``WER`` metric instance (it needs torch,
not NeMo), so everything is tested without a card.
"""

from __future__ import annotations

import types
from collections.abc import Callable, Iterable
from typing import Any

from cadence_worker.scoring import edit_distance
from cadence_worker.steps.dataset_import import normalise_text


def normalise(text: str) -> str:
    """The evaluation normalisation: NFKC, case-folded, punctuation removed, single spaces."""
    return normalise_text(text, True)


class RawReferences:
    """The manifest texts by what NeMo's reference decoding makes of them."""

    def __init__(self, texts: Iterable[str], round_trip: Callable[[str], str]) -> None:
        self._by_decoded: dict[str, str] = {}
        for t in texts:
            self._by_decoded.setdefault(" ".join(round_trip(t).split()), t)

    def __len__(self) -> int:
        return len(self._by_decoded)

    def reference(self, decoded: str) -> str:
        return self._by_decoded.get(" ".join(decoded.split()), decoded)


def counts(hypothesis: str, reference: str, use_cer: bool = False) -> tuple[int, int]:
    """Edits and reference length (words, or characters for CER) after normalising both sides."""
    h, r = normalise(hypothesis), normalise(reference)
    hs, rs = (list(h), list(r)) if use_cer else (h.split(), r.split())
    return edit_distance(rs, hs), len(rs)


def install(metric: Any, refs: RawReferences) -> None:
    """Make one NeMo ``WER`` metric score its hypotheses against raw, normalised references. The replacement keeps
    torchmetrics' update bookkeeping (``_wrap_update``) and the metric's own hypothesis decoding."""
    import torch

    def update(
        self: Any,
        predictions: Any,
        predictions_lengths: Any,
        targets: Any,
        targets_lengths: Any,
        predictions_mask: Any = None,
        input_ids: Any = None,
        **kwargs: Any,
    ) -> None:
        with torch.no_grad():
            lengths = targets_lengths.long().cpu()
            tokens = targets.long().cpu()
            if getattr(self, "batch_dim_index", 0) != 0:
                tokens = tokens.movedim(self.batch_dim_index, 0)
            references = [
                refs.reference(self.decoding.decode_ids_to_str(tokens[i][: int(lengths[i])].tolist()))
                for i in range(tokens.shape[0])
            ]
            hypotheses = (
                self.decode(predictions, predictions_lengths, predictions_mask, input_ids)
                if predictions.numel() > 0
                else []
            )
        edits = words = 0
        for h, r in zip(hypotheses, references, strict=False):
            if isinstance(h, list):
                h = h[0]
            e, n = counts(str(getattr(h, "text", h)), r, bool(getattr(self, "use_cer", False)))
            edits, words = edits + e, words + n
        self.scores = torch.tensor(edits, device=self.scores.device, dtype=self.scores.dtype)
        self.words = torch.tensor(words, device=self.words.device, dtype=self.words.dtype)
        self.hypotheses = hypotheses

    bound = types.MethodType(update, metric)
    wrap = getattr(metric, "_wrap_update", None)
    metric.update = wrap(bound) if callable(wrap) else bound
