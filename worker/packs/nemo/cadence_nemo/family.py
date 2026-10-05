"""The Nemotron 3.5 streaming family (R41, R43): cache-aware FastConformer RNNT with a language prompt, published by
workers of runtime `nemo-speech` (entry point ``cadence.families``) under the name the seeded base model references
(``familyId`` in control-plane/internal/registry/fixtures/base-models.yaml).

Latency profiles follow docs/spec/03-pipelines-defaults.md: ``att_context_size`` ``[56, r]`` gives 80 * (r + 1) ms of
algorithmic latency (8x subsampling of 10 ms frames: one encoder frame is 80 ms) with 56 frames (4.48 s) of left
context.
"""

from __future__ import annotations

from pathlib import Path

from cadence_worker.protocol_gen import LatencyProfile
from cadence_worker.registry import Family

RUNTIME = "nemo-speech"
NAME = "nemo.fastconformer-rnnt.cache-aware"
SAMPLE_RATE = 16000
ENCODER_FRAME_MS = 80
LEFT_CONTEXT_FRAMES = 56


def _profile(right: int) -> LatencyProfile:
    ms = ENCODER_FRAME_MS * (right + 1)
    return {
        "name": f"{ms}ms",
        "latencyMs": ms,
        "chunkMs": ms,
        "leftContextMs": ENCODER_FRAME_MS * LEFT_CONTEXT_FRAMES,
        "params": {"att_context_size": [LEFT_CONTEXT_FRAMES, right]},
        "label": f"{ms} ms · [{LEFT_CONTEXT_FRAMES},{right}]",
    }


PROFILES: list[LatencyProfile] = [_profile(r) for r in (0, 1, 3, 6, 13)]


def profile(name: str) -> LatencyProfile:
    for p in PROFILES:
        if p["name"] == name:
            return p
    raise KeyError(name)


def att_context_size(p: LatencyProfile) -> list[int]:
    params = p.get("params") or {}
    left, right = params["att_context_size"]
    return [int(left), int(right)]


FAMILY = Family(
    runtime=RUNTIME,
    descriptor={
        "name": NAME,
        "version": "1",
        "title": "Nemotron 3.5 streaming (cache-aware FastConformer RNNT, NeMo)",
        "framework": "nemo",
        "architecture": "cache-aware FastConformer encoder (8x subsampling) + RNNT decoder and joint, language prompt "
        "concatenated to the encoder output; 0.6 B parameters for nemotron-3.5-asr-streaming-0.6b",
        "formats": [".nemo"],
        "input": {"sampleRate": SAMPLE_RATE, "channels": 1},
        "features": {
            "computedBy": "model",
            "kind": "log-mel filterbank computed by the preprocessor inside the .nemo",
            "stored": False,
        },
        "tokenizer": "sentencepiece",
        "capabilities": {
            "streaming": True,
            "wordTimestamps": True,
            "confidence": True,
            "boosting": "nemo-phrase-boosting",
            "languagePrompt": True,
            "trainModes": ["finetune"],
        },
        "latencyProfiles": PROFILES,
        "roles": {
            "calibrate": "oomptimizer_calibrate",
            "train": "nemotron_finetune",
            "average": "checkpoint_average",
            "transcribe": "nemotron_transcribe",
            "materialize": "checkpoint_from_base",
            "live": "nemotron_live",
            # Streams to a deployable on a staging server (phase 5, 06 "Staging serving"): batch for parity,
            # benchmarks and shadow replay; relay for a transcription session whose targets are deployments.
            "serve": "nemotron_serve",
        },
        # A live session's card memory (R49, spike A5 finding 6): one model loaded is 3.7 GB steady with a 5.6 GB
        # load peak (the state dict and the model on the card together), plus margin; every further distinct model
        # adds its fp32 weights; targets of one model share them.
        "interactive": {"memoryMb": 6000, "extraCheckpointMb": 2600},
        "defaultsSection": "packs.nemo",
        "help": "guides.nemo-pack",
        "skill": "cadence-train",
    },
    fixtures=Path(__file__).resolve().parent / "fixtures",
    conformance={
        # The conformance flow on the fixtures (ten short FLEURS he_il clips): a handful of optimiser steps each.
        "base_model": {
            "hfRepo": "nvidia/nemotron-3.5-asr-streaming-0.6b",
            "revision": "ea30d66debe3740a08b573244286791d423d6b3e",
            "checkpointFile": "nemotron-3.5-asr-streaming-0.6b.nemo",
            "familyId": NAME,
        },
        "import": {
            "source_name": "fleurs-he-fixtures",
            "source_kind": "public",
            "licence": "CC-BY-4.0",
            "locale": "he-IL",
        },
        "calibrate": {"bucket_bins": [4.0], "timed_steps": 3, "warmup_steps": 1, "start_batch_size": 4},
        "train": {"steps": 6, "val_every": 3, "log_every": 1},
        "stop": {"steps": 1000, "val_every": 1000, "log_every": 1},
        "resume": {"steps": 9, "val_every": 3, "log_every": 1},
        "transcribe": {"batch_size": 8},
        # A sanity bound only (the suite's WER compares lower-cased references with punctuated hypotheses): the run on
        # the card measured 0.56-0.74. No baseline stage: nine steps on eight test clips need not beat the base model,
        # and a second training run costs another 10 GB of store per nightly run.
        "score": {"maxWer": 0.9},
    },
)
