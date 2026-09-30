"""Training helpers shared by the toy calibrate and train step kinds."""

from __future__ import annotations

import random

import torch

from cadence_toy.data import Utterance, batch
from cadence_toy.model import CharTokenizer, TinyCTC, decode_offline, features
from cadence_worker.scoring import wer

_ctc = torch.nn.CTCLoss(blank=0, zero_infinity=True)


def train_step(model: TinyCTC, opt: torch.optim.Optimizer, utts: list[Utterance], tok: CharTokenizer) -> float:
    model.train()
    x, x_len, y, y_len = batch(utts, tok, features)
    logp, _ = model(x)
    loss = _ctc(logp.transpose(0, 1), y, x_len, y_len)
    opt.zero_grad()
    loss.backward()
    torch.nn.utils.clip_grad_norm_(model.parameters(), 5.0)
    opt.step()
    return float(loss.item())


def evaluate(model: TinyCTC, tok: CharTokenizer, utts: list[Utterance]) -> float:
    model.eval()
    return wer((tok.normalise(u.text), decode_offline(model, tok, u.samples).text()) for u in utts)


def sampler(utts: list[Utterance], size: int, seed: int, step: int) -> list[Utterance]:
    """The batch of a given step: deterministic in (seed, step), so a resumed run draws what the uninterrupted one
    would have."""
    rng = random.Random(seed * 1_000_003 + step)
    return [utts[rng.randrange(len(utts))] for _ in range(size)]
