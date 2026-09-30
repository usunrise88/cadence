"""The training dataset of the prompt model with the augmentation profile applied per clip after loading (runs in the
dataloader workers). Imports NeMo at module level: only the image imports this module."""

from __future__ import annotations

import os
from typing import Any

import numpy as np
import torch
from nemo.collections.asr.data.audio_to_text_lhotse_prompt_index import LhotseSpeechToTextBpeDatasetWithPromptIndex

from cadence_nemo.augment import Profile, apply


class AugmentingDataset(LhotseSpeechToTextBpeDatasetWithPromptIndex):  # type: ignore[misc]
    """``LhotseSpeechToTextBpeDatasetWithPromptIndex`` + the profile: (audio, lengths, tokens, token lengths, prompt
    indices) with each clip's audio passed through :func:`cadence_nemo.augment.apply`. The generator is seeded per
    worker process from torch's worker seed (Lightning's ``seed_everything(workers=True)`` makes those distinct and
    reproducible for a given run seed)."""

    def __init__(self, tokenizer: Any, cfg: dict[str, Any], profile: Profile, sample_rate: int) -> None:
        super().__init__(tokenizer=tokenizer, cfg=cfg)
        self.profile = profile
        self.sample_rate = sample_rate
        self._rng: np.random.Generator | None = None
        self._pid = -1

    def rng(self) -> np.random.Generator:
        if self._rng is None or self._pid != os.getpid():
            self._pid = os.getpid()
            self._rng = np.random.default_rng(torch.initial_seed() % (2**32))
        return self._rng

    def __getitem__(self, cuts: Any) -> tuple[torch.Tensor, ...]:
        audio, lens, tokens, token_lens, prompts = super().__getitem__(cuts)
        if not self.profile.enabled:
            return audio, lens, tokens, token_lens, prompts
        rng = self.rng()
        clips = []
        for i in range(audio.shape[0]):
            x = audio[i, : int(lens[i])].numpy().astype(np.float32)
            y, _ = apply(x, self.sample_rate, self.profile, rng)
            clips.append(torch.from_numpy(np.ascontiguousarray(y)))
        new_lens = torch.tensor([c.numel() for c in clips], dtype=lens.dtype)
        padded = torch.nn.utils.rnn.pad_sequence(clips, batch_first=True).to(audio.dtype)
        return padded, new_lens, tokens, token_lens, prompts
