"""An auxiliary's weights from the Hugging Face Hub at its pinned revision: from ``HF_HOME`` or a read-only cache in
``CADENCE_HF_READONLY_CACHES`` (colon-separated hub directories, e.g. the stand's ``/stand/hf/hub`` for the nightly
run) when one holds them, else downloaded into ``HF_HOME`` once. huggingface_hub comes from the omni image."""

from __future__ import annotations

import os
from collections.abc import Sequence
from pathlib import Path

from cadence_worker.steps.base import StepInputError


def snapshot(repo: str, revision: str, allow: Sequence[str]) -> Path:
    from huggingface_hub import snapshot_download

    caches: list[str | None] = [None, *filter(None, os.environ.get("CADENCE_HF_READONLY_CACHES", "").split(":"))]
    for cache in caches:
        try:
            return Path(
                snapshot_download(
                    repo, revision=revision, cache_dir=cache, allow_patterns=list(allow), local_files_only=True
                )
            )
        except Exception:  # not in this cache (LocalEntryNotFoundError and friends): try the next
            continue
    try:
        return Path(snapshot_download(repo, revision=revision, allow_patterns=list(allow)))
    except Exception as e:  # network, auth or a missing revision: the step cannot run
        raise StepInputError(f"cannot fetch {repo}@{revision}: {e}") from e
