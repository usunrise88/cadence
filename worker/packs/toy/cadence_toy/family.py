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
        "architecture": "linear + layer norm + unidirectional GRU + CTC head, about 60 k parameters",
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
            "materialize": "toy_checkpoint_from_base",
            "live": "toy_live",
            # Phase 5: an in-process deployable and its served decode keep the export → parity → benchmark seam honest
            # on a CPU; the parity reference is the family's own transcribe step.
            "export": "toy_export",
            "parity": "toy_transcribe",
            "serve": "toy_serve",
        },
        "exportFormats": [{"format": "toy-pt-dir", "server": "toy", "default": True}],
        # A live session runs on the CPU (toy_live needs no card); the reservation only matters for GPU kinds.
        "interactive": {"memoryMb": 512},
        "defaultsSection": "packs.toy",
        "help": "guides.toy-pack",
    },
    fixtures=Path(__file__).resolve().parent / "fixtures",
    conformance={
        # The toy "base model": the untrained network from a seed (materialize role).
        "base_model": {"seed": 0},
        # Half the default budget, then resume to the default 300 steps: the fixtures reach a WER of 0 by then.
        "train": {"steps": 150},
        "resume": {"steps": 300},
        "stop": {"steps": 5000},
        "baseline": {"steps": 1},
        "score": {"maxWer": 0.1},
    },
)
