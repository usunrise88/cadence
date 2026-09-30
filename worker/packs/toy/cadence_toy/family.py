"""The toy-ctc model family (R41), published by workers of runtime `toy` (entry point ``cadence.families``)."""

from __future__ import annotations

from pathlib import Path

from cadence_toy.model import SAMPLE_RATE, feature_config
from cadence_worker.protocol_gen import LatencyProfile
from cadence_worker.registry import Family

RUNTIME = "toy"
NAME = "toy-ctc"

PROFILES: list[LatencyProfile] = [
    {"name": "offline", "latencyMs": 0, "label": "offline"},
    # Simulated streaming: 320 ms chunks with the GRU state carried over (the whole past is the left context).
    {"name": "320ms", "latencyMs": 320, "chunkMs": 320, "params": {"chunk_ms": 320}, "label": "320 ms · 16 frames"},
]


def profile(name: str) -> LatencyProfile:
    for p in PROFILES:
        if p["name"] == name:
            return p
    raise KeyError(name)


FAMILY = Family(
    runtime=RUNTIME,
    descriptor={
        "name": NAME,
        "version": "1",
        "title": "Toy CTC (CPU)",
        "framework": "pytorch",
        "architecture": "linear + unidirectional GRU + CTC head, about 60 k parameters",
        "formats": ["toy-pt"],
        "input": {"sampleRate": SAMPLE_RATE, "channels": 1},
        "features": feature_config(),
        "tokenizer": "chars",
        "capabilities": {
            "streaming": True,
            "wordTimestamps": True,
            "confidence": True,
            "boosting": "",
            "languagePrompt": False,
            "trainModes": ["scratch"],
        },
        "latencyProfiles": PROFILES,
        "roles": {
            "calibrate": "toy_calibrate",
            "train": "toy_train",
            "average": "toy_average",
            "transcribe": "toy_transcribe",
        },
        "defaultsSection": "packs.toy",
        "help": "guides.toy-pack",
    },
    fixtures=Path(__file__).resolve().parent / "fixtures",
    conformance={
        "train": {"steps": 40},
        "resume": {"steps": 80},
        "stop": {"steps": 5000},
    },
)
